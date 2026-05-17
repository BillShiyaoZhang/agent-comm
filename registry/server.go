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
	agentpb "github.com/nousresearch/hermes-agent/agent-comm/proto"
	goproto "google.golang.org/protobuf/proto"
)

const ProtoID = "/hermes/agent-comm/registry/1.0.0"

// RegistryEntry holds the full registration info for a URN.
type RegistryEntry struct {
	Info        peer.AddrInfo
	X25519PubKey []byte // X25519 public key for ECIES
}

// Server handles URN registration and resolution via libp2p streams.
type Server struct {
	host  host.Host
	mu    sync.RWMutex
	账册 map[string]RegistryEntry
}

// NewServer creates a registry server attached to the given host.
func NewServer(h host.Host) *Server {
	return &Server{host: h, 账册: make(map[string]RegistryEntry)}
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
		peerID, addrs, x25519PubKey, found := s.handleResolve(op.Resolve)
		resp.Op = &agentpb.URNRegistryResponse_Resolve{
			Resolve: &agentpb.ResolveResponse{Found: found, PeerId: peerID, Addrs: addrs, X25519Pubkey: x25519PubKey},
		}
	}

	respBytes, _ := goproto.Marshal(&resp)
	stream.Write(respBytes)
	stream.Close()
}

func (s *Server) handleRegister(r *agentpb.RegisterRequest) (bool, string) {
	if r.Urn == "" || r.PeerId == "" {
		return false, "urn and peer_id are required"
	}
	pid, err := peer.Decode(r.PeerId)
	if err != nil {
		return false, fmt.Sprintf("invalid peer_id: %v", err)
	}
	var addrs []multiaddr.Multiaddr
	for _, a := range r.Addrs {
		m, err := multiaddr.NewMultiaddr(a)
		if err != nil {
			continue
		}
		addrs = append(addrs, m)
	}

	s.mu.Lock()
	s.账册[r.Urn] = RegistryEntry{
		Info:         peer.AddrInfo{ID: pid, Addrs: addrs},
		X25519PubKey: r.X25519Pubkey,
	}
	s.mu.Unlock()
	return true, ""
}

func (s *Server) handleResolve(r *agentpb.ResolveRequest) (string, []string, []byte, bool) {
	s.mu.RLock()
	entry, ok := s.账册[r.Urn]
	s.mu.RUnlock()
	if !ok {
		return "", nil, nil, false
	}
	addrs := make([]string, len(entry.Info.Addrs))
	for i, a := range entry.Info.Addrs {
		addrs[i] = a.String()
	}
	return entry.Info.ID.String(), addrs, entry.X25519PubKey, true
}

// ListURNs returns all registered URNs.
func (s *Server) ListURNs() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	urns := make([]string, 0, len(s.账册))
	for urn := range s.账册 {
		urns = append(urns, urn)
	}
	return urns
}

// HandleRegister registers a URN -> PeerID/addrs mapping locally (used by the bootstrap for its own entry).
func (s *Server) HandleRegister(urn string, pid peer.ID, addrs []multiaddr.Multiaddr, x25519PubKey []byte) {
	s.mu.Lock()
	s.账册[urn] = RegistryEntry{
		Info:         peer.AddrInfo{ID: pid, Addrs: addrs},
		X25519PubKey: x25519PubKey,
	}
	s.mu.Unlock()
}

// Register sets the stream handler on the host.
func (s *Server) Register() {
	s.host.SetStreamHandler(protocol.ID(ProtoID), func(stream network.Stream) {
		s.HandleStream(stream)
	})
}