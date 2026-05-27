package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/BillShiyaoZhang/agent-comm/crypto"
	"github.com/BillShiyaoZhang/agent-comm/libp2p"
	"github.com/BillShiyaoZhang/agent-comm/mq"
	"github.com/BillShiyaoZhang/agent-comm/registry"
	"github.com/BillShiyaoZhang/agent-comm/session"
	"github.com/BillShiyaoZhang/agent-comm/proto"
	goproto "google.golang.org/protobuf/proto"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
)

const (
	PlatformDomain = "agent-communication.online"
	PlatformPeerID = "12D3KooWJVLmsJZvHoKYcQDDTWwf1mvqLxfZTceHU5N7t27j6Rvn"
	PlatformURN    = "urn:hermes:platform:ee8be13add63a020"
)

func main() {
	fmt.Printf("==================================================\n")
	fmt.Printf("=== Deployed Platform Integration Test Client ===\n")
	fmt.Printf("==================================================\n\n")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// 1. Prepare local identity
	keysDir := "/tmp/platform_test_keys"
	os.RemoveAll(keysDir)
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

	// 3. Set up address info for remote platform node (We'll prioritize UDP/QUIC then fallback to TCP)
	tcpMaddrStr := fmt.Sprintf("/dns4/%s/tcp/45041/p2p/%s", PlatformDomain, PlatformPeerID)
	quicMaddrStr := fmt.Sprintf("/dns4/%s/udp/45041/quic-v1/p2p/%s", PlatformDomain, PlatformPeerID)

	var platformAddrInfo *peer.AddrInfo
	var connectErr error

	// Try QUIC first
	fmt.Printf("Attempting connection via UDP/QUIC: %s\n", quicMaddrStr)
	platformAddrInfo, err = peer.AddrInfoFromString(quicMaddrStr)
	if err == nil {
		h.Peerstore().AddAddrs(platformAddrInfo.ID, platformAddrInfo.Addrs, peerstore.TempAddrTTL)
		connectErr = h.Connect(ctx, *platformAddrInfo)
		if connectErr == nil {
			fmt.Printf("✅ Connected via UDP/QUIC successfully!\n\n")
		} else {
			fmt.Printf("⚠️ UDP/QUIC connection failed: %v\n", connectErr)
		}
	} else {
		fmt.Printf("⚠️ Failed to parse UDP/QUIC address: %v\n", err)
	}

	// Fallback to TCP if QUIC failed or wasn't tried
	if connectErr != nil || platformAddrInfo == nil {
		fmt.Printf("Attempting connection via TCP: %s\n", tcpMaddrStr)
		platformAddrInfo, err = peer.AddrInfoFromString(tcpMaddrStr)
		if err != nil {
			fmt.Printf("❌ Failed to parse TCP address: %v\n", err)
			os.Exit(1)
		}
		h.Peerstore().AddAddrs(platformAddrInfo.ID, platformAddrInfo.Addrs, peerstore.TempAddrTTL)
		connectErr = h.Connect(ctx, *platformAddrInfo)
		if connectErr != nil {
			fmt.Printf("❌ Connection via TCP failed: %v\n", connectErr)
			os.Exit(1)
		}
		fmt.Printf("✅ Connected via TCP successfully!\n\n")
	}

	// 4. Registry Integration
	regClient := registry.NewClient(h)

	fmt.Printf("--- Step 1: Registering URN on Platform Registry ---\n")
	err = regClient.Register(*platformAddrInfo, myURN, h.Addrs(), keys.X25519PK)
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
	env, err := sessionMgr.BuildEnvelope(keys.X25519PK, plaintextMsg)
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
