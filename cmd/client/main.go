// Client node: connects to bootstrap, registers, pulls offline messages, sends messages.
package main

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/nousresearch/hermes-agent/agent-comm/contacts"
	"github.com/nousresearch/hermes-agent/agent-comm/crypto"
	"github.com/nousresearch/hermes-agent/agent-comm/dht"
	"github.com/nousresearch/hermes-agent/agent-comm/libp2p"
	"github.com/nousresearch/hermes-agent/agent-comm/mq"
	"github.com/nousresearch/hermes-agent/agent-comm/proto"
	"github.com/nousresearch/hermes-agent/agent-comm/registry"
	"github.com/nousresearch/hermes-agent/agent-comm/session"
	"github.com/nousresearch/hermes-agent/agent-comm/wot"
	goproto "google.golang.org/protobuf/proto"
)

func main() {
	fmt.Println("=== agent-comm Client Node ===")
	fmt.Println()

	ctx := context.Background()

	// Step 1: Load or create persistent identity
	keysDir := os.Getenv("AGENT_KEYSDIR")
	if keysDir == "" {
		keysDir = "/tmp/client_keys"
	}
	keys, err := crypto.LoadOrCreateIdentity(keysDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load identity: %v\n", err)
		os.Exit(1)
	}

	peerID, _ := keys.PeerID()
	urn := keys.Ed25519.URN()
	fmt.Printf("Identity:\n  URN: %s\n  PeerID: %s\n\n", urn, peerID)

	// Step 2: Bootstrap node address
	bootstrapAddr := os.Getenv("BOOTSTRAP_ADDR")
	if bootstrapAddr == "" {
		bootstrapAddr = "/ip4/127.0.0.1/tcp/45041/p2p/12D3KooWHTJsARN6DBRoscxnNg2vaQQhXHqFXBuzWEG65y3JjhDX"
		fmt.Println("Using default bootstrap (set BOOTSTRAP_ADDR env to override)")
	}
	fmt.Printf("Bootstrap: %s\n\n", bootstrapAddr)

	// Step 3: Create libp2p host
	cfg := libp2p.Config{
		ListenAddrs:  []string{"/ip4/0.0.0.0/tcp/0", "/ip4/0.0.0.0/udp/0/quic"},
		EnableRelay:  true,
		PrivKeyBytes: keys.Ed25519.PrivateKey,
	}
	h, err := libp2p.NewHost(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create host: %v\n", err)
		os.Exit(1)
	}
	defer h.Close()

	fmt.Printf("Host: %s on %v\n\n", h.ID(), h.Addrs())

	// Step 4: Connect to bootstrap
	bootstrapInfo, err := peer.AddrInfoFromString(bootstrapAddr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to parse bootstrap: %v\n", err)
		os.Exit(1)
	}
	h.Peerstore().AddAddrs(bootstrapInfo.ID, bootstrapInfo.Addrs, peerstore.TempAddrTTL)
	connCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	if err := h.Connect(connCtx, *bootstrapInfo); err != nil {
		cancel()
		fmt.Fprintf(os.Stderr, "Failed to connect to bootstrap: %v\n", err)
		os.Exit(1)
	}
	cancel()
	fmt.Printf("Connected to bootstrap: %s\n\n", bootstrapInfo.ID)

	// Step 5: DHT client
	dhtCfg := dht.DHTConfig{Mode: dht.ModeClient, Bootstraps: []peer.AddrInfo{*bootstrapInfo}}
	d, err := dht.NewDHT(ctx, h, dhtCfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create DHT: %v\n", err)
		os.Exit(1)
	}
	defer d.Close()
	dht.Bootstrap(ctx, d)

	// Step 6: Register with bootstrap registry
	regClient := registry.NewClient(h)
	if err := regClient.Register(*bootstrapInfo, urn, h.Addrs(), keys.X25519PK); err != nil {
		fmt.Printf("  [Note] Register: %v\n", err)
	} else {
		fmt.Printf("Registered with bootstrap registry: %s -> %s\n", urn, h.ID())
	}

	// Step 7: Session + MQ + WoT + Contacts setup
	mgr := session.NewManager(h, keys)
	mqClient := mq.NewClient(h)

	// Contacts store
	contactsDB := os.Getenv("CONTACTS_DB")
	if contactsDB == "" {
		contactsDB = "/tmp/client_contacts.db"
	}
	contactStore, err := contacts.NewStore(contactsDB)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to open contacts store: %v\n", err)
		os.Exit(1)
	}
	defer contactStore.Close()

	// WoT store
	wotDB := os.Getenv("WOT_DB")
	if wotDB == "" {
		wotDB = "/tmp/client_wot.db"
	}
	wotStore, err := wot.NewStore(wotDB, keys)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to open WoT store: %v\n", err)
		os.Exit(1)
	}
	defer wotStore.Close()

	// Register our own keys so we can verify self-signed claims
	wotStore.AddKnownPeer(urn, h.ID().String(), keys.X25519PK, keys.Ed25519.PublicKey)

	wotResolver := wot.NewResolver(h, wotStore)
	wot.RegisterWOTHandler(h, wotStore)

	// Handle incoming session streams
	h.SetStreamHandler(session.ProtoID, func(stream network.Stream) {
		handleSessionStream(stream, mgr)
	})

	// Pull offline messages on startup
	go pullOfflineMessages(ctx, mgr, mqClient, *bootstrapInfo, urn)

	// Print client info
	fmt.Println("\n=== CLIENT INFO ===")
	for _, addr := range h.Addrs() {
		fmt.Printf("  %s/p2p/%s\n", addr.String(), h.ID())
	}
	fmt.Println("==================\n")

	// Interactive messaging loop
	fmt.Println("Commands:")
	fmt.Println("  send <recipientURN> <message>  — send an encrypted message (checks WoT trust)")
	fmt.Println("  trust <urn>                    — mark a peer as directly trusted (bootstrap)")
	fmt.Println("  untrust <urn>                  — remove trust mark")
	fmt.Println("  claim <subjectURN>             — issue a TRUSTED claim about a peer (needs peer info)")
	fmt.Println("  trustpath <urn>                — check if there's a trust path to URN")
	fmt.Println("  contacts                      — list all contacts")
	fmt.Println("  pull                          — pull offline messages")
	fmt.Println("  quit                          — exit")
	fmt.Println()

	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, " ", 3)
		cmd := parts[0]

		switch cmd {
		case "quit", "exit":
			fmt.Println("Goodbye.")
			return

		case "pull":
			pullOfflineMessages(ctx, mgr, mqClient, *bootstrapInfo, urn)

		case "send":
			if len(parts) < 3 {
				fmt.Println("Usage: send <recipientURN> <message>")
				continue
			}
			recipientURN := parts[1]
			msg := parts[2]
			sendMessage(ctx, mgr, mqClient, *bootstrapInfo, keys, urn, h, contactStore, wotResolver, wotStore, recipientURN, msg)

		case "trust":
			if len(parts) < 2 {
				fmt.Println("Usage: trust <urn>")
				continue
			}
			targetURN := parts[1]
			trustPeer(ctx, h, bootstrapInfo, contactStore, wotStore, keys, targetURN)

		case "untrust":
			if len(parts) < 2 {
				fmt.Println("Usage: untrust <urn>")
				continue
			}
			targetURN := parts[1]
			if err := contactStore.SetTrusted(targetURN, false); err != nil {
				fmt.Printf("Error: %v\n", err)
			} else {
				fmt.Printf("Untrusted: %s\n", targetURN)
			}

		case "claim":
			if len(parts) < 2 {
				fmt.Println("Usage: claim <subjectURN>")
				continue
			}
			subjectURN := parts[1]
			issueClaim(ctx, h, bootstrapInfo, wotStore, keys, subjectURN)

		case "trustpath":
			if len(parts) < 2 {
				fmt.Println("Usage: trustpath <urn>")
				continue
			}
			targetURN := parts[1]
			checkTrustPath(ctx, wotResolver, wotStore, contactStore, keys, targetURN)

		case "contacts":
			list, _ := contactStore.List()
			if len(list) == 0 {
				fmt.Println("No contacts.")
			}
			for _, c := range list {
				trusted := ""
				if c.Trusted {
					trusted = " [TRUSTED]"
				}
				fp := contacts.Fingerprint(c.X25519PK)
				fmt.Printf("  %s%s (PeerID=%s, fp=%s)\n", c.URN, trusted, c.PeerID, fp)
			}

		default:
			fmt.Printf("Unknown command: %s\n", cmd)
		}
	}
}

