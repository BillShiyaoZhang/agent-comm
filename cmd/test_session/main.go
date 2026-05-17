package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/nousresearch/hermes-agent/agent-comm/crypto"
	"github.com/nousresearch/hermes-agent/agent-comm/dht"
	"github.com/nousresearch/hermes-agent/agent-comm/libp2p"
	"github.com/nousresearch/hermes-agent/agent-comm/proto"
	"github.com/nousresearch/hermes-agent/agent-comm/registry"
	"github.com/nousresearch/hermes-agent/agent-comm/session"
	goproto "google.golang.org/protobuf/proto"
)

// Protocol-level AAD constant — both sides use this so AAD always matches.
// This avoids the PeerID↔URN→staticPubKey→PeerID derivation chain.
const aadConstant = "agent-comm-v1"

func main() {
	fmt.Println("=== agent-comm Two-Node Encrypted Message Test ===\n")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// --- Node A (Bootstrap/Server) ---
	dirA := "/tmp/node_a_keys"
	os.RemoveAll(dirA)
	keysA, err := crypto.LoadOrCreateIdentity(dirA)
	if err != nil {
		fmt.Fprintf(os.Stderr, "A: load keys: %v\n", err)
		return
	}
	peerIDA, _ := keysA.PeerID()
	fmt.Printf("Node A:\n  URN: %s\n  PeerID: %s\n\n", keysA.Ed25519.URN(), peerIDA)

	cfgA := libp2p.Config{
		ListenAddrs:  []string{"/ip4/0.0.0.0/tcp/47101", "/ip4/0.0.0.0/udp/0/quic"},
		EnableRelay:  true,
		PrivKeyBytes: keysA.Ed25519.PrivateKey,
	}
	hostA, err := libp2p.NewHost(cfgA)
	if err != nil {
		fmt.Fprintf(os.Stderr, "A: create host: %v\n", err)
		return
	}
	defer hostA.Close()

	dhtA, err := dht.NewDHT(ctx, hostA, dht.DHTConfig{Mode: dht.ModeServer})
	if err != nil {
		fmt.Fprintf(os.Stderr, "A: create DHT: %v\n", err)
		return
	}
	dht.Bootstrap(ctx, dhtA)
	fmt.Printf("Node A listening on: %v\n\n", hostA.Addrs())

	// Registry server on A
	regA := registry.NewServer(hostA)
	regA.Register()
	regA.HandleRegister(keysA.Ed25519.URN(), hostA.ID(), hostA.Addrs(), keysA.X25519PK)

	// Session server on A
	mgrA := session.NewManager(hostA, keysA)
	hostA.SetStreamHandler(session.ProtoID, func(stream network.Stream) {
		handleIncomingSession(stream, keysA, mgrA)
	})

	// --- Node B (Client) ---
	dirB := "/tmp/node_b_keys"
	os.RemoveAll(dirB)
	keysB, err := crypto.LoadOrCreateIdentity(dirB)
	if err != nil {
		fmt.Fprintf(os.Stderr, "B: load keys: %v\n", err)
		return
	}
	peerIDB, _ := keysB.PeerID()
	fmt.Printf("Node B:\n  URN: %s\n  PeerID: %s\n\n", keysB.Ed25519.URN(), peerIDB)

	cfgB := libp2p.Config{
		ListenAddrs:  []string{"/ip4/0.0.0.0/tcp/47102", "/ip4/0.0.0.0/udp/0/quic"},
		EnableRelay:  true,
		PrivKeyBytes: keysB.Ed25519.PrivateKey,
	}
	hostB, err := libp2p.NewHost(cfgB)
	if err != nil {
		fmt.Fprintf(os.Stderr, "B: create host: %v\n", err)
		return
	}
	defer hostB.Close()

	// Connect B to A
	addrInfoA := peer.AddrInfo{ID: hostA.ID(), Addrs: hostA.Addrs()}
	hostB.Peerstore().AddAddrs(addrInfoA.ID, addrInfoA.Addrs, peerstore.TempAddrTTL)
	if err := hostB.Connect(ctx, addrInfoA); err != nil {
		fmt.Fprintf(os.Stderr, "B: connect to A: %v\n", err)
		return
	}
	fmt.Printf("Node B connected to Node A\n")

	// DHT on B
	dhtB, err := dht.NewDHT(ctx, hostB, dht.DHTConfig{Mode: dht.ModeClient})
	if err != nil {
		fmt.Fprintf(os.Stderr, "B: create DHT: %v\n", err)
		return
	}
	dht.Bootstrap(ctx, dhtB)

	// Register B with A's registry (B's X25519 pubkey included)
	regClient := registry.NewClient(hostB)
	if err := regClient.Register(addrInfoA, keysB.Ed25519.URN(), hostB.Addrs(), keysB.X25519PK); err != nil {
		fmt.Fprintf(os.Stderr, "B: register: %v\n", err)
	} else {
		fmt.Printf("Node B registered with Node A's registry (X25519 pubkey included)\n")
	}
	fmt.Println()

	// --- Exchange messages: B -> A ---
	fmt.Println("--- B sends encrypted message to A ---")
	mgrB := session.NewManager(hostB, keysB)

	// Get A's pubkey from registry
	resolvedA, err := regClient.Resolve(addrInfoA, keysA.Ed25519.URN())
	if err != nil {
		fmt.Fprintf(os.Stderr, "B: resolve A: %v\n", err)
		return
	}
	aPubKey := resolvedA.X25519PubKey
	if len(aPubKey) != 32 {
		fmt.Fprintf(os.Stderr, "B: A's X25519 pubkey not in registry or invalid length: %d\n", len(aPubKey))
		return
	}
	fmt.Printf("Got A's X25519 pubkey from registry: %x...\n", aPubKey[:8])

	reply, err := mgrB.SendMessage(ctx, addrInfoA, aPubKey, "Hello from Node B! Can you hear me?")
	if err != nil {
		fmt.Fprintf(os.Stderr, "B -> A FAILED: %v\n", err)
	} else {
		fmt.Printf("B -> A SUCCESS: A replied: %q\n\n", reply)
	}

	fmt.Println("Test complete.")
}

