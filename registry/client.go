// Package registry provides URN -> PeerID/Addrs resolution via libp2p streams.
package registry

import (
	"context"
	"fmt"
	"io"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/multiformats/go-multiaddr"
	agentpb "github.com/nousresearch/hermes-agent/agent-comm/proto"
	goproto "google.golang.org/protobuf/proto"
)

// ResolveResult holds the resolved address info and X25519 public key.
type ResolveResult struct {
	peer.AddrInfo
	X25519PubKey []byte // X25519 public key for ECIES encryption (nil if not registered)
}

// Client resolves URNs by querying a registry server via libp2p streams.
type Client struct {
	host host.Host
}

// NewClient creates a new registry client using the given host.
func NewClient(h host.Host) *Client {
	return &Client{host: h}
}

// Resolve queries a registry server for the PeerID, addresses, and X25519 pubkey of a URN.
func (c *Client) Resolve(target peer.AddrInfo, urn string) (ResolveResult, error) {
	req := &agentpb.URNRegistryRequest{
		Op: &agentpb.URNRegistryRequest_Resolve{
			Resolve: &agentpb.ResolveRequest{Urn: urn},
		},
	}
	reqBytes, err := goproto.Marshal(req)
	if err != nil {
		return ResolveResult{}, err
	}

	stream, err := c.host.NewStream(context.Background(), target.ID, protocol.ID(ProtoID))
	if err != nil {
		return ResolveResult{}, fmt.Errorf("failed to open stream: %w", err)
	}

	if _, err := stream.Write(reqBytes); err != nil {
		stream.Reset()
		return ResolveResult{}, fmt.Errorf("failed to write request: %w", err)
	}
	if err := stream.CloseWrite(); err != nil {
		stream.Reset()
		return ResolveResult{}, fmt.Errorf("failed to signal write done: %w", err)
	}

	buf, err := io.ReadAll(stream)
	stream.Close()
	if err != nil {
		return ResolveResult{}, fmt.Errorf("failed to read response: %w", err)
	}

	var resp agentpb.URNRegistryResponse
	if err := goproto.Unmarshal(buf, &resp); err != nil {
		return ResolveResult{}, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	resolve, ok := resp.Op.(*agentpb.URNRegistryResponse_Resolve)
	if !ok {
		return ResolveResult{}, fmt.Errorf("unexpected response type")
	}
	if !resolve.Resolve.Found {
		return ResolveResult{}, fmt.Errorf("URN not found: %s", urn)
	}

	pid, err := peer.Decode(resolve.Resolve.PeerId)
	if err != nil {
		return ResolveResult{}, fmt.Errorf("invalid peer_id in response: %w", err)
	}

	addrs := make([]multiaddr.Multiaddr, 0, len(resolve.Resolve.Addrs))
	for _, a := range resolve.Resolve.Addrs {
		m, err := multiaddr.NewMultiaddr(a)
		if err != nil {
			continue
		}
		addrs = append(addrs, m)
	}

	return ResolveResult{
		AddrInfo:    peer.AddrInfo{ID: pid, Addrs: addrs},
		X25519PubKey: resolve.Resolve.X25519Pubkey,
	}, nil
}

// Register tells the registry server about this node's URN -> PeerID/addrs mapping.
func (c *Client) Register(target peer.AddrInfo, urn string, addrs []multiaddr.Multiaddr, x25519PubKey []byte) error {
	addrsStr := make([]string, len(addrs))
	for i, a := range addrs {
		addrsStr[i] = a.String()
	}

	req := &agentpb.URNRegistryRequest{
		Op: &agentpb.URNRegistryRequest_Register{
			Register: &agentpb.RegisterRequest{
				Urn:          urn,
				PeerId:       c.host.ID().String(),
				Addrs:        addrsStr,
				X25519Pubkey: x25519PubKey,
			},
		},
	}
	reqBytes, err := goproto.Marshal(req)
	if err != nil {
		return err
	}

	stream, err := c.host.NewStream(context.Background(), target.ID, protocol.ID(ProtoID))
	if err != nil {
		return fmt.Errorf("failed to open stream: %w", err)
	}

	if _, err := stream.Write(reqBytes); err != nil {
		stream.Reset()
		return fmt.Errorf("failed to write request: %w", err)
	}
	if err := stream.CloseWrite(); err != nil {
		stream.Reset()
		return fmt.Errorf("failed to signal write done: %w", err)
	}

	buf, err := io.ReadAll(stream)
	stream.Close()
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	var resp agentpb.URNRegistryResponse
	if err := goproto.Unmarshal(buf, &resp); err != nil {
		return fmt.Errorf("failed to unmarshal response: %w", err)
	}

	reg, ok := resp.Op.(*agentpb.URNRegistryResponse_Register)
	if !ok {
		return fmt.Errorf("unexpected response type")
	}
	if !reg.Register.Ok {
		return fmt.Errorf("registration failed: %s", reg.Register.Info)
	}
	return nil
}