// ---- Session handling ----

func handleSessionStream(stream network.Stream, mgr *session.Manager) {
	defer stream.Close()

	sizeBuf := make([]byte, 4)
	if _, err := stream.Read(sizeBuf); err != nil {
		return
	}
	size := binary.BigEndian.Uint32(sizeBuf)
	envBytes := make([]byte, size)
	if _, err := stream.Read(envBytes); err != nil {
		return
	}

	var env proto.EncryptedEnvelope
	if err := goproto.Unmarshal(envBytes, &env); err != nil {
		return
	}

	plaintext, err := mgr.DecryptEnvelope(&env)
	if err != nil {
		fmt.Printf("[session] from %s: decrypt FAILED: %v\n", env.SenderUrn, err)
		return
	}

	var msg proto.ChatMessage
	if err := goproto.Unmarshal(plaintext, &msg); err != nil {
		fmt.Printf("[session] from %s: %q (parse error)\n", env.SenderUrn, string(plaintext))
		return
	}

	if txt := msg.GetText(); txt != nil {
		fmt.Printf("[session] from %s: %q\n", env.SenderUrn, txt.Text)
	}
}

// ---- WoT commands ----

// trustPeer resolves a peer's info from the registry and adds them as a trusted contact.
// This is the bootstrap trust mechanism: direct manual trust.
func trustPeer(ctx context.Context, h host.Host, bootstrap *peer.AddrInfo, contactStore *contacts.Store, wotStore *wot.Store, keys *crypto.IdentityKeys, targetURN string) {
	if targetURN == keys.Ed25519.URN() {
		fmt.Println("Cannot trust yourself.")
		return
	}

	// Resolve from registry
	resolved, err := registry.NewClient(h).Resolve(*bootstrap, targetURN)
	if err != nil {
		fmt.Printf("[trust] resolve %s failed: %v\n", targetURN, err)
		return
	}

	// Add as known peer in WoT store (so we can verify their future claims)
	wotStore.AddKnownPeer(targetURN, resolved.ID.String(), resolved.X25519PubKey, keys.Ed25519.PublicKey) // Note: we don't have their Ed25519 pubkey here yet

	// Add as trusted contact
	c := &contacts.Contact{
		URN:         targetURN,
		PeerID:      resolved.ID.String(),
		X25519PK:    resolved.X25519PubKey,
		Ed25519PK:   nil, // don't have it yet
		DisplayName: "",
		Trusted:     true,
	}
	if err := contactStore.Add(c); err != nil {
		fmt.Printf("[trust] add contact failed: %v\n", err)
		return
	}
	fmt.Printf("[trust] %s marked as TRUSTED (PeerID=%s)\n", targetURN, resolved.ID)
}

