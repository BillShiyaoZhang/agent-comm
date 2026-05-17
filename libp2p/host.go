// Package libp2p provides the network transport layer for agent-comm.
package libp2p

import (
	"context"
	"fmt"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
)

// Config holds the libp2p host configuration.
type Config struct {
	ListenAddrs   []string   // Multiaddr strings to listen on
	EnableRelay   bool       // Enable Circuit Relay
	EnableDHT     bool       // Enable DHT (Phase 2)
	PrivKeyBytes  []byte     // Ed25519 private key bytes (for persistent identity)
	ProtocolID    string     // Base protocol ID
	ResourceConns int        // Max concurrent connections
}

// DefaultConfig returns the default configuration.
func DefaultConfig() Config {
	return Config{
		ListenAddrs: []string{
			"/ip4/0.0.0.0/tcp/0",
			"/ip4/0.0.0.0/udp/0/quic",
		},
		EnableRelay:   true,
		EnableDHT:     false,
		ProtocolID:    "/hermes/agent-comm/1.0.0",
		ResourceConns: 64,
	}
}

// NewHost creates a new libp2p host.
// If PrivKeyBytes is provided, the host will use it for identity (enabling persistence).
// Otherwise, a new random identity is generated.
func NewHost(cfg Config) (host.Host, error) {
	opts := []libp2p.Option{
		libp2p.ListenAddrStrings(cfg.ListenAddrs...),
		libp2p.EnableNATService(),
	}

	// Use provided private key for persistent identity
	if cfg.PrivKeyBytes != nil {
		privKey, err := crypto.UnmarshalEd25519PrivateKey(cfg.PrivKeyBytes)
		if err != nil {
			return nil, fmt.Errorf("failed to unmarshal private key: %w", err)
		}
		opts = append(opts, libp2p.Identity(privKey))
	}

	// Enable Circuit Relay
	if cfg.EnableRelay {
		opts = append(opts, libp2p.EnableRelay())
	}

	h, err := libp2p.New(opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create libp2p host: %w", err)
	}

	return h, nil
}

// ConnectToPeer attempts to establish a connection to a peer by address.
// Address format: /ip4/x.x.x.x/tcp/y/p2p/PeerID
func ConnectToPeer(ctx context.Context, h host.Host, addr string) error {
	addrInfo, err := peer.AddrInfoFromString(addr)
	if err != nil {
		return fmt.Errorf("invalid address: %w", err)
	}

	h.Peerstore().AddAddrs(addrInfo.ID, addrInfo.Addrs, peerstore.TempAddrTTL)

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	return h.Connect(ctx, *addrInfo)
}

// GetPeerID returns the host's peer ID as a string (base58-encoded).
func GetPeerID(h host.Host) string {
	return h.ID().String()
}

// GetPeerAddrs returns all known addresses for the host in p2p format.
func GetPeerAddrs(h host.Host) []string {
	var addrs []string
	for _, addr := range h.Addrs() {
		addrStr := addr.String() + "/p2p/" + h.ID().String()
		addrs = append(addrs, addrStr)
	}
	return addrs
}

// CloseHost gracefully closes the host.
func CloseHost(h host.Host) error {
	if h == nil {
		return nil
	}
	return h.Close()
}

// AddrsWithID returns listen addresses with the peer ID appended.
func AddrsWithID(h host.Host) []string {
	return GetPeerAddrs(h)
}

// ProtocolIDForTopic returns the full protocol ID for a topic.
func ProtocolIDForTopic(topic string) string {
	return fmt.Sprintf("/hermes/agent-comm/%s/1.0.0", topic)
}