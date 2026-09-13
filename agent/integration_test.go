package agent

import (
	"context"
	"crypto/ed25519"
	"path/filepath"
	"testing"
	"time"

	"github.com/BillShiyaoZhang/agent-comm/contacts"
	"github.com/BillShiyaoZhang/agent-comm/crypto"
	p2phost "github.com/BillShiyaoZhang/agent-comm/libp2p"
	"github.com/BillShiyaoZhang/agent-comm/mq"
	pb "github.com/BillShiyaoZhang/agent-comm/proto"
	"github.com/BillShiyaoZhang/agent-comm/registry"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	goproto "google.golang.org/protobuf/proto"
)

func TestAgentHybridIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// 1. Create a Platform/Bootstrap node running Registry and MQ servers
	platformKeysDir := filepath.Join(t.TempDir(), "platform_keys")
	platformKeys, err := crypto.LoadOrCreateIdentity(platformKeysDir)
	if err != nil {
		t.Fatalf("failed to create platform keys: %v", err)
	}

	platformHost, err := p2phost.NewHost(p2phost.Config{
		ListenAddrs:  []string{"/ip4/127.0.0.1/tcp/0"},
		PrivKeyBytes: platformKeys.Ed25519.PrivateKey,
	})
	if err != nil {
		t.Fatalf("failed to start platform host: %v", err)
	}
	defer platformHost.Close()

	// Start Registry server
	platformStore := registry.NewInMemoryStore()
	regServer := registry.NewServer(platformHost, platformStore)
	regServer.Register()
	platformURN := platformKeys.Ed25519.URN()
	platformTimestamp := time.Now().Unix()
	platformSignature := ed25519.Sign(platformKeys.Ed25519.PrivateKey, registry.BuildSignedMsg(
		platformURN, platformHost.ID().String(), platformKeys.X25519PK, false, platformTimestamp))
	platformAddrs := make([]string, len(platformHost.Addrs()))
	for i, addr := range platformHost.Addrs() {
		platformAddrs[i] = addr.String()
	}
	if err := platformStore.RegisterWithSignature(platformURN, platformHost.ID().String(), platformAddrs, nil,
		platformKeys.X25519PK, platformKeys.Ed25519.PublicKey, platformSignature, false, platformTimestamp); err != nil {
		t.Fatalf("register platform identity: %v", err)
	}

	// Start MQ server
	platformMQDb := filepath.Join(t.TempDir(), "platform_mq.db")
	sqliteStore, err := mq.NewSQLiteStore(platformMQDb)
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	mqServer, err := mq.NewServer(platformHost, sqliteStore)
	if err != nil {
		t.Fatalf("failed to start MQ server: %v", err)
	}
	defer mqServer.Close()

	bootstrapInfo := peer.AddrInfo{
		ID:    platformHost.ID(),
		Addrs: platformHost.Addrs(),
	}

	// 2. Setup Agent A (Sender)
	keysDirA := filepath.Join(t.TempDir(), "keys_a")
	dbPathA := filepath.Join(t.TempDir(), "dr_a.db")
	agentA, err := InitIdentity(ctx, Config{
		KeysDir:        keysDirA,
		DBPath:         dbPathA,
		ListenAddrs:    []string{"/ip4/127.0.0.1/tcp/0"},
		BootstrapNodes: []peer.AddrInfo{bootstrapInfo},
	})
	if err != nil {
		t.Fatalf("failed to init agent A: %v", err)
	}
	defer agentA.Close()

	// 3. Setup Agent B (Receiver)
	keysDirB := filepath.Join(t.TempDir(), "keys_b")
	dbPathB := filepath.Join(t.TempDir(), "dr_b.db")
	agentB, err := InitIdentity(ctx, Config{
		KeysDir:        keysDirB,
		DBPath:         dbPathB,
		ListenAddrs:    []string{"/ip4/127.0.0.1/tcp/0"},
		BootstrapNodes: []peer.AddrInfo{bootstrapInfo},
	})
	if err != nil {
		t.Fatalf("failed to init agent B: %v", err)
	}
	defer agentB.Close()

	// Let's manually register each other in contacts databases
	peerIDA, _ := agentA.Keys.PeerID()
	err = agentB.Contacts.Add(&contacts.Contact{
		URN:         agentA.Keys.Ed25519.URN(),
		PeerID:      peerIDA,
		X25519PK:    agentA.Keys.X25519PK,
		Ed25519PK:   agentA.Keys.Ed25519.PublicKey,
		DisplayName: "Agent A",
		Trusted:     true,
	})
	if err != nil {
		t.Fatalf("failed to add contact A to B: %v", err)
	}

	peerIDB, _ := agentB.Keys.PeerID()
	err = agentA.Contacts.Add(&contacts.Contact{
		URN:         agentB.Keys.Ed25519.URN(),
		PeerID:      peerIDB,
		X25519PK:    agentB.Keys.X25519PK,
		Ed25519PK:   agentB.Keys.Ed25519.PublicKey,
		DisplayName: "Agent B",
		Trusted:     true,
	})
	if err != nil {
		t.Fatalf("failed to add contact B to A: %v", err)
	}

	// Connect agents to the platform host (simulate boot network discovery)
	agentA.Host.Peerstore().AddAddrs(bootstrapInfo.ID, bootstrapInfo.Addrs, peerstore.PermanentAddrTTL)
	if err := agentA.Host.Connect(ctx, bootstrapInfo); err != nil {
		t.Fatalf("agent A failed to connect to bootstrap: %v", err)
	}

	agentB.Host.Peerstore().AddAddrs(bootstrapInfo.ID, bootstrapInfo.Addrs, peerstore.PermanentAddrTTL)
	if err := agentB.Host.Connect(ctx, bootstrapInfo); err != nil {
		t.Fatalf("agent B failed to connect to bootstrap: %v", err)
	}

	// Wait a moment for background registrations to finish
	time.Sleep(1 * time.Second)

	urnA := agentA.Keys.Ed25519.URN()
	urnB := agentB.Keys.Ed25519.URN()

	// 4. Test Case 1: Real-time Message Delivery (Direct P2P Stream)
	receivedMsgs := make(chan string, 10)
	agentB.OnMessage(ctx, func(senderURN string, msg string) {
		if senderURN == urnA {
			receivedMsgs <- msg
		}
	})

	// Wait a moment for B to set up listening
	time.Sleep(100 * time.Millisecond)

	// Since agentA needs B's addresses in its peerstore for direct connection,
	// resolve B from registry.
	res, err := agentA.Registry.Resolve(bootstrapInfo, urnB)
	if err != nil {
		t.Fatalf("agent A failed to resolve agent B: %v", err)
	}
	agentA.Host.Peerstore().AddAddrs(res.ID, res.Addrs, peerstore.TempAddrTTL)

	// Send message directly
	testMsg1 := "Hello Agent B! This is a real-time direct P2P message."
	err = agentA.SendMessage(ctx, urnB, testMsg1)
	if err != nil {
		t.Fatalf("SendMessage direct failed: %v", err)
	}

	select {
	case msg := <-receivedMsgs:
		var chatMsg pb.ChatMessage
		err := goproto.Unmarshal([]byte(msg), &chatMsg)
		if err != nil {
			t.Fatalf("failed to unmarshal received message: %v", err)
		}
		text := chatMsg.GetText().Text
		if text != testMsg1 {
			t.Errorf("expected msg %q, got %q", testMsg1, text)
		} else {
			t.Logf("Successfully received direct P2P message: %q", text)
		}
	case <-time.After(5 * time.Second):
		t.Error("timeout waiting for direct P2P message")
	}

	// 5. Test Case 2: Offline Fallback Message Delivery (Platform MQ)
	// We simulate agent B going offline by closing its host.
	agentB.Close()
	agentB.Host.Close()

	// Sleep to ensure connections are closed
	time.Sleep(500 * time.Millisecond)

	// A sends message to B. Since B is offline, direct delivery fails, and it falls back to MQ.
	testMsg2 := "Hello Agent B! You were offline, so this went to the MQ."
	err = agentA.SendMessage(ctx, urnB, testMsg2)
	if err != nil {
		t.Fatalf("SendMessage fallback failed: %v", err)
	}

	// Let's verify that the message is stored on the MQ server.
	// We can spin up a new agent B2 using the same keys and db to simulate B coming back online.
	agentB2, err := InitIdentity(ctx, Config{
		KeysDir:        keysDirB,
		DBPath:         dbPathB,
		ListenAddrs:    []string{"/ip4/127.0.0.1/tcp/0"},
		BootstrapNodes: []peer.AddrInfo{bootstrapInfo},
	})
	if err != nil {
		t.Fatalf("failed to init agent B2: %v", err)
	}
	defer agentB2.Close()

	agentB2.Host.Peerstore().AddAddrs(bootstrapInfo.ID, bootstrapInfo.Addrs, peerstore.PermanentAddrTTL)
	if err := agentB2.Host.Connect(ctx, bootstrapInfo); err != nil {
		t.Fatalf("agent B2 failed to connect to bootstrap: %v", err)
	}

	// Manually retrieve from MQ using agentB2's client (to avoid waiting for the 30s poll ticker)
	envs, err := agentB2.MQClient.Retrieve(ctx, bootstrapInfo, urnB)
	if err != nil {
		t.Fatalf("failed to retrieve messages from MQ: %v", err)
	}

	if len(envs) != 1 {
		t.Fatalf("expected 1 message in MQ, got %d", len(envs))
	}

	// Decrypt the envelope
	plaintext, err := agentB2.Session.DecryptEnvelope(envs[0])
	if err != nil {
		t.Fatalf("failed to decrypt envelope: %v", err)
	}

	var chatMsg pb.ChatMessage
	err = goproto.Unmarshal(plaintext, &chatMsg)
	if err != nil {
		t.Fatalf("failed to unmarshal MQ message: %v", err)
	}

	text := chatMsg.GetText().Text
	if text != testMsg2 {
		t.Errorf("expected decrypted MQ message %q, got %q", testMsg2, text)
	} else {
		t.Logf("Successfully retrieved and decrypted offline MQ message: %q", text)
	}

	// Ack the message to delete it
	deleted, err := agentB2.MQClient.Ack(ctx, bootstrapInfo, []string{envs[0].MessageId})
	if err != nil {
		t.Fatalf("failed to ack message: %v", err)
	}
	if deleted != 1 {
		t.Errorf("expected 1 message deleted, got %d", deleted)
	}
}