// issueClaim creates a TRUSTED claim about a subject.
// The subject must already be known (we have their info from registry).
func issueClaim(ctx context.Context, h host.Host, bootstrap *peer.AddrInfo, wotStore *wot.Store, keys *crypto.IdentityKeys, subjectURN string) {
	if subjectURN == keys.Ed25519.URN() {
		fmt.Println("Cannot issue a claim about yourself.")
		return
	}

	// Resolve subject's info
	resolved, err := registry.NewClient(h).Resolve(*bootstrap, subjectURN)
	if err != nil {
		fmt.Printf("[claim] resolve %s failed: %v\n", subjectURN, err)
		return
	}

	// Add as known peer so we can reference them
	wotStore.AddKnownPeer(subjectURN, resolved.ID.String(), resolved.X25519PubKey, keys.Ed25519.PublicKey)

	// Create and sign the claim
	claim, err := wot.NewDirectTrustClaim(keys, subjectURN, resolved.ID.String(), resolved.X25519PubKey)
	if err != nil {
		fmt.Printf("[claim] create failed: %v\n", err)
		return
	}

	// Store our own claim
	if err := wotStore.AddMyClaim(claim); err != nil {
		fmt.Printf("[claim] store failed: %v\n", err)
		return
	}
	fmt.Printf("[claim] Issued TRUSTED claim about %s (PeerID=%s)\n", subjectURN, resolved.ID)
}

// checkTrustPath checks if there's a trust path to the target URN.
func checkTrustPath(ctx context.Context, resolver *wot.Resolver, store *wot.Store, contactStore *contacts.Store, keys *crypto.IdentityKeys, targetURN string) {
	path, err := resolver.FindTrustPath(ctx, targetURN)
	if err != nil {
		fmt.Printf("[trustpath] No trust path to %s: %v\n", targetURN, err)
		return
	}

	fmt.Printf("[trustpath] Path to %s (depth=%d):\n", targetURN, path.Depth)
	for i, c := range path.Claims {
		fmt.Printf("  [%d] %s --TRUSTED--> %s\n", i, c.IssuerUrn, c.SubjectUrn)
	}
	fmt.Printf("  Trusted pubkey: %x\n", path.TrustedPK[:8])
}

// ---- Message sending ----

