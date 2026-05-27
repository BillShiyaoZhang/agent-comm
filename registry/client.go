// Package registry provides URN -> PeerID/Addrs resolution via libp2p streams.
package registry

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/BillShiyaoZhang/agent-comm/crypto"
	agentpb "github.com/BillShiyaoZhang/agent-comm/proto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/multiformats/go-multiaddr"
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

// HTTPClient resolves and registers URNs via the HTTP REST API.
type HTTPClient struct {
	BaseURL string
	Keys    *crypto.IdentityKeys
}

// NewHTTPClient creates a new registry HTTP client.
func NewHTTPClient(baseURL string, keys *crypto.IdentityKeys) *HTTPClient {
	return &HTTPClient{
		BaseURL: baseURL,
		Keys:    keys,
	}
}

type registerReq struct {
	URN           string   `json:"urn"`
	PeerID        string   `json:"peer_id"`
	Addrs         []string `json:"addrs"`
	RelayAddrs    []string `json:"relay_addrs"`
	X25519Pubkey  []byte   `json:"x25519_pubkey"`
	Ed25519Pubkey []byte   `json:"ed25519_pubkey"`
	Signature     []byte   `json:"signature"`
	Timestamp     int64    `json:"timestamp"`
}

// Register registers a URN with the HTTP Registry API using signature verification.
func (c *HTTPClient) Register(urn string, peerID string, addrs []string, relayAddrs []string, x25519PubKey []byte) error {
	timestamp := time.Now().Unix()

	// 1. Generate internal registry signature over (urn | peerID | timestamp)
	// msg := urn + "|" + peerID + "|" + timestamp (big-endian 8 bytes)
	tsBuf := make([]byte, 8)
	binary.BigEndian.PutUint64(tsBuf, uint64(timestamp))
	msg := append([]byte(urn+"|"+peerID+"|"), tsBuf...)
	sig := ed25519.Sign(c.Keys.Ed25519.PrivateKey, msg)

	reqObj := registerReq{
		URN:           urn,
		PeerID:        peerID,
		Addrs:         addrs,
		RelayAddrs:    relayAddrs,
		X25519Pubkey:  x25519PubKey,
		Ed25519Pubkey: c.Keys.Ed25519.PublicKey,
		Signature:     sig,
		Timestamp:     timestamp,
	}

	bodyBytes, err := json.Marshal(reqObj)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequest("POST", c.BaseURL+"/api/v1/registry/register", bytes.NewReader(bodyBytes))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	// 2. Generate HTTP Authorization payload signature over the JSON body
	payloadSig := ed25519.Sign(c.Keys.Ed25519.PrivateKey, bodyBytes)
	req.Header.Set("Authorization", "Ed25519 "+hex.EncodeToString(payloadSig)+":"+hex.EncodeToString(c.Keys.Ed25519.PublicKey))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("HTTP status %d: %s", resp.StatusCode, string(respBody))
	}

	var res map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	if res["ok"] != true {
		return fmt.Errorf("registration failed: ok is not true")
	}

	return nil
}

type resolveResp struct {
	Found        bool     `json:"found"`
	URN          string   `json:"urn"`
	PeerID       string   `json:"peer_id"`
	Addrs        []string `json:"addrs"`
	RelayAddrs   []string `json:"relay_addrs"`
	X25519Pubkey []byte   `json:"x25519_pubkey"`
	ExpiresAt    string   `json:"expires_at"`
}

// Resolve queries the HTTP registry for a URN.
func (c *HTTPClient) Resolve(urn string) (ResolveResult, error) {
	resp, err := http.Get(c.BaseURL + "/api/v1/registry/resolve?urn=" + urn)
	if err != nil {
		return ResolveResult{}, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return ResolveResult{}, fmt.Errorf("URN not found: %s", urn)
	}
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return ResolveResult{}, fmt.Errorf("HTTP status %d: %s", resp.StatusCode, string(respBody))
	}

	var res resolveResp
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return ResolveResult{}, fmt.Errorf("decode response: %w", err)
	}

	if !res.Found {
		return ResolveResult{}, fmt.Errorf("URN not found: %s", urn)
	}

	pid, err := peer.Decode(res.PeerID)
	if err != nil {
		return ResolveResult{}, fmt.Errorf("invalid peer_id: %w", err)
	}

	addrs := make([]multiaddr.Multiaddr, 0, len(res.Addrs))
	for _, a := range res.Addrs {
		m, err := multiaddr.NewMultiaddr(a)
		if err != nil {
			continue
		}
		addrs = append(addrs, m)
	}

	return ResolveResult{
		AddrInfo:     peer.AddrInfo{ID: pid, Addrs: addrs},
		X25519PubKey: res.X25519Pubkey,
	}, nil
}