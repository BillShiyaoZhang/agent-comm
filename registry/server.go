// Package registry provides URN -> PeerID/Addrs resolution via libp2p streams.
package registry

import (
	"fmt"
	"io"
	"sync"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/multiformats/go-multiaddr"
	agentpb "github.com/BillShiyaoZhang/agent-comm/proto"
	goproto "google.golang.org/protobuf/proto"
)

const ProtoID = "/hermes/agent-comm/registry/1.0.0"

// RegistryEntry holds the full registration info for a URN.
type RegistryEntry struct {
	Info           peer.AddrInfo
	X25519PubKey   []byte // X25519 public key for ECIES
	Ed25519PubKey  []byte // Ed25519 identity public key
	Signature      []byte // Self-signature
	Timestamp      int64  // Registration timestamp
	StoresUserData bool   // Storage policy
	RelayAddrs     []string
}

// Store defines the registry database backend interface.
type Store interface {
	Register(urn, peerID string, addrs []string, x25519PubKey []byte) (bool, string)
	Resolve(urn string) (peerID string, addrs []string, x25519PubKey []byte, found bool)
	RegisterWithSignature(urn, peerID string, addrs, relayAddrs []string, x25519PK, ed25519PK, signature []byte, storesUserData bool, timestamp int64) error
	ResolveExtended(urn string) (peerID string, addrs, relayAddrs []string, x25519PK, ed25519PK, signature []byte, storesUserData bool, timestamp int64, found bool)
}

// InMemoryStore is the default in-memory registry database.
type InMemoryStore struct {
	mu sync.RWMutex
	账册 map[string]RegistryEntry
}

// NewInMemoryStore creates a new InMemoryStore.
func NewInMemoryStore() *InMemoryStore {
	return &InMemoryStore{账册: make(map[string]RegistryEntry)}
}

// Register adds or updates a registration entry.
func (s *InMemoryStore) Register(urn, peerID string, addrs []string, x25519PubKey []byte) (bool, string) {
	err := s.RegisterWithSignature(urn, peerID, addrs, nil, x25519PubKey, nil, nil, false, 0)
	if err != nil {
		return false, err.Error()
	}
	return true, ""
}

// Resolve looks up the entry for a URN.
func (s *InMemoryStore) Resolve(urn string) (string, []string, []byte, bool) {
	pid, addrs, _, x25519PubKey, _, _, _, _, found := s.ResolveExtended(urn)
	return pid, addrs, x25519PubKey, found
}

// RegisterWithSignature registers an entry with signature validation data.
func (s *InMemoryStore) RegisterWithSignature(urn, peerID string, addrs, relayAddrs []string, x25519PK, ed25519PK, signature []byte, storesUserData bool, timestamp int64) error {
	if urn == "" || peerID == "" {
		return fmt.Errorf("urn and peer_id are required")
	}
	pid, err := peer.Decode(peerID)
	if err != nil {
		return fmt.Errorf("invalid peer_id: %w", err)
	}
	var maddrs []multiaddr.Multiaddr
	for _, a := range addrs {
		m, err := multiaddr.NewMultiaddr(a)
		if err != nil {
			continue
		}
		maddrs = append(maddrs, m)
	}

	s.mu.Lock()
	s.账册[urn] = RegistryEntry{
		Info:           peer.AddrInfo{ID: pid, Addrs: maddrs},
		X25519PubKey:   x25519PK,
		Ed25519PubKey:  ed25519PK,
		Signature:      signature,
		Timestamp:      timestamp,
		StoresUserData: storesUserData,
		RelayAddrs:     relayAddrs,
	}
	s.mu.Unlock()
	return nil
}

// ResolveExtended looks up the extended signature entry for a URN.
func (s *InMemoryStore) ResolveExtended(urn string) (string, []string, []string, []byte, []byte, []byte, bool, int64, bool) {
	s.mu.RLock()
	entry, ok := s.账册[urn]
	s.mu.RUnlock()
	if !ok {
		return "", nil, nil, nil, nil, nil, false, 0, false
	}
	addrs := make([]string, len(entry.Info.Addrs))
	for i, a := range entry.Info.Addrs {
		addrs[i] = a.String()
	}
	return entry.Info.ID.String(), addrs, entry.RelayAddrs, entry.X25519PubKey, entry.Ed25519PubKey, entry.Signature, entry.StoresUserData, entry.Timestamp, true
}