// sendMessage sends an encrypted message, checking WoT trust first.
func sendMessage(ctx context.Context, mgr *session.Manager, mqClient *mq.Client, relay peer.AddrInfo, keys *crypto.IdentityKeys, myURN string, h host.Host, contactStore *contacts.Store, wotResolver *wot.Resolver, wotStore *wot.Store, recipientURN, plaintext string) {
	// Resolve recipient via registry
	resolved, err := registry.NewClient(mgr.Host()).Resolve(relay, recipientURN)
	if err != nil {
		fmt.Printf("[send] resolve %s failed: %v\n", recipientURN, err)
		return
	}
	recipientPubKey := resolved.X25519PubKey
	if len(recipientPubKey) != 32 {
		fmt.Printf("[send] recipient X25519 pubkey invalid (len=%d)\n", len(recipientPubKey))
		return
	}

	// --- WoT Trust Check ---
	// Check 1: direct contact trust
	isTrusted := contactStore.IsTrusted(recipientURN)

	// Check 2: WoT trust path
	if !isTrusted {
		isTrusted = wotResolver.IsTrusted(recipientURN)
	}

	// Check 3: if we have this peer's X25519 in our contacts store, verify it matches
	contactX25519, _, err := contactStore.GetPubkeys(recipientURN)
	if err == nil && len(contactX25519) == 32 {
		// Use the cached (trusted) pubkey
		if string(recipientPubKey) != string(contactX25519) {
			fmt.Printf("[send] WARNING: recipient pubkey differs from cached contact pubkey for %s\n", recipientURN)
			fmt.Printf("[send] Cached:  %x\n", contactX25519[:8])
			fmt.Printf("[send] Registry: %x\n", recipientPubKey[:8])
			fmt.Printf("[send] Proceeding with registry pubkey...\n")
		}
	}

	if !isTrusted {
		fmt.Printf("[send] WARNING: %s is not in your trust graph.\n", recipientURN)
		fmt.Printf("[send] Proceeding anyway (MITM risk). To trust: 'trust %s'\n", recipientURN)
	}

	// --- Send ---
	addrInfo := peer.AddrInfo{ID: resolved.ID, Addrs: resolved.Addrs}
	reply, err := mgr.SendMessage(ctx, addrInfo, recipientPubKey, plaintext)
	if err != nil {
		// Offline — store via MQ
		fmt.Printf("[send] direct send failed (%v), storing via MQ relay...\n", err)

		envelope, err := mgr.BuildEnvelope(recipientPubKey, plaintext)
		if err != nil {
			fmt.Printf("[send] build envelope: %v\n", err)
			return
		}
		msgID, err := mqClient.Store(ctx, relay, recipientURN, envelope, 7)
		if err != nil {
			fmt.Printf("[send] MQ store failed: %v\n", err)
			return
		}
		fmt.Printf("[send] stored via relay: msg_id=%s\n", msgID)
		return
	}
	fmt.Printf("[send] -> %s: %q (reply: %q)\n", recipientURN, plaintext, reply)
}

// pullOfflineMessages retrieves and decrypts any pending messages from the relay.
func pullOfflineMessages(ctx context.Context, mgr *session.Manager, mqClient *mq.Client, relay peer.AddrInfo, myURN string) {
	envelopes, err := mqClient.Retrieve(ctx, relay, myURN)
	if err != nil {
		fmt.Printf("[mq] retrieve error: %v\n", err)
		return
	}
	if len(envelopes) == 0 {
		fmt.Println("[mq] no pending messages")
		return
	}

	fmt.Printf("[mq] retrieved %d message(s):\n", len(envelopes))
	var toAck []string
	for _, env := range envelopes {
		if env.MessageId != "" {
			toAck = append(toAck, env.MessageId)
		}
		plaintext, err := mgr.DecryptEnvelope(env)
		if err != nil {
			fmt.Printf("  [decrypt] from %s FAILED: %v\n", env.SenderUrn, err)
			continue
		}
		var msg proto.ChatMessage
		if err := goproto.Unmarshal(plaintext, &msg); err != nil {
			fmt.Printf("  from %s: %q (parse error: %v)\n", env.SenderUrn, string(plaintext), err)
			continue
		}
		if txt := msg.GetText(); txt != nil {
			fmt.Printf("  from %s: %q (ts=%d)\n", env.SenderUrn, txt.Text, txt.Timestamp)
		}
	}

	if len(toAck) > 0 {
		deleted, err := mqClient.Ack(ctx, relay, toAck)
		if err != nil {
			fmt.Printf("[mq] ack error: %v\n", err)
		} else {
			fmt.Printf("[mq] acked %d message(s) from relay\n", deleted)
		}
	}
}