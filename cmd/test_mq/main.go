// Test: offline message delivery via MQ relay.
// Three nodes: Relay (bootstrap), Sender (B), Receiver (A).
// B sends message to A while A is offline, then A comes online and retrieves it.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/nousresearch/hermes-agent/agent-comm/crypto"
	"github.com/nousresearch/hermes-agent/agent-comm/dht"
	"github.com/nousresearch/hermes-agent/agent-comm/libp2p"
	"github.com/nousresearch/hermes-agent/agent-comm/mq"
	"github.com/nousresearch/hermes-agent/agent-comm/proto"
	"github.com/nousresearch/hermes-agent/agent-comm/registry"
	"github.com/nousresearch/hermes-agent/agent-comm/session"
	goproto "google.golang.org/protobuf/proto"
)

func main() {
	fmt.Println("=== MQ Offline Message Test ===\n")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Clean up old keys
	os.RemoveAll("/tmp/mq_relay_keys")
	os.RemoveAll("/tmp/mq_sender_keys")
	os.RemoveAll("/tmp/mq_receiver_keys")
	os.RemoveAll("/tmp/mq_relay_db")

	// --- Relay Node (Bootstrap) ---
	fmt.Println("--- Setting up Relay (Bootstrap) ---")
	relayKeys, _ := crypto.LoadOrCreateIdentity("/tmp/mq_relay_keys")
	relayURN := relayKeys.Ed25519.URN()
	relayCfg := libp2p.Config{
		ListenAddrs:  []string{"/ip4/0.0.0.0/tcp/45200", "/ip4/0.0.0.0/udp/0/quic"},
		EnableRelay:  true,
		PrivKeyBytes: relayKeys.Ed25519.PrivateKey,
	}
	relayHost, _ := libp2p.NewHost(relayCfg)
	defer relayHost.Close()

	relayDHT, _ := dht.NewDHT(ctx, relayHost, dht.DHTConfig{Mode: dht.ModeServer})
	defer relayDHT.Close()
	dht.Bootstrap(ctx, relayDHT)

	regServer := registry.NewServer(relayHost)
	regServer.Register()
	regServer.HandleRegister(relayURN, relayHost.ID(), relayHost.Addrs(), relayKeys.X25519PK)

	mqServer, _ := mq.NewServer(relayHost, "/tmp/mq_relay_db")
	defer mqServer.Close()

	fmt.Printf("Relay: URN=%s PeerID=%s\n", relayURN, relayHost.ID())
	fmt.Printf("Relay listening on: %v\n\n", relayHost.Addrs())

	// --- Sender Node (B) ---
	fmt.Println("--- Setting up Sender (B) ---")
	senderKeys, _ := crypto.LoadOrCreateIdentity("/tmp/mq_sender_keys")
	senderURN := senderKeys.Ed25519.URN()
	senderCfg := libp2p.Config{
		ListenAddrs:  []string{"/ip4/0.0.0.0/tcp/0", "/ip4/0.0.0.0/udp/0/quic"},
		EnableRelay:  true,
		PrivKeyBytes: senderKeys.Ed25519.PrivateKey,
	}
	senderHost, _ := libp2p.NewHost(senderCfg)
	defer senderHost.Close()

	senderHost.Peerstore().AddAddrs(relayHost.ID(), relayHost.Addrs(), peerstore.TempAddrTTL)
	senderHost.Connect(ctx, peer.AddrInfo{ID: relayHost.ID(), Addrs: relayHost.Addrs()})

	senderDHT, _ := dht.NewDHT(ctx, senderHost, dht.DHTConfig{Mode: dht.ModeClient})
	_ = senderDHT
	senderDHT2, _ := dht.NewDHT(ctx, senderHost, dht.DHTConfig{Mode: dht.ModeClient, Bootstraps: []peer.AddrInfo{{ID: relayHost.ID(), Addrs: relayHost.Addrs()}}})
	dht.Bootstrap(ctx, senderDHT2)

	regClient := registry.NewClient(senderHost)
	regClient.Register(peer.AddrInfo{ID: relayHost.ID(), Addrs: relayHost.Addrs()}, senderURN, senderHost.Addrs(), senderKeys.X25519PK)

	senderMgr := session.NewManager(senderHost, senderKeys)
	senderMQ := mq.NewClient(senderHost)
	fmt.Printf("Sender: URN=%s\n\n", senderURN)

	// --- Receiver Node (A) — NOT STARTED YET ---
	fmt.Println("--- Setting up Receiver (A) keys only (offline) ---")
	receiverKeys, _ := crypto.LoadOrCreateIdentity("/tmp/mq_receiver_keys")
	receiverURN := receiverKeys.Ed25519.URN()
	fmt.Printf("Receiver: URN=%s (offline)\n\n", receiverURN)

	// Register A's URN with relay registry (so B can resolve A)
	// In real system A would do this when online
	regClient.Register(peer.AddrInfo{ID: relayHost.ID(), Addrs: relayHost.Addrs()}, receiverURN, nil, receiverKeys.X25519PK)

	// --- B sends message to A (A is offline) ---
	fmt.Println("--- B sends message to A (A is offline) ---")

	// B resolves A's pubkey from registry
	resolved, err := registry.NewClient(senderHost).Resolve(peer.AddrInfo{ID: relayHost.ID(), Addrs: relayHost.Addrs()}, receiverURN)
	if err != nil {
		fmt.Printf("FAIL: B cannot resolve A: %v\n", err)
		return
	}

	// B builds encrypted envelope for A
	envelope, err := senderMgr.BuildEnvelope(resolved.X25519PubKey, "Hello A! This is an offline message.")
	if err != nil {
		fmt.Printf("FAIL: build envelope: %v\n", err)
		return
	}
	envelope.SenderUrn = senderURN
	envelope.SenderStaticPubkey = senderKeys.X25519PK

	// B stores via relay (MQ) — A is offline
	msgID, err := senderMQ.Store(ctx, peer.AddrInfo{ID: relayHost.ID(), Addrs: relayHost.Addrs()}, receiverURN, envelope, 7)
	if err != nil {
		fmt.Printf("FAIL: MQ store failed: %v\n", err)
		return
	}
	fmt.Printf("B stored message for A via relay: msg_id=%s\n\n", msgID)

	// Verify message is in relay DB
	pending, err := senderMQ.Retrieve(ctx, peer.AddrInfo{ID: relayHost.ID(), Addrs: relayHost.Addrs()}, receiverURN)
	if err != nil {
		fmt.Printf("FAIL: verify retrieve: %v\n", err)
		return
	}
	fmt.Printf("Relay has %d message(s) for A\n\n", len(pending))

	// --- Now A comes online ---
	fmt.Println("--- A comes online and pulls messages ---")

	// A's host
	recvCfg := libp2p.Config{
		ListenAddrs:  []string{"/ip4/0.0.0.0/tcp/0", "/ip4/0.0.0.0/udp/0/quic"},
		EnableRelay:  true,
		PrivKeyBytes: receiverKeys.Ed25519.PrivateKey,
	}
	recvHost, _ := libp2p.NewHost(recvCfg)
	defer recvHost.Close()

	recvHost.Peerstore().AddAddrs(relayHost.ID(), relayHost.Addrs(), peerstore.TempAddrTTL)
	recvHost.Connect(ctx, peer.AddrInfo{ID: relayHost.ID(), Addrs: relayHost.Addrs()})

	// Register A
	registry.NewClient(recvHost).Register(peer.AddrInfo{ID: relayHost.ID(), Addrs: relayHost.Addrs()}, receiverURN, recvHost.Addrs(), receiverKeys.X25519PK)

	recvMgr := session.NewManager(recvHost, receiverKeys)
	recvMQ := mq.NewClient(recvHost)

	// Handle incoming sessions
	recvHost.SetStreamHandler(session.ProtoID, func(stream network.Stream) {
		defer stream.Close()
		sizeBuf := make([]byte, 4)
		if _, err := stream.Read(sizeBuf); err != nil {
			return
		}
		size := uint32(sizeBuf[0])<<24 | uint32(sizeBuf[1])<<16 | uint32(sizeBuf[2])<<8 | uint32(sizeBuf[3])
		envBytes := make([]byte, size)
		if _, err := stream.Read(envBytes); err != nil {
			return
		}
		var env proto.EncryptedEnvelope
		if err := goproto.Unmarshal(envBytes, &env); err != nil {
			return
		}
		pt, err := recvMgr.DecryptEnvelope(&env)
		if err != nil {
			fmt.Printf("[A] session from %s: decrypt FAILED: %v\n", env.SenderUrn, err)
			return
		}
		var msg proto.ChatMessage
		if err := goproto.Unmarshal(pt, &msg); err != nil {
			return
		}
		if txt := msg.GetText(); txt != nil {
			fmt.Printf("[A] session from %s: %q\n", env.SenderUrn, txt.Text)
		}
	})

	// Pull offline messages from relay
	envelopes, err := recvMQ.Retrieve(ctx, peer.AddrInfo{ID: relayHost.ID(), Addrs: relayHost.Addrs()}, receiverURN)
	if err != nil {
		fmt.Printf("FAIL: A retrieve failed: %v\n", err)
		return
	}
	if len(envelopes) == 0 {
		fmt.Println("FAIL: A retrieved 0 messages (expected 1)")
		return
	}

	fmt.Printf("A retrieved %d message(s):\n", len(envelopes))
	receivedText := ""
	for _, env := range envelopes {
		pt, err := recvMgr.DecryptEnvelope(env)
		if err != nil {
			fmt.Printf("  decrypt FAILED: %v\n", err)
			continue
		}
		var msg proto.ChatMessage
		if err := goproto.Unmarshal(pt, &msg); err != nil {
			fmt.Printf("  parse FAILED: %v\n", err)
			continue
		}
		if txt := msg.GetText(); txt != nil {
			fmt.Printf("  from %s: %q (ts=%d)\n", env.SenderUrn, txt.Text, txt.Timestamp)
			receivedText = txt.Text
		}
	}

	// Ack
	ids := make([]string, len(envelopes))
	for i, e := range envelopes {
		ids[i] = e.MessageId
	}
	deleted, _ := recvMQ.Ack(ctx, peer.AddrInfo{ID: relayHost.ID(), Addrs: relayHost.Addrs()}, ids)
	fmt.Printf("A acked %d message(s) from relay\n\n", deleted)

	// Verify
	if receivedText == "Hello A! This is an offline message." {
		fmt.Println("=== TEST PASSED ===")
	} else {
		fmt.Printf("=== TEST FAILED: expected 'Hello A! This is an offline message.', got %q ===\n", receivedText)
	}
}