// tamperingRegistryStore simulates a compromised registry's response without
// allowing invalid registrations into the production store.
type tamperingRegistryStore struct {
	registry.Store
	poisonedURN string
	x25519PK    []byte
}

func (s *tamperingRegistryStore) ResolveExtended(urn string) (string, []string, []string, []byte, []byte, []byte, bool, int64, bool) {
	pid, addrs, relays, x25519PK, ed25519PK, signature, storesData, timestamp, found := s.Store.ResolveExtended(urn)
	if found && urn == s.poisonedURN {
		x25519PK = s.x25519PK
	}
	return pid, addrs, relays, x25519PK, ed25519PK, signature, storesData, timestamp, found
}

func TestRegistryVerificationFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 1. Setup Platform/Bootstrap node
	platformKeysDir := filepath.Join(t.TempDir(), "platform_keys")
	platformKeys, err := crypto.LoadOrCreateIdentity(platformKeysDir)
	if err != nil {
		t.Fatalf("failed to create platform keys: %v", err)
	}
	platformHost, err := p2phost.NewHost(p2phost.Config{
		ListenAddrs:  []string{"/ip4/127.0.0.1/tcp/0"},
		PrivKeyBytes: platformKeys.Ed25519.PrivateKey,
	})
	if err != nil {
		t.Fatalf("failed to start platform host: %v", err)
	}
	defer platformHost.Close()

	// Start a test-only malicious registry that tampers with one resolution.
	bob, err := crypto.LoadOrCreateIdentity(filepath.Join(t.TempDir(), "bob_keys"))
	if err != nil {
		t.Fatal(err)
	}
	_, tamperedX25519PK, err := crypto.GenerateX25519KeyPair()
	if err != nil {
		t.Fatal(err)
	}
	store := &tamperingRegistryStore{
		Store:       registry.NewInMemoryStore(),
		poisonedURN: bob.Ed25519.URN(),
		x25519PK:    tamperedX25519PK,
	}
	regServer := registry.NewServer(platformHost, store)
	regServer.Register()

	bootstrapInfo := peer.AddrInfo{
		ID:    platformHost.ID(),
		Addrs: platformHost.Addrs(),
	}

	// 2. Setup Agent A (Sender)
	keysDirA := filepath.Join(t.TempDir(), "keys_a")
	dbPathA := filepath.Join(t.TempDir(), "dr_a.db")
	agentA, err := InitIdentity(ctx, Config{
		KeysDir:        keysDirA,
		DBPath:         dbPathA,
		ListenAddrs:    []string{"/ip4/127.0.0.1/tcp/0"},
		BootstrapNodes: []peer.AddrInfo{bootstrapInfo},
	})
	if err != nil {
		t.Fatalf("failed to init agent A: %v", err)
	}
	defer agentA.Close()

	// Connect Agent A to bootstrap
	agentA.Host.Peerstore().AddAddrs(bootstrapInfo.ID, bootstrapInfo.Addrs, peerstore.PermanentAddrTTL)
	if err := agentA.Host.Connect(ctx, bootstrapInfo); err != nil {
		t.Fatalf("agent A failed to connect to bootstrap: %v", err)
	}

	// 3. Store Bob's valid ownership proof; the fixture changes only its response.
	urnB := bob.Ed25519.URN()
	peerIDB, err := bob.PeerID()
	if err != nil {
		t.Fatal(err)
	}
	timestamp := time.Now().Unix()
	msg := registry.BuildSignedMsg(urnB, peerIDB, bob.X25519PK, false, timestamp)
	sig, err := bob.Ed25519.Sign(msg)
	if err != nil {
		t.Fatalf("sign registration message: %v", err)
	}

	err = store.RegisterWithSignature(urnB, peerIDB, []string{"/ip4/127.0.0.1/tcp/4567"}, nil, bob.X25519PK, bob.Ed25519.PublicKey, sig, false, timestamp)
	if err != nil {
		t.Fatalf("store valid registration: %v", err)
	}

	// 5. Try resolving URN B from Agent A's registry client
	res, err := agentA.Registry.Resolve(bootstrapInfo, urnB)
	if err != nil {
		t.Fatalf("agent A failed to resolve agent B: %v", err)
	}

	// 6. VerifyResolveResult should fail on the tampered key
	err = registry.VerifyResolveResult(urnB, &res)
	if err == nil {
		t.Error("expected VerifyResolveResult to FAIL on tampered public key, but it succeeded!")
	} else {
		t.Logf("VerifyResolveResult failed as expected: %v", err)
	}
}