// ListURNs returns all registered URNs.
func (s *InMemoryStore) ListURNs() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	urns := make([]string, 0, len(s.账册))
	for urn := range s.账册 {
		urns = append(urns, urn)
	}
	return urns
}

// HandleRegister registers an entry directly with peer types.
func (s *InMemoryStore) HandleRegister(urn string, pid peer.ID, addrs []multiaddr.Multiaddr, x25519PubKey []byte) {
	s.mu.Lock()
	s.账册[urn] = RegistryEntry{
		Info:         peer.AddrInfo{ID: pid, Addrs: addrs},
		X25519PubKey: x25519PubKey,
	}
	s.mu.Unlock()
}

// Server handles URN registration and resolution via libp2p streams.
type Server struct {
	host  host.Host
	store Store
}

// NewServer creates a registry server attached to the given host and store.
func NewServer(h host.Host, store Store) *Server {
	return &Server{host: h, store: store}
}

// HandleStream services a registry request over a libp2p stream.
func (s *Server) HandleStream(stream network.Stream) {
	buf, err := io.ReadAll(stream)
	if err != nil {
		fmt.Printf("[registry] read error: %v\n", err)
		stream.Close()
		return
	}
	fmt.Printf("[registry] received %d bytes\n", len(buf))

	var req agentpb.URNRegistryRequest
	if err := goproto.Unmarshal(buf, &req); err != nil {
		fmt.Printf("[registry] unmarshal error: %v\n", err)
		stream.Close()
		return
	}

	var resp agentpb.URNRegistryResponse

	switch op := req.Op.(type) {
	case *agentpb.URNRegistryRequest_Register:
		ok, info := s.handleRegister(op.Register)
		resp.Op = &agentpb.URNRegistryResponse_Register{
			Register: &agentpb.RegisterResponse{Ok: ok, Info: info},
		}
	case *agentpb.URNRegistryRequest_Resolve:
		peerID, addrs, relayAddrs, x25519PubKey, ed25519PubKey, signature, storesUserData, timestamp, found := s.handleResolve(op.Resolve)
		resp.Op = &agentpb.URNRegistryResponse_Resolve{
			Resolve: &agentpb.ResolveResponse{
				Found:            found,
				PeerId:           peerID,
				Addrs:            addrs,
				X25519Pubkey:     x25519PubKey,
				Ed25519Pubkey:    ed25519PubKey,
				Signature:        signature,
				Timestamp:        timestamp,
				StoresUserData:   storesUserData,
				RelayAddrs:       relayAddrs,
			},
		}
	}

	respBytes, _ := goproto.Marshal(&resp)
	stream.Write(respBytes)
	stream.Close()
}

func (s *Server) handleRegister(r *agentpb.RegisterRequest) (bool, string) {
	err := s.store.RegisterWithSignature(r.Urn, r.PeerId, r.Addrs, r.RelayAddrs, r.X25519Pubkey, r.Ed25519Pubkey, r.Signature, r.StoresUserData, r.Timestamp)
	if err != nil {
		return false, err.Error()
	}
	return true, ""
}

func (s *Server) handleResolve(r *agentpb.ResolveRequest) (string, []string, []string, []byte, []byte, []byte, bool, int64, bool) {
	return s.store.ResolveExtended(r.Urn)
}

// ListURNs returns all registered URNs from the underlying store.
func (s *Server) ListURNs() []string {
	if lister, ok := s.store.(interface{ ListURNs() []string }); ok {
		return lister.ListURNs()
	}
	return nil
}

// HandleRegister registers a URN -> PeerID/addrs mapping locally.
func (s *Server) HandleRegister(urn string, pid peer.ID, addrs []multiaddr.Multiaddr, x25519PubKey []byte) {
	if regger, ok := s.store.(interface {
		HandleRegister(urn string, pid peer.ID, addrs []multiaddr.Multiaddr, x25519PubKey []byte)
	}); ok {
		regger.HandleRegister(urn, pid, addrs, x25519PubKey)
	} else {
		addrsStrs := make([]string, len(addrs))
		for i, a := range addrs {
			addrsStrs[i] = a.String()
		}
		s.store.Register(urn, pid.String(), addrsStrs, x25519PubKey)
	}
}

// Register sets the stream handler on the host.
func (s *Server) Register() {
	s.host.SetStreamHandler(protocol.ID(ProtoID), func(stream network.Stream) {
		s.HandleStream(stream)
	})
}