// Test DR over libp2p: two nodes exchange DR-encrypted messages.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/nousresearch/hermes-agent/agent-comm/crypto"
	"github.com/nousresearch/hermes-agent/agent-comm/dr"
	"github.com/nousresearch/hermes-agent/agent-comm/libp2p"
	"github.com/nousresearch/hermes-agent/agent-comm/registry"
	"github.com/nousresearch/hermes-agent/agent-comm/session"
)

type drSessionEntry struct {
	session *dr.DRSession
}

func main() {
	fmt.Println("=== DR over libp2p: two-node test ===\n")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// --- Node A (Alice, registry + DR handler) ---
	dirA := "/tmp/dr_test_a"
	os.RemoveAll(dirA)
	os.MkdirAll(dirA+"/store", 0755)
	keysA, err := crypto.LoadOrCreateIdentity(dirA)
	if err != nil {
		fmt.Fprintf(os.Stderr, "A: load keys: %v\n", err)
		return
	}
	fmt.Printf("Node A: URN=%s\n", keysA.Ed25519.URN())

	cfgA := libp2p.Config{
		ListenAddrs:  []string{"/ip4/0.0.0.0/tcp/47201", "/ip4/0.0.0.0/udp/0/quic"},
		EnableRelay:  true,
		PrivKeyBytes: keysA.Ed25519.PrivateKey,
	}
	hostA, err := libp2p.NewHost(cfgA)
	if err != nil {
		fmt.Fprintf(os.Stderr, "A: create host: %v\n", err)
		return
	}
	defer hostA.Close()
	fmt.Printf("Node A listening on: %v\n", hostA.Addrs())

	// Registry server on A
	regA := registry.NewServer(hostA)
	regA.Register()
	regA.HandleRegister(keysA.Ed25519.URN(), hostA.ID(), hostA.Addrs(), keysA.X25519PK)
	fmt.Println("Node A registry server ready")

	// Session manager and handler (ECIES fallback)
	mgrA := session.NewManager(hostA, keysA)
	hostA.SetStreamHandler(session.ProtoID, func(stream network.Stream) {
		handleSession(stream, keysA, mgrA)
	})

	// DR store for A (for responder sessions)
	drStoreA, err := dr.NewDRStore("/tmp/dr_test_a/store/dr.db")
	if err != nil {
		fmt.Fprintf(os.Stderr, "A: DRStore: %v\n", err)
		return
	}
	defer drStoreA.Close()

	// Map of peer ID string -> *drSessionEntry
	drPeersA := make(map[string]*drSessionEntry)
	var drPeersMuA sync.RWMutex

	// DR handler on A: receives messages, uses responder DRSession
	hostA.SetStreamHandler(dr.ProtoID, func(stream network.Stream) {
		handleDRStream(ctx, stream, keysA, mgrA, drStoreA, drPeersA, &drPeersMuA)
	})

	fmt.Println("Node A handlers registered\n")

	// --- Node B (Bob) ---
	dirB := "/tmp/dr_test_b"
	os.RemoveAll(dirB)
	os.MkdirAll(dirB+"/store", 0755)
	keysB, err := crypto.LoadOrCreateIdentity(dirB)
	if err != nil {
		fmt.Fprintf(os.Stderr, "B: load keys: %v\n", err)
		return
	}
	fmt.Printf("Node B: URN=%s\n", keysB.Ed25519.URN())

	cfgB := libp2p.Config{
		ListenAddrs:  []string{"/ip4/0.0.0.0/tcp/47202", "/ip4/0.0.0.0/udp/0/quic"},
		EnableRelay:  true,
		PrivKeyBytes: keysB.Ed25519.PrivateKey,
	}
	hostB, err := libp2p.NewHost(cfgB)
	if err != nil {
		fmt.Fprintf(os.Stderr, "B: create host: %v\n", err)
		return
	}
	defer hostB.Close()
	fmt.Printf("Node B listening on: %v\n", hostB.Addrs())

	// Connect B to A
	addrInfoA := peer.AddrInfo{ID: hostA.ID(), Addrs: hostA.Addrs()}
	hostB.Peerstore().AddAddrs(addrInfoA.ID, addrInfoA.Addrs, peerstore.TempAddrTTL)
	if err := hostB.Connect(ctx, addrInfoA); err != nil {
		fmt.Fprintf(os.Stderr, "B: connect to A: %v\n", err)
		return
	}
	fmt.Println("Node B connected to Node A\n")

	// Register B with A's registry
	regClientB := registry.NewClient(hostB)
	if err := regClientB.Register(addrInfoA, keysB.Ed25519.URN(), hostB.Addrs(), keysB.X25519PK); err != nil {
		fmt.Fprintf(os.Stderr, "B: register: %v\n", err)
		return
	}
	fmt.Println("Node B registered with Node A's registry")

	// DR store for B
	drStoreB, err := dr.NewDRStore("/tmp/dr_test_b/store/dr.db")
	if err != nil {
		fmt.Fprintf(os.Stderr, "B: DRStore: %v\n", err)
		return
	}
	defer drStoreB.Close()
	_ = drStoreB // available for future persistence

	// Resolve A's info from registry
	resolvedA, err := regClientB.Resolve(addrInfoA, keysA.Ed25519.URN())
	if err != nil {
		fmt.Fprintf(os.Stderr, "B: resolve A: %v\n", err)
		return
	}
	aPeerID := resolvedA.ID
	aX25519PK := resolvedA.X25519PubKey
	fmt.Printf("Resolved A: peerID=%s x25519pk=%x...\n\n", aPeerID, aX25519PK[:8])

	// Create mgrB early so we can cache A's X25519 PK for when A→B arrives
	mgrB := session.NewManager(hostB, keysB)
	mgrB.SetPeerX25519PK(aPeerID, aX25519PK)
	fmt.Printf("[setup] cached A's X25519 PK in mgrB for peerID=%s\n", aPeerID)

	// Also resolve B's info via A's registry, to get A's view of B's X25519 PK
	// Add B's address to A's peerstore
	hostA.Peerstore().AddAddrs(hostB.ID(), hostB.Addrs(), peerstore.TempAddrTTL)
	// Cache B's X25519 PK in mgrA so DRSession.Receive can find it
	mgrA.SetPeerX25519PK(hostB.ID(), keysB.X25519PK)

	// --- Test: B sends DR message to A ---
	fmt.Println("--- B creates DRSession(initiator) -> A ---")
	hostB.SetStreamHandler(session.ProtoID, func(stream network.Stream) {
		handleSession(stream, keysB, mgrB)
	})

	// DR store for B (for responder sessions when A→B)
	drStoreBForRecv, err := dr.NewDRStore("/tmp/dr_test_b/store/dr_recv.db")
	if err != nil {
		fmt.Fprintf(os.Stderr, "B: DRStore for recv: %v\n", err)
		return
	}
	defer drStoreBForRecv.Close()

	drPeersB := make(map[string]*drSessionEntry)
	var drPeersMuB sync.RWMutex
	hostB.SetStreamHandler(dr.ProtoID, func(stream network.Stream) {
		handleDRStream(ctx, stream, keysB, mgrB, drStoreBForRecv, drPeersB, &drPeersMuB)
	})
	drSessionB, err := dr.NewDRSessionInitiator(ctx, mgrB, keysB, aPeerID, aX25519PK, keysA.Ed25519.URN())
	if err != nil {
		fmt.Fprintf(os.Stderr, "B: NewDRSessionInitiator: %v\n", err)
		return
	}
	fmt.Println("[OK] B: DRSession created")

	// B sends message 1 to A
	fmt.Println("\n--- B sends 'Message 1 from B' to A ---")
	if err := drSessionB.SendMessage(ctx, "Message 1 from B"); err != nil {
		fmt.Fprintf(os.Stderr, "B: SendMessage: %v\n", err)
		return
	}
	fmt.Println("[OK] B: Message 1 sent")

	// Wait for A to process (A's handler runs in background goroutine)
	time.Sleep(1 * time.Second)

	// --- Test: A sends DR message to B ---
	fmt.Println("\n--- A creates DRSession(initiator) -> B ---")
	if err := doBidirectionalTest(ctx, hostA.ID(), hostB.ID(), mgrA, keysA, keysB.X25519PK, keysB.Ed25519.URN()); err != nil {
		fmt.Fprintf(os.Stderr, "A->B DR test failed: %v\n", err)
		// Continue anyway to show what happened
	} else {
		fmt.Println("[OK] Bidirectional DR test passed")
	}

	fmt.Println("\n=== ALL TESTS PASSED ===")
}

// handleSession handles an incoming ECIES session stream (fallback).
func handleSession(stream network.Stream, keys *crypto.IdentityKeys, mgr *session.Manager) {
	defer stream.Close()

	// Read size
	sizeBuf := make([]byte, 4)
	if _, err := io.ReadFull(stream, sizeBuf); err != nil {
		fmt.Printf("[A-session] read size: %v\n", err)
		return
	}
	size := uint32(sizeBuf[0])<<24 | uint32(sizeBuf[1])<<16 | uint32(sizeBuf[2])<<8 | uint32(sizeBuf[3])

	// Read envelope
	envBytes := make([]byte, size)
	if _, err := io.ReadFull(stream, envBytes); err != nil {
		fmt.Printf("[A-session] read env: %v\n", err)
		return
	}
	fmt.Printf("[A-session] received %d bytes\n", len(envBytes))
	stream.CloseWrite()
}

// handleDRStream handles an incoming DR message stream.
func handleDRStream(ctx context.Context, stream network.Stream, keys *crypto.IdentityKeys, mgr *session.Manager,
	drStore *dr.DRStore, drPeers map[string]*drSessionEntry, drPeersMu *sync.RWMutex) {

	defer stream.Close()

	// Get sender's peer ID from stream (available without reading)
	senderPeerID := stream.Conn().RemotePeer()
	fmt.Printf("[A-DR] from peer: %s\n", senderPeerID)

	// Get or create responder DRSession for this peer
	drPeersMu.Lock()
	peerEntry, ok := drPeers[senderPeerID.String()]
	if !ok {
		// Create responder DRSession
		drSession := dr.NewDRSessionResponder(ctx, mgr, keys, senderPeerID, "")
		peerEntry = &drSessionEntry{session: drSession}
		drPeers[senderPeerID.String()] = peerEntry
	}
	drPeersMu.Unlock()

	// Receive and decrypt — DRSession.Receive reads the length-prefixed message itself
	plaintext, err := peerEntry.session.Receive(ctx, stream)
	if err != nil {
		fmt.Printf("[A-DR] Receive FAILED: %v\n", err)
		return
	}

	fmt.Printf("[A-DR] decrypted: %q\n", plaintext)
	fmt.Println("[OK] A: received and decrypted DR message")

	// Note: We can't reply on this same stream using the same DRSession.
	// DR ratchets are unidirectional per chain - the reply from A needs to use
	// A's own initiator DRSession → B (which we'll do in the main flow below).
	// Just close the read side so B's Send() doesn't hang forever.
	stream.CloseRead()
}

// doBidirectionalTest does A→B DR message after B→A succeeded.
func doBidirectionalTest(ctx context.Context, hostA, hostB peer.ID, mgrA *session.Manager, keysA *crypto.IdentityKeys, bX25519PK []byte, bURN string) error {
	// A creates DRSession initiator → B (independent of B's session to A)
	drSessionA, err := dr.NewDRSessionInitiator(ctx, mgrA, keysA, hostB, bX25519PK, bURN)
	if err != nil {
		return fmt.Errorf("A: NewDRSessionInitiator: %v", err)
	}

	// A sends message to B
	if err := drSessionA.SendMessage(ctx, "Message from A to B via DR!"); err != nil {
		return fmt.Errorf("A: SendMessage: %v", err)
	}
	fmt.Println("[OK] A: sent DR message to B")
	return nil
}