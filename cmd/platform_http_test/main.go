package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/BillShiyaoZhang/agent-comm/crypto"
	"github.com/BillShiyaoZhang/agent-comm/mq"
	"github.com/BillShiyaoZhang/agent-comm/registry"
	"github.com/BillShiyaoZhang/agent-comm/session"
	"github.com/BillShiyaoZhang/agent-comm/proto"
	goproto "google.golang.org/protobuf/proto"

	"github.com/libp2p/go-libp2p"
)

func main() {
	targetURL := flag.String("target", "http://localhost:8080", "Target platform HTTP base URL")
	flag.Parse()

	fmt.Printf("==================================================\n")
	fmt.Printf("=== HTTP REST API Signature Verification Test ===\n")
	fmt.Printf("Target: %s\n", *targetURL)
	fmt.Printf("==================================================\n\n")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// 1. Generate identity keys
	keysDir := filepath.Join(os.TempDir(), "platform_http_test_keys")
	os.RemoveAll(keysDir)
	keys, err := crypto.LoadOrCreateIdentity(keysDir)
	if err != nil {
		fmt.Printf("❌ Failed to create identity: %v\n", err)
		os.Exit(1)
	}
	myURN := keys.Ed25519.URN()
	fmt.Printf("Generated URN: %s\n", myURN)

	// Create a dummy libp2p host just to get a Peer ID
	h, err := libp2p.New()
	if err != nil {
		fmt.Printf("❌ Failed to create libp2p host: %v\n", err)
		os.Exit(1)
	}
	defer h.Close()
	myPeerID := h.ID().String()
	fmt.Printf("Generated PeerID: %s\n\n", myPeerID)

	// 2. Initialize HTTP Clients
	regClient := registry.NewHTTPClient(*targetURL, keys)
	mqClient := mq.NewHTTPClient(*targetURL, keys)
	sessionMgr := session.NewManager(h, keys)

	// 3. Test Registry Register
	fmt.Println("--- Step 1: Registering URN on HTTP Registry ---")
	addrs := []string{"/ip4/127.0.0.1/tcp/12345"}
	err = regClient.Register(myURN, myPeerID, addrs, nil, keys.X25519PK)
	if err != nil {
		fmt.Printf("❌ HTTP Registration failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("✅ Registered successfully!\n\n")

	// 4. Test Registry Resolve
	fmt.Println("--- Step 2: Resolving URN from HTTP Registry ---")
	res, err := regClient.Resolve(myURN)
	if err != nil {
		fmt.Printf("❌ HTTP Resolution failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("✅ Resolved successfully!\n")
	fmt.Printf("  PeerID: %s\n", res.ID)
	fmt.Printf("  Addrs: %v\n", res.Addrs)
	fmt.Printf("  X25519 Pubkey len: %d bytes\n\n", len(res.X25519PubKey))

	if res.ID.String() != myPeerID {
		fmt.Printf("❌ Error: Resolved PeerID %s mismatch expected %s\n", res.ID.String(), myPeerID)
		os.Exit(1)
	}

	// 5. Test MQ Store
	fmt.Println("--- Step 3: Storing Envelope in HTTP MQ ---")
	testMsg := "Hello Hermes! Testing HTTP Signature Authentication."
	env, err := sessionMgr.BuildEnvelope(keys.X25519PK, testMsg)
	if err != nil {
		fmt.Printf("❌ BuildEnvelope failed: %v\n", err)
		os.Exit(1)
	}

	msgID, err := mqClient.Store(ctx, myURN, env, 7)
	if err != nil {
		fmt.Printf("❌ MQ Store failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("✅ Envelope stored successfully! MessageID: %s\n\n", msgID)

	// 6. Test MQ Retrieve
	fmt.Println("--- Step 4: Retrieving Envelope from HTTP MQ ---")
	envs, err := mqClient.Retrieve(ctx, myURN)
	if err != nil {
		fmt.Printf("❌ MQ Retrieve failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("✅ Retrieved %d message(s)!\n", len(envs))

	var retrievedEnv *proto.EncryptedEnvelope
	for _, e := range envs {
		if e.MessageId == msgID {
			retrievedEnv = e
			break
		}
	}
	if retrievedEnv == nil {
		fmt.Println("❌ Error: Stored message ID not found in retrieved list")
		os.Exit(1)
	}

	// 7. Decrypt message
	fmt.Println("--- Step 5: Decrypting Message ---")
	decryptedBytes, err := sessionMgr.DecryptEnvelope(retrievedEnv)
	if err != nil {
		fmt.Printf("❌ DecryptEnvelope failed: %v\n", err)
		os.Exit(1)
	}

	var chatMsg proto.ChatMessage
	if err := goproto.Unmarshal(decryptedBytes, &chatMsg); err != nil {
		fmt.Printf("❌ Unmarshal ChatMessage failed: %v\n", err)
		os.Exit(1)
	}

	text := chatMsg.GetText()
	if text == nil {
		fmt.Println("❌ Error: ChatMessage text body is nil")
		os.Exit(1)
	}
	fmt.Printf("✅ Decrypted Message Plaintext: %q\n\n", text.Text)

	if text.Text != testMsg {
		fmt.Printf("❌ Error: Decrypted plaintext %q does not match original %q\n", text.Text, testMsg)
		os.Exit(1)
	}

	// 8. Test MQ Ack
	fmt.Println("--- Step 6: Acknowledging (Deleting) Message ---")
	deletedCount, err := mqClient.Ack(ctx, []string{msgID})
	if err != nil {
		fmt.Printf("❌ MQ Ack failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("✅ MQ Ack succeeded. Deleted messages count: %d\n\n", deletedCount)

	fmt.Printf("==================================================\n")
	fmt.Printf("🎉 ALL HTTP SIGNATURE VERIFICATION TESTS PASSED! 🎉\n")
	fmt.Printf("==================================================\n")
}
