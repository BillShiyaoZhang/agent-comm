package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/BillShiyaoZhang/agent-comm/contacts"
	pb "github.com/BillShiyaoZhang/agent-comm/proto"
	goproto "google.golang.org/protobuf/proto"
)

const (
	RealPlatformDomain = "agent-communication.online"
	RealPlatformPeerID = "12D3KooWRsYuopRwdiyNLhiTrxY1innpSRCCkAygdoMqeVyn2x8f"
	RemoteAgentURN     = "urn:hermes:agent:VBYEE9xFV6AiTZQcsvbBEz"
)

func TestRealPlatformLifecycle(t *testing.T) {
	if os.Getenv("TEST_REAL_PLATFORM") != "true" {
		t.Skip("Skipping test against the real platform. Set TEST_REAL_PLATFORM=true to run.")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// 1. Set up platform address info
	quicMaddrStr := "/dns4/" + RealPlatformDomain + "/udp/45041/quic-v1/p2p/" + RealPlatformPeerID
	platformAddrInfo, err := peer.AddrInfoFromString(quicMaddrStr)
	if err != nil {
		t.Fatalf("failed to parse platform multiaddr: %v", err)
	}

	// 2. Initialize Agent A
	keysDirA := filepath.Join(t.TempDir(), "keys_a")
	dbPathA := filepath.Join(t.TempDir(), "dr_a.db")
	agentA, err := InitIdentity(ctx, Config{
		KeysDir:        keysDirA,
		DBPath:         dbPathA,
		BootstrapNodes: []peer.AddrInfo{*platformAddrInfo},
	})
	if err != nil {
		t.Fatalf("failed to init agent A: %v", err)
	}
	defer agentA.Close()

	// Connect to platform
	agentA.Host.Peerstore().AddAddrs(platformAddrInfo.ID, platformAddrInfo.Addrs, peerstore.PermanentAddrTTL)
	if err := agentA.Host.Connect(ctx, *platformAddrInfo); err != nil {
		t.Fatalf("agent A failed to connect to real platform: %v", err)
	}

	// Wait for background registration to complete
	time.Sleep(1 * time.Second)

	urnA := agentA.Keys.Ed25519.URN()
	t.Logf("Agent A registered URN: %s", urnA)

	// 3. Resolve Agent A's URN from the real platform
	res, err := agentA.Registry.Resolve(*platformAddrInfo, urnA)
	if err != nil {
		t.Fatalf("failed to resolve self URN from registry: %v", err)
	}
	if res.ID != agentA.Host.ID() {
		t.Errorf("resolved peer ID %s does not match host ID %s", res.ID, agentA.Host.ID())
	}
	t.Logf("Successfully resolved self URN from real platform!")

	// 4. Test MQ: Send message to self via MQ blind-store
	testMsg := "Hello from real platform test!"
	env, err := agentA.Session.BuildEnvelope(agentA.Keys.X25519PK, testMsg)
	if err != nil {
		t.Fatalf("failed to build envelope: %v", err)
	}

	msgID, err := agentA.MQClient.Store(ctx, *platformAddrInfo, urnA, env, 7)
	if err != nil {
		t.Fatalf("failed to store message on real platform MQ: %v", err)
	}
	t.Logf("Successfully stored message on MQ. MessageID: %s", msgID)

	// 5. Test MQ: Retrieve message from MQ
	envs, err := agentA.MQClient.Retrieve(ctx, *platformAddrInfo, urnA)
	if err != nil {
		t.Fatalf("failed to retrieve message from real platform MQ: %v", err)
	}
	if len(envs) == 0 {
		t.Fatalf("expected to retrieve at least 1 message, got 0")
	}

	var foundEnv *pb.EncryptedEnvelope
	for _, e := range envs {
		if e.MessageId == msgID {
			foundEnv = e
			break
		}
	}
	if foundEnv == nil {
		t.Fatalf("stored message ID %s not found in retrieved list", msgID)
	}

	// Decrypt retrieved message
	plaintext, err := agentA.Session.DecryptEnvelope(foundEnv)
	if err != nil {
		t.Fatalf("failed to decrypt envelope: %v", err)
	}

	var chatMsg pb.ChatMessage
	if err := goproto.Unmarshal(plaintext, &chatMsg); err != nil {
		t.Fatalf("failed to unmarshal chat message: %v", err)
	}

	if chatMsg.GetText().Text != testMsg {
		t.Errorf("expected text %q, got %q", testMsg, chatMsg.GetText().Text)
	}
	t.Logf("Successfully retrieved and decrypted message from real MQ: %s", chatMsg.GetText().Text)

	// 6. Test MQ: Ack message
	deleted, err := agentA.MQClient.Ack(ctx, *platformAddrInfo, []string{msgID})
	if err != nil {
		t.Fatalf("failed to ack message: %v", err)
	}
	if deleted != 1 {
		t.Errorf("expected 1 deleted message, got %d", deleted)
	}
	t.Logf("Successfully acked message on real MQ.")

	// 7. Resolve Remote Agent's URN
	t.Logf("Resolving remote agent URN: %s", RemoteAgentURN)
	resRemote, err := agentA.Registry.Resolve(*platformAddrInfo, RemoteAgentURN)
	if err != nil {
		t.Fatalf("failed to resolve remote agent URN %s from real platform: %v", RemoteAgentURN, err)
	}
	t.Logf("Successfully resolved remote agent info from real registry!")
	t.Logf("  Remote PeerID: %s", resRemote.ID)
	t.Logf("  Remote Addrs: %v", resRemote.Addrs)

	// Extract Ed25519 PK from remote PeerID
	pubKey, err := resRemote.ID.ExtractPublicKey()
	if err != nil {
		t.Fatalf("failed to extract public key from remote PeerID: %v", err)
	}
	edPubKey, err := pubKey.Raw()
	if err != nil {
		t.Fatalf("failed to get raw remote Ed25519 public key bytes: %v", err)
	}

	// Add remote agent as contact and attempt SendMessage (will test fallback logic)
	err = agentA.Contacts.Add(&contacts.Contact{
		URN:         RemoteAgentURN,
		PeerID:      resRemote.ID.String(),
		X25519PK:    resRemote.X25519PubKey,
		Ed25519PK:   edPubKey,
		DisplayName: "Remote Hermes Agent",
		Trusted:     true,
	})
	if err != nil {
		t.Fatalf("failed to add remote contact: %v", err)
	}

	t.Logf("Sending message to remote agent URN...")
	err = agentA.SendMessage(ctx, RemoteAgentURN, "Hello from automated integration test!")
	if err != nil {
		t.Fatalf("SendMessage to remote agent failed: %v", err)
	}
	t.Logf("Successfully sent message (direct/relay or blind-stored on MQ)!")
}