// handleIncomingSession handles an incoming session stream.
func handleIncomingSession(stream network.Stream, keys *crypto.IdentityKeys, mgr *session.Manager) {
	defer stream.Close()

	// Read envelope size (4-byte big-endian)
	sizeBuf := make([]byte, 4)
	if _, err := io.ReadFull(stream, sizeBuf); err != nil {
		fmt.Printf("[A] read size: %v\n", err)
		return
	}
	size := uint32(sizeBuf[0])<<24 | uint32(sizeBuf[1])<<16 | uint32(sizeBuf[2])<<8 | uint32(sizeBuf[3])

	// Read envelope
	envBytes := make([]byte, size)
	if _, err := io.ReadFull(stream, envBytes); err != nil {
		fmt.Printf("[A] read envelope: %v\n", err)
		return
	}

	var env proto.EncryptedEnvelope
	if err := goproto.Unmarshal(envBytes, &env); err != nil {
		fmt.Printf("[A] unmarshal: %v\n", err)
		return
	}
	fmt.Printf("[A] received from %s: %d ciphertext bytes, msg_id=%s\n",
		env.SenderUrn, len(env.Ciphertext), env.MessageId)

	// Decrypt: ECDH(my_static_SK, sender_static_PK) → shared secret
	sharedSecret, err := mgr.Ecies().ComputeSharedSecret(keys.X25519SK, env.SenderStaticPubkey)
	if err != nil {
		fmt.Printf("[A] ECDH failed: %v\n", err)
		stream.CloseWrite()
		return
	}

	// AAD = protocol constant (same for both directions, derived from sharedSecret HKDF)
	// The HKDF info "agent-comm-ephemeral-v1" already provides per-message key separation;
	// the additional AAD is the protocol label to bind this to our protocol.
	aad := sha256.Sum256([]byte(aadConstant))

	fullEphem := make([]byte, 32)
	copy(fullEphem, env.EphemeralPubkey)
	plaintext, err := mgr.Ecies().DecryptWithSharedSecret(
		sharedSecret, fullEphem, env.Nonce, env.Ciphertext, env.Tag, aad[:16])
	if err != nil {
		fmt.Printf("[A] decrypt FAILED: %v\n", err)
		stream.CloseWrite()
		return
	}

	var msg proto.ChatMessage
	if err := goproto.Unmarshal(plaintext, &msg); err != nil {
		fmt.Printf("[A] unmarshal chat: %v\n", err)
		return
	}

	if txt := msg.GetText(); txt != nil {
		fmt.Printf("[A] message: %q (ts=%d)\n", txt.Text, txt.Timestamp)
	}

	// Auto-reply: encrypt reply using ECDH with sender's static pubkey
	err = mgr.SendReply(stream, env.SenderStaticPubkey, env.SenderUrn,
		"Yes! Got your message loud and clear. This is an encrypted reply.")
	if err != nil {
		fmt.Printf("[A] send reply FAILED: %v\n", err)
	} else {
		fmt.Printf("[A] encrypted reply sent\n")
	}
	stream.CloseWrite()
}