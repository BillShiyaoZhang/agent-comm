// Package dht provides Kademlia DHT integration for peer discovery.
// Phase 2: Implements automated peer discovery via go-libp2p-kad-dht.
package dht

import (
	"context"
	"fmt"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"

	dht "github.com/libp2p/go-libp2p-kad-dht"
)

// Re-exported DHT mode constants so callers don't need to import go-libp2p-kad-dht directly.
const (
	ModeClient ModeOpt = dht.ModeClient
	ModeServer ModeOpt = dht.ModeServer
	ModeAuto   ModeOpt = dht.ModeAuto
)

// ModeOpt is the DHT operating mode.
type ModeOpt = dht.ModeOpt

// DHTConfig holds DHT configuration.
type DHTConfig struct {
	Mode       ModeOpt
	Bootstraps []peer.AddrInfo // Bootstrap peers for client mode
}

// DefaultConfig returns the default DHT configuration (client mode).
func DefaultConfig() DHTConfig {
	return DHTConfig{
		Mode:       ModeClient,
		Bootstraps: nil,
	}
}

// NewDHT creates a new DHT instance attached to the given libp2p host.
// In client mode, it will use the provided bootstrap peers if no peers are found locally.
// In server mode, it will respond to DHT queries from other peers.
func NewDHT(ctx context.Context, h host.Host, cfg DHTConfig) (*dht.IpfsDHT, error) {
	opts := []dht.Option{
		dht.Mode(cfg.Mode),
		dht.Concurrency(10),
		dht.Resiliency(3),
	}

	if len(cfg.Bootstraps) > 0 {
		opts = append(opts, dht.BootstrapPeers(cfg.Bootstraps...))
	}

	d, err := dht.New(ctx, h, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create DHT: %w", err)
	}

	return d, nil
}

// FindPeer searches for a peer by ID in the DHT.
// Returns the peer's AddrInfo if found.
// This is the core of Phase 2: given a PeerID (derived from a URN), find addresses.
func FindPeer(ctx context.Context, d *dht.IpfsDHT, pid peer.ID) (peer.AddrInfo, error) {
	return d.FindPeer(ctx, pid)
}

// Bootstrap connects to bootstrap peers and populates the routing table.
// Call this after creating a DHT to join the network.
func Bootstrap(ctx context.Context, d *dht.IpfsDHT) error {
	return d.Bootstrap(ctx)
}

// Refresh triggers an async DHT routing table refresh.
func Refresh(ctx context.Context, d *dht.IpfsDHT) {
	d.RefreshRoutingTable()
}

// ListPeers returns all known peer IDs from the DHT routing table.
func ListPeers(d *dht.IpfsDHT) []peer.ID {
	return d.RoutingTable().ListPeers()
}

// GetRoutingTableSize returns the number of peers in the routing table.
func GetRoutingTableSize(d *dht.IpfsDHT) int {
	return d.RoutingTable().Size()
}

// Mode returns the current DHT mode (client/server).
func Mode(d *dht.IpfsDHT) ModeOpt {
	return d.Mode()
}