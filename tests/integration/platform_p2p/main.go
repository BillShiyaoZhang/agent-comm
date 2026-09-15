package main

import (
	"context"
	"crypto/ed25519"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/BillShiyaoZhang/agent-comm/crypto"
	"github.com/BillShiyaoZhang/agent-comm/libp2p"
	"github.com/BillShiyaoZhang/agent-comm/mq"
	"github.com/BillShiyaoZhang/agent-comm/proto"
	"github.com/BillShiyaoZhang/agent-comm/registry"
	"github.com/BillShiyaoZhang/agent-comm/session"
	goproto "google.golang.org/protobuf/proto"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
)

func main() {
	target := flag.String("target", "", "Required test Platform multiaddr including /p2p/<PeerID>")
	flag.Parse()
	if *target == "" {
		fmt.Fprintln(os.Stderr, "Pass -target with the multiaddr of an isolated test Platform")
		os.Exit(2)
	}
	platformAddrInfo, err := peer.AddrInfoFromString(*target)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Invalid Platform multiaddr: %v\n", err)
		os.Exit(2)
	}

	fmt.Printf("==================================================\n")
	fmt.Printf("=== Deployed Platform Integration Test Client ===\n")
	fmt.Printf("==================================================\n\n")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// 1. Prepare local identity
	keysDir, err := os.MkdirTemp("", "agent-comm-platform-p2p-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Create temporary identity directory: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(keysDir)
	keys, err := crypto.LoadOrCreateIdentity(keysDir)
	if err != nil {
		fmt.Printf("❌ Failed to create local identity: %v\n", err)
		os.Exit(1)
	}
	myURN := keys.Ed25519.URN()
	fmt.Printf("Generated client identity URN: %s\n", myURN)

	// 2. Create local libp2p host
	cfg := libp2p.Config{
		ListenAddrs:  []string{"/ip4/0.0.0.0/tcp/0", "/ip4/0.0.0.0/udp/0/quic"},
		EnableRelay:  true,
		PrivKeyBytes: keys.Ed25519.PrivateKey,
	}
	h, err := libp2p.NewHost(cfg)
	if err != nil {
		fmt.Printf("❌ Failed to start local P2P host: %v\n", err)
		os.Exit(1)
	}
	defer h.Close()
	fmt.Printf("Local P2P host created. PeerID: %s\n\n", h.ID())

	// 3. Connect only to the explicitly supplied test Platform.
	h.Peerstore().AddAddrs(platformAddrInfo.ID, platformAddrInfo.Addrs, peerstore.TempAddrTTL)
	if err := h.Connect(ctx, *platformAddrInfo); err != nil {
		fmt.Fprintf(os.Stderr, "Connect to test Platform: %v\n", err)
		os.Exit(1)
	}

	// 4. Registry Integration
	regClient := registry.NewClient(h)

	fmt.Printf("--- Step 1: Registering URN on Platform Registry ---\n")
	timestamp := time.Now().Unix()
	signature := ed25519.Sign(keys.Ed25519.PrivateKey, registry.BuildSignedMsg(myURN, h.ID().String(), keys.X25519PK, false, timestamp))
	err = regClient.RegisterWithSignature(*platformAddrInfo, myURN, h.Addrs(), nil,
		keys.X25519PK, keys.Ed25519.PublicKey, signature, false, timestamp)
	if err != nil {
		fmt.Printf("❌ Registration failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("✅ Registered URN successfully!\n\n")

	fmt.Printf("--- Step 2: Resolving URN from Platform Registry ---\n")
	res, err := regClient.Resolve(*platformAddrInfo, myURN)
	if err != nil {
		fmt.Printf("❌ Resolution failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("✅ Resolved URN successfully!\n")
	fmt.Printf("  Resolved PeerID: %s\n", res.ID)
	fmt.Printf("  Resolved Addrs: %v\n", res.Addrs)
	fmt.Printf("  Resolved X25519 Pubkey length: %d bytes\n\n", len(res.X25519PubKey))

	// 5. MQ Integration
	sessionMgr := session.NewManager(h, keys)
	mqClient := mq.NewClient(h)

	fmt.Printf("--- Step 3: Building and Storing Message on MQ ---\n")
	plaintextMsg := "Hello hermes platform! This is a test message stored on MQ relay."

	// Encrypt for our own public key (blind store so we retrieve it ourselves)
	env, err := sessionMgr.BuildEnvelope(keys.X25519PK, plaintextMsg, myURN)
	if err != nil {
		fmt.Printf("❌ BuildEnvelope failed: %v\n", err)
		os.Exit(1)
	}

	msgID, err := mqClient.Store(ctx, *platformAddrInfo, myURN, env, 7)
	if err != nil {
		fmt.Printf("❌ MQ Store failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("✅ Message stored successfully on MQ! MessageID: %s\n\n", msgID)

	fmt.Printf("--- Step 4: Retrieving Message from MQ ---\n")
	envs, err := mqClient.Retrieve(ctx, *platformAddrInfo, myURN)
	if err != nil {
		fmt.Printf("❌ MQ Retrieve failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("✅ Retrieved %d message(s) from MQ!\n", len(envs))

	var retrievedEnv *proto.EncryptedEnvelope
	for _, e := range envs {
		if e.MessageId == env.MessageId {
			retrievedEnv = e
			break
		}
	}

	if retrievedEnv == nil {
		fmt.Printf("❌ Failed to find our stored message in the retrieved messages.\n")
		os.Exit(1)
	}
	fmt.Printf("Found stored message in retrieved envelope list.\n\n")

	fmt.Printf("--- Step 5: Decrypting Message ---\n")
	decryptedBytes, err := sessionMgr.DecryptEnvelope(retrievedEnv)
	if err != nil {
		fmt.Printf("❌ DecryptEnvelope failed: %v\n", err)
		os.Exit(1)
	}

	var chatMsg proto.ChatMessage
	err = goproto.Unmarshal(decryptedBytes, &chatMsg)
	if err != nil {
		fmt.Printf("❌ Unmarshal chat message failed: %v\n", err)
		os.Exit(1)
	}

	textBody := chatMsg.GetText()
	if textBody == nil {
		fmt.Printf("❌ Message has no text body.\n")
		os.Exit(1)
	}

	fmt.Printf("✅ Decrypted Message Plaintext: \"%s\"\n\n", textBody.Text)

	fmt.Printf("--- Step 6: Acknowledging (Deleting) Message from MQ ---\n")
	deletedCount, err := mqClient.Ack(ctx, *platformAddrInfo, []string{retrievedEnv.MessageId})
	if err != nil {
		fmt.Printf("❌ MQ Ack failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("✅ MQ Ack succeeded. Deleted messages count: %d\n\n", deletedCount)

	fmt.Printf("==================================================\n")
	fmt.Printf("🎉 ALL PLATFORM INTEGRATION TESTS PASSED SUCCESSFULLY! 🎉\n")
	fmt.Printf("==================================================\n")
}
