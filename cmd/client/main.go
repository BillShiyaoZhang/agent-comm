// Client node: command line interface supporting subcommands and interactive shell.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/BillShiyaoZhang/agent-comm/agent"
	"github.com/BillShiyaoZhang/agent-comm/contacts"
	"github.com/BillShiyaoZhang/agent-comm/proto"
	"github.com/BillShiyaoZhang/agent-comm/wot"
	"github.com/BillShiyaoZhang/agent-comm/registry"

	libp2pcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/multiformats/go-multiaddr"
	goproto "google.golang.org/protobuf/proto"
)

// ---------------------------------------------------------------------------
// Inbox: in-memory message queue for Chat Relay mode.
// When a host AI agent is actively polling the inbox, incoming messages are
// queued here instead of being auto-responded to. The host AI reads messages
// via GET /messages/inbox and sends replies via POST /messages/send.
// ---------------------------------------------------------------------------

type InboxMessage struct {
	ID        string    `json:"id"`
	SenderURN string    `json:"sender_urn"`
	Content   string    `json:"content"`
	Timestamp time.Time `json:"timestamp"`
}

type Inbox struct {
	mu       sync.Mutex
	messages []InboxMessage
	lastPoll time.Time
}

func NewInbox() *Inbox {
	return &Inbox{}
}

// Push adds a new message to the inbox queue.
func (inbox *Inbox) Push(senderURN, content string) {
	inbox.mu.Lock()
	defer inbox.mu.Unlock()
	inbox.messages = append(inbox.messages, InboxMessage{
		ID:        fmt.Sprintf("msg-%d", time.Now().UnixNano()),
		SenderURN: senderURN,
		Content:   content,
		Timestamp: time.Now(),
	})
}

// Drain returns all pending messages and clears the queue.
// It also updates lastPoll to indicate the host AI is active.
func (inbox *Inbox) Drain() []InboxMessage {
	inbox.mu.Lock()
	defer inbox.mu.Unlock()
	inbox.lastPoll = time.Now()
	msgs := inbox.messages
	inbox.messages = nil
	return msgs
}

// HostIsActive returns true if a host AI polled within the last 60 seconds.
func (inbox *Inbox) HostIsActive() bool {
	inbox.mu.Lock()
	defer inbox.mu.Unlock()
	return time.Since(inbox.lastPoll) < 60*time.Second
}

func main() {
	ctx := context.Background()

	// 1. Parse global configuration flags from os.Args
	var customBootstrap string
	var customKeysDir string
	var customDBPath string
	var customLocalAddr string

	newArgs := []string{os.Args[0]}
	for i := 1; i < len(os.Args); i++ {
		arg := os.Args[i]
		if arg == "--bootstrap" || arg == "-b" {
			if i+1 < len(os.Args) {
				customBootstrap = os.Args[i+1]
				i++
			} else {
				fmt.Fprintf(os.Stderr, "❌ Error: --bootstrap / -b requires an argument\n")
				os.Exit(1)
			}
		} else if strings.HasPrefix(arg, "--bootstrap=") {
			customBootstrap = strings.TrimPrefix(arg, "--bootstrap=")
		} else if strings.HasPrefix(arg, "-b=") {
			customBootstrap = strings.TrimPrefix(arg, "-b=")
		} else if arg == "--keysdir" {
			if i+1 < len(os.Args) {
				customKeysDir = os.Args[i+1]
				i++
			} else {
				fmt.Fprintf(os.Stderr, "❌ Error: --keysdir requires an argument\n")
				os.Exit(1)
			}
		} else if strings.HasPrefix(arg, "--keysdir=") {
			customKeysDir = strings.TrimPrefix(arg, "--keysdir=")
		} else if arg == "--dbpath" {
			if i+1 < len(os.Args) {
				customDBPath = os.Args[i+1]
				i++
			} else {
				fmt.Fprintf(os.Stderr, "❌ Error: --dbpath requires an argument\n")
				os.Exit(1)
			}
		} else if strings.HasPrefix(arg, "--dbpath=") {
			customDBPath = strings.TrimPrefix(arg, "--dbpath=")
		} else if arg == "--local-addr" || arg == "-l" {
			if i+1 < len(os.Args) {
				customLocalAddr = os.Args[i+1]
				i++
			} else {
				fmt.Fprintf(os.Stderr, "❌ Error: --local-addr / -l requires an argument\n")
				os.Exit(1)
			}
		} else if strings.HasPrefix(arg, "--local-addr=") {
			customLocalAddr = strings.TrimPrefix(arg, "--local-addr=")
		} else if strings.HasPrefix(arg, "-l=") {
			customLocalAddr = strings.TrimPrefix(arg, "-l=")
		} else {
			newArgs = append(newArgs, arg)
		}
	}
	os.Args = newArgs

	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "inbox":
			handleInboxCmd(customLocalAddr)
			return
		case "reply":
			handleReplyCmd(customLocalAddr)
			return
		case "help", "--help", "-h":
			printUsage()
			return
		}
	}

	// 2. Resolve final configurations (CLI flags override environment variables)
	keysDir := os.Getenv("AGENT_KEYSDIR")
	if customKeysDir != "" {
		keysDir = customKeysDir
	}
	if keysDir == "" {
		if execPath, err := os.Executable(); err == nil {
			execDir := filepath.Dir(execPath)
			// Avoid using temp build directory of "go run" as the skill keys dir,
			// and ensure the executable directory is writable.
			if !strings.Contains(execPath, "go-build") &&
				!strings.Contains(execDir, filepath.Join("Temp", "go-build")) &&
				isDirWritable(execDir) {
				keysDir = filepath.Join(execDir, "keys")
			}
		}
		if keysDir == "" {
			home, err := os.UserHomeDir()
			if err == nil {
				keysDir = filepath.Join(home, ".agent-comm", "keys")
			} else {
				keysDir = "/tmp/client_keys"
			}
		}
	}

	dbPath := os.Getenv("DB_PATH")
	if customDBPath != "" {
		dbPath = customDBPath
	}
	if dbPath == "" {
		dbPath = filepath.Join(keysDir, "agent_comm.db")
	}

	wotDB := os.Getenv("WOT_DB")
	if wotDB == "" {
		wotDB = filepath.Join(keysDir, "wot.db")
	}

	bootstrapAddr := os.Getenv("BOOTSTRAP_ADDR")
	if customBootstrap != "" {
		bootstrapAddr = customBootstrap
	}

	localAddr := os.Getenv("LOCAL_ADDR")
	if customLocalAddr != "" {
		localAddr = customLocalAddr
	}
	if localAddr == "" {
		localAddr = ":8000"
	}

	bindAddr := localAddr
	if strings.Contains(bindAddr, "://") {
		if u, err := url.Parse(bindAddr); err == nil {
			bindAddr = u.Host
		}
	}
	if !strings.Contains(bindAddr, ":") {
		bindAddr = ":" + bindAddr
	}

	if bootstrapAddr != "" {
		if !strings.Contains(bootstrapAddr, "/p2p/") && !strings.Contains(bootstrapAddr, "/ipfs/") {
			fmt.Println("ℹ️ Bootstrap address missing PeerID. Resolving dynamically...")
			resolved, err := resolveBootstrapAddress(ctx, bootstrapAddr)
			if err != nil {
				fmt.Fprintf(os.Stderr, "❌ Failed to resolve PeerID for %q: %v\n", bootstrapAddr, err)
				os.Exit(1)
			}
			bootstrapAddr = resolved
		}
	}

	var bootstrapNodes []peer.AddrInfo
	var bootstrapInfo *peer.AddrInfo

	if bootstrapAddr != "" {
		var err error
		bootstrapInfo, err = peer.AddrInfoFromString(bootstrapAddr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "❌ Failed to parse bootstrap address %q: %v\n", bootstrapAddr, err)
			os.Exit(1)
		}
		bootstrapNodes = []peer.AddrInfo{*bootstrapInfo}
	} else {
		fmt.Println("ℹ️ Running in standalone local mode (no bootstrap/platform configured).")
	}

	// 3. Initialize High-level Agent
	a, err := agent.InitIdentity(ctx, agent.Config{
		KeysDir:        keysDir,
		DBPath:         dbPath,
		BootstrapNodes: bootstrapNodes,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Failed to init agent: %v\n", err)
		os.Exit(1)
	}
	defer a.Close()

	// Make sure we connect to the bootstrap/platform node if configured
	if bootstrapInfo != nil {
		a.Host.Peerstore().AddAddrs(bootstrapInfo.ID, bootstrapInfo.Addrs, peerstore.PermanentAddrTTL)
		if err := a.Host.Connect(ctx, *bootstrapInfo); err != nil {
			fmt.Printf("⚠️ Warning: Failed to connect to platform bootstrap node: %v\n", err)
			fmt.Println("🔄 Attempting to dynamically resolve latest Bootstrap PeerID from platform...")
			newAddr, resolveErr := resolveBootstrapAddress(ctx, bootstrapAddr)
			if resolveErr == nil && newAddr != bootstrapAddr {
				newInfo, parseErr := peer.AddrInfoFromString(newAddr)
				if parseErr == nil {
					fmt.Printf("✅ Resolved updated bootstrap address: %s\n", newAddr)
					a.Host.Peerstore().AddAddrs(newInfo.ID, newInfo.Addrs, peerstore.PermanentAddrTTL)
					if retryErr := a.Host.Connect(ctx, *newInfo); retryErr == nil {
						fmt.Println("✅ Successfully connected to updated platform bootstrap node!")
						bootstrapInfo = newInfo
						a.BootstrapNodes = []peer.AddrInfo{*newInfo}
						// Register self with the new bootstrap registry
						go func() {
							_ = a.Registry.Register(*newInfo, a.Keys.Ed25519.URN(), a.Host.Addrs(), a.Keys.X25519PK)
						}()
					} else {
						fmt.Printf("❌ Retry connection to resolved address failed: %v\n", retryErr)
					}
				} else {
					fmt.Printf("❌ Failed to parse resolved bootstrap address: %v\n", parseErr)
				}
			} else {
				fmt.Printf("❌ Dynamic bootstrap resolution failed or address unchanged: %v\n", resolveErr)
			}
		}
	}

	// 4. Initialize WoT Store & Resolver
	wotStore, err := wot.NewStore(wotDB, a.Keys)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Failed to open WoT store: %v\n", err)
		os.Exit(1)
	}
	defer wotStore.Close()

	// Register self in WoT store
	wotStore.AddKnownPeer(a.Keys.Ed25519.URN(), a.Host.ID().String(), a.Keys.X25519PK, a.Keys.Ed25519.PublicKey)
	wotResolver := wot.NewResolver(a.Host, wotStore)
	wot.RegisterWOTHandler(a.Host, wotStore)

	// 5. Check for subcommand or run interactive loop
	cmd := ""
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}

	switch cmd {
	case "share":
		card, err := a.GenerateContactCard()
		if err != nil {
			fmt.Fprintf(os.Stderr, "❌ Error generating card: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(card)

	case "import":
		var cardText string
		if len(os.Args) > 2 {
			// Read from file
			filePath := os.Args[2]
			data, err := os.ReadFile(filePath)
			if err != nil {
				// If file read fails, try treating the argument as raw card text
				cardText = filePath
			} else {
				cardText = string(data)
			}
		} else {
			// Read from Stdin
			fmt.Println("Paste the contact card text block below. Type 'END' on a new line when done:")
			scanner := bufio.NewScanner(os.Stdin)
			var lines []string
			for scanner.Scan() {
				txt := scanner.Text()
				if strings.TrimSpace(txt) == "END" {
					break
				}
				lines = append(lines, txt)
			}
			cardText = strings.Join(lines, "\n")
		}

		c, err := a.ImportContactCard(cardText, "")
		if err != nil {
			fmt.Fprintf(os.Stderr, "❌ Error importing contact card: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("✅ Successfully imported contact: %s (PeerID=%s)\n", c.URN, c.PeerID)

	case "send":
		if len(os.Args) < 4 {
			fmt.Println("Usage: send <recipientURN> <message>")
			os.Exit(1)
		}
		recipientURN := os.Args[2]
		msgText := os.Args[3]

		// Perform trust check
		isTrusted := a.Contacts.IsTrusted(recipientURN)
		if !isTrusted {
			isTrusted = wotResolver.IsTrusted(recipientURN)
		}
		if !isTrusted {
			fmt.Printf("⚠️ WARNING: %s is not in your trust graph.\n", recipientURN)
			fmt.Println("To trust this peer, run: trust <urn>")
		}

		err = a.SendMessage(ctx, recipientURN, msgText)
		if err != nil {
			fmt.Fprintf(os.Stderr, "❌ Failed to send message: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("✅ Message sent successfully (delivered or blind-stored on Platform MQ).")

	case "pull":
		if bootstrapInfo == nil {
			fmt.Fprintln(os.Stderr, "❌ Error: 'pull' command requires a bootstrap platform address (use -b or BOOTSTRAP_ADDR).")
			os.Exit(1)
		}
		pullOfflineMessages(ctx, a, *bootstrapInfo)

	case "listen":
		fmt.Printf("==================================================\n")
		fmt.Printf("📡 Starting Daemon Listener Mode\n")
		fmt.Printf("My URN: %s\n", a.Keys.Ed25519.URN())
		fmt.Printf("My PeerID: %s\n", a.Host.ID())
		if bootstrapInfo != nil {
			fmt.Printf("Bootstrap Platform: %s\n", bootstrapAddr)
		} else {
			fmt.Printf("Bootstrap Platform: NONE (Standalone Local Mode)\n")
		}
		fmt.Printf("==================================================\n\n")

		// Initialize message inbox for Chat Relay mode
		inbox := NewInbox()

		// Start local HTTP health-check and info server for Web Dashboard connectivity
		startLocalServer(bindAddr, a, inbox)

		// Define owner command execution handler
		handleMessage := func(senderURN string, msg string) {
			var chatMsg proto.ChatMessage
			actualText := msg
			if err := goproto.Unmarshal([]byte(msg), &chatMsg); err == nil {
				if txt := chatMsg.GetText(); txt != nil {
					actualText = txt.Text
				}
			}

			// Resolve contact to verify trust tier
			contact, err := a.Contacts.Get(senderURN)
			if err != nil {
				fmt.Printf("⚠️ Unauthorized message from %s (not in contacts). Discarding.\n", senderURN)
				return
			}

			if contact.TrustTier != "self" {
				fmt.Printf("⚠️ Unauthorized message from %s (trust_tier = %q). Discarding.\n", senderURN, contact.TrustTier)
				return
			}

			cmdText := strings.TrimSpace(actualText)
			fmt.Printf("[%s] 💬 Message received: %s\n", senderURN, cmdText)

			// Chat Relay mode: if a host AI agent is actively polling the inbox,
			// queue ALL messages for the host AI to handle. No auto-response.
			if inbox.HostIsActive() {
				inbox.Push(senderURN, cmdText)
				fmt.Printf("📥 Queued message for host AI agent\n")
				return
			}

			// Fallback: no host AI polling — handle built-in commands locally
			var responseText string
			switch strings.ToLower(cmdText) {
			case "ping":
				responseText = "pong"
			case "stats", "run stats":
				responseText = fmt.Sprintf("=== Local Agent Stats ===\nURN: %s\nTime: %s\nOS: %s\nStatus: Running", a.Keys.Ed25519.URN(), time.Now().Format(time.RFC3339), runtime.GOOS)
			case "help":
				responseText = "Available commands: ping, stats, help"
			default:
				// No host AI and not a built-in command: queue anyway and inform sender
				inbox.Push(senderURN, cmdText)
				responseText = fmt.Sprintf("Message received: %q. No AI agent is currently connected to process your message. It has been queued.", cmdText)
			}

			fmt.Printf("🤖 Executing built-in command: %s\n", cmdText)
			err = a.SendMessage(ctx, senderURN, responseText)
			if err != nil {
				fmt.Printf("❌ Failed to send response: %v\n", err)
			} else {
				fmt.Printf("✅ Sent response to owner URN: %s\n", senderURN)
			}
		}

		// Register standard message callback
		a.OnMessage(ctx, handleMessage)

		// Run periodic polling ticker in addition to callback listener
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()

		if bootstrapInfo != nil {
			fmt.Println("Listening for direct connections and polling platform MQ (every 10s)... Press Ctrl+C to exit.")
		} else {
			fmt.Println("Listening for direct connections (standalone local P2P mode)... Press Ctrl+C to exit.")
		}

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if bootstrapInfo == nil {
					continue
				}
				// Silent MQ poll and decryption
				envs, err := a.MQClient.Retrieve(ctx, *bootstrapInfo, a.Keys.Ed25519.URN())
				if err == nil && len(envs) > 0 {
					var toAck []string
					for _, env := range envs {
						if env.MessageId != "" {
							toAck = append(toAck, env.MessageId)
						}
						plaintext, err := a.Session.DecryptEnvelope(env)
						if err == nil {
							handleMessage(env.SenderUrn, string(plaintext))
						}
					}
					if len(toAck) > 0 {
						_, _ = a.MQClient.Ack(ctx, *bootstrapInfo, toAck)
					}
				}
			}
		}

	case "contacts":
		list, _ := a.Contacts.List()
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

	case "trust":
		if len(os.Args) < 3 {
			fmt.Println("Usage: trust <urn>")
			os.Exit(1)
		}
		targetURN := os.Args[2]
		if bootstrapInfo == nil {
			fmt.Fprintln(os.Stderr, "❌ Error: 'trust' command requires a bootstrap platform address for resolution (use -b or BOOTSTRAP_ADDR).")
			os.Exit(1)
		}
		trustPeer(ctx, a, wotStore, bootstrapInfo, targetURN)

	case "untrust":
		if len(os.Args) < 3 {
			fmt.Println("Usage: untrust <urn>")
			os.Exit(1)
		}
		targetURN := os.Args[2]
		if err := a.Contacts.SetTrusted(targetURN, false); err != nil {
			fmt.Printf("❌ Error: %v\n", err)
		} else {
			fmt.Printf("✅ Untrusted: %s\n", targetURN)
		}

	case "claim":
		if len(os.Args) < 3 {
			fmt.Println("Usage: claim <subjectURN>")
			os.Exit(1)
		}
		subjectURN := os.Args[2]
		if bootstrapInfo == nil {
			fmt.Fprintln(os.Stderr, "❌ Error: 'claim' command requires a bootstrap platform address for resolution (use -b or BOOTSTRAP_ADDR).")
			os.Exit(1)
		}
		issueClaim(ctx, a, wotStore, bootstrapInfo, subjectURN)

	case "trustpath":
		if len(os.Args) < 3 {
			fmt.Println("Usage: trustpath <urn>")
			os.Exit(1)
		}
		targetURN := os.Args[2]
		checkTrustPath(ctx, wotResolver, targetURN)

	case "interactive":
		runInteractiveLoop(ctx, a, wotStore, wotResolver, bootstrapInfo)

	default:
		if cmd != "" {
			fmt.Printf("❌ Unknown command: %s\n\n", cmd)
		}
		printUsage()
	}
}

func printUsage() {
	fmt.Println("Usage: agent-comm [global flags] <command> [arguments]")
	fmt.Println()
	fmt.Println("Global Flags (Optional):")
	fmt.Println("  -b, --bootstrap <addr>         - Platform Bootstrap Multiaddress (e.g. /dns4/.../p2p/...)")
	fmt.Println("  -l, --local-addr <addr>        - Local HTTP server bind address (e.g. :8000 or localhost:8000)")
	fmt.Println("  --keysdir <path>               - Identity keys storage path")
	fmt.Println("  --dbpath <path>                - Main SQLite storage database path")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  share                          - Generate and show my contact card")
	fmt.Println("  import <file_or_text>          - Import contact card from file or raw text")
	fmt.Println("  send <recipientURN> <message>  - Send an encrypted message (checks WoT trust)")
	fmt.Println("  pull                           - Pull offline messages once and exit (requires bootstrap)")
	fmt.Println("  listen                         - Listen for direct connections and poll MQ continuously")
	fmt.Println("  contacts                       - List all contacts")
	fmt.Println("  trust <urn>                    - Mark a peer as directly trusted (requires bootstrap)")
	fmt.Println("  untrust <urn>                  - Remove trust status")
	fmt.Println("  claim <subjectURN>             - Issue a TRUSTED WoT claim about a peer (requires bootstrap)")
	fmt.Println("  trustpath <urn>                - Resolve Web of Trust path to URN")
	fmt.Println("  interactive                    - Start interactive menu shell")
	fmt.Println("  inbox                          - Check local daemon's memory inbox (loopback only)")
	fmt.Println("  reply <recipientURN> <message> - Send a reply through the local daemon (loopback only)")
	fmt.Println("  help                           - Show this help menu")
	fmt.Println()
	fmt.Println("Environment Variables (Optional):")
	fmt.Println("  BOOTSTRAP_ADDR                 - Platform Bootstrap Multiaddress")
	fmt.Println("  LOCAL_ADDR                     - Local HTTP server bind address")
	fmt.Println("  AGENT_KEYSDIR                  - Identity keys storage path")
	fmt.Println("  DB_PATH                        - Main SQLite storage database path")
	fmt.Println("  WOT_DB                         - Web of Trust database path")
}

// ---- Helpers ----

func pullOfflineMessages(ctx context.Context, a *agent.Agent, bootstrapNode peer.AddrInfo) {
	envs, err := a.MQClient.Retrieve(ctx, bootstrapNode, a.Keys.Ed25519.URN())
	if err != nil {
		fmt.Printf("❌ MQ retrieve error: %v\n", err)
		return
	}
	if len(envs) == 0 {
		fmt.Println("ℹ️ No pending messages.")
		return
	}

	fmt.Printf("📬 Retrieved %d message(s):\n", len(envs))
	var toAck []string
	for _, env := range envs {
		if env.MessageId != "" {
			toAck = append(toAck, env.MessageId)
		}
		plaintext, err := a.Session.DecryptEnvelope(env)
		if err != nil {
			fmt.Printf("  [decrypt] from %s FAILED: %v\n", env.SenderUrn, err)
			continue
		}
		var msg proto.ChatMessage
		if err := goproto.Unmarshal(plaintext, &msg); err != nil {
			fmt.Printf("  from %s: [raw] %q (parse error: %v)\n", env.SenderUrn, string(plaintext), err)
			continue
		}
		if txt := msg.GetText(); txt != nil {
			fmt.Printf("  💬 from %s: %q (ts=%d)\n", env.SenderUrn, txt.Text, txt.Timestamp)
		}
	}

	if len(toAck) > 0 {
		deleted, err := a.MQClient.Ack(ctx, bootstrapNode, toAck)
		if err != nil {
			fmt.Printf("❌ MQ ack error: %v\n", err)
		} else {
			fmt.Printf("✅ MQ acked %d message(s)\n", deleted)
		}
	}
}

func trustPeer(ctx context.Context, a *agent.Agent, wotStore *wot.Store, bootstrap *peer.AddrInfo, targetURN string) {
	if targetURN == a.Keys.Ed25519.URN() {
		fmt.Println("❌ Cannot trust yourself.")
		return
	}

	resolved, err := a.Registry.Resolve(*bootstrap, targetURN)
	if err != nil {
		fmt.Printf("❌ Resolve %s failed: %v\n", targetURN, err)
		return
	}

	if err := registry.VerifyResolveResult(targetURN, &resolved); err != nil {
		fmt.Printf("❌ Security alert: registry verification failed: %v\n", err)
		return
	}

	pubKey, err := resolved.ID.ExtractPublicKey()
	if err != nil {
		fmt.Printf("❌ Failed to extract public key: %v\n", err)
		return
	}
	edPubKey, _ := pubKey.Raw()

	wotStore.AddKnownPeer(targetURN, resolved.ID.String(), resolved.X25519PubKey, edPubKey)

	c := &contacts.Contact{
		URN:       targetURN,
		PeerID:    resolved.ID.String(),
		X25519PK:  resolved.X25519PubKey,
		Ed25519PK: edPubKey,
		Trusted:   true,
	}
	if err := a.Contacts.Add(c); err != nil {
		fmt.Printf("❌ Failed to add trusted contact: %v\n", err)
		return
	}
	fmt.Printf("✅ Marked %s as trusted.\n", targetURN)
}

func issueClaim(ctx context.Context, a *agent.Agent, wotStore *wot.Store, bootstrap *peer.AddrInfo, subjectURN string) {
	if subjectURN == a.Keys.Ed25519.URN() {
		fmt.Println("❌ Cannot issue claim about yourself.")
		return
	}

	resolved, err := a.Registry.Resolve(*bootstrap, subjectURN)
	if err != nil {
		fmt.Printf("❌ Resolve %s failed: %v\n", subjectURN, err)
		return
	}

	if err := registry.VerifyResolveResult(subjectURN, &resolved); err != nil {
		fmt.Printf("❌ Security alert: registry verification failed: %v\n", err)
		return
	}

	pubKey, err := resolved.ID.ExtractPublicKey()
	if err != nil {
		fmt.Printf("❌ Failed to extract public key: %v\n", err)
		return
	}
	edPubKey, _ := pubKey.Raw()

	wotStore.AddKnownPeer(subjectURN, resolved.ID.String(), resolved.X25519PubKey, edPubKey)

	claim, err := wot.NewDirectTrustClaim(a.Keys, subjectURN, resolved.ID.String(), resolved.X25519PubKey)
	if err != nil {
		fmt.Printf("❌ Create claim failed: %v\n", err)
		return
	}

	if err := wotStore.AddMyClaim(claim); err != nil {
		fmt.Printf("❌ Store claim failed: %v\n", err)
		return
	}
	fmt.Printf("✅ Issued WoT TRUSTED claim about %s\n", subjectURN)
}

func checkTrustPath(ctx context.Context, resolver *wot.Resolver, targetURN string) {
	path, err := resolver.FindTrustPath(ctx, targetURN)
	if err != nil {
		fmt.Printf("❌ No trust path to %s: %v\n", targetURN, err)
		return
	}

	fmt.Printf("✅ Found Trust Path to %s (depth=%d):\n", targetURN, path.Depth)
	for i, c := range path.Claims {
		fmt.Printf("  [%d] %s --TRUSTED--> %s\n", i, c.IssuerUrn, c.SubjectUrn)
	}
}

// ---- Original Interactive menu shell ----
func runInteractiveLoop(ctx context.Context, a *agent.Agent, wotStore *wot.Store, wotResolver *wot.Resolver, bootstrap *peer.AddrInfo) {
	fmt.Println("=== agent-comm Interactive Menu ===")
	fmt.Println("Type 'help' to see menu choices, 'quit' to exit.")
	fmt.Println()

	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("agent-comm> ")
		if !scanner.Scan() {
			break
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, " ", 3)
		cmd := parts[0]

		switch cmd {
		case "quit", "exit":
			return
		case "help":
			fmt.Println("Interactive Commands:")
			fmt.Println("  share                         - Generate and show my contact card")
			fmt.Println("  import                        - Paste and import a contact card")
			fmt.Println("  send <recipientURN> <msg>     - Send an encrypted message")
			fmt.Println("  pull                          - Pull offline messages once")
			fmt.Println("  contacts                      - List all contacts")
			fmt.Println("  trust <urn>                   - Trust a peer")
			fmt.Println("  untrust <urn>                 - Untrust a peer")
			fmt.Println("  claim <subjectURN>            - Issue trust claim about peer")
			fmt.Println("  trustpath <urn>               - Check Web of Trust path to peer")
			fmt.Println("  quit                          - Exit shell")
		case "share":
			card, _ := a.GenerateContactCard()
			fmt.Println(card)
		case "import":
			fmt.Println("Paste the card block. Type 'END' on a new line when done:")
			var lines []string
			for scanner.Scan() {
				t := scanner.Text()
				if strings.TrimSpace(t) == "END" {
					break
				}
				lines = append(lines, t)
			}
			cardText := strings.Join(lines, "\n")
			c, err := a.ImportContactCard(cardText, "")
			if err != nil {
				fmt.Printf("❌ Error: %v\n", err)
			} else {
				fmt.Printf("✅ Imported contact: %s\n", c.URN)
			}
		case "send":
			if len(parts) < 3 {
				fmt.Println("Usage: send <recipientURN> <message>")
				continue
			}
			recipientURN := parts[1]
			msgText := parts[2]
			err := a.SendMessage(ctx, recipientURN, msgText)
			if err != nil {
				fmt.Printf("❌ Failed to send: %v\n", err)
			} else {
				fmt.Println("✅ Message sent successfully.")
			}
		case "pull":
			if bootstrap == nil {
				fmt.Println("❌ Error: 'pull' requires a bootstrap platform address.")
				continue
			}
			pullOfflineMessages(ctx, a, *bootstrap)
		case "contacts":
			list, _ := a.Contacts.List()
			for _, c := range list {
				fmt.Printf("  %s (Trusted=%v, PeerID=%s)\n", c.URN, c.Trusted, c.PeerID)
			}
		case "trust":
			if len(parts) < 2 {
				fmt.Println("Usage: trust <urn>")
				continue
			}
			if bootstrap == nil {
				fmt.Println("❌ Error: 'trust' requires a bootstrap platform address for resolution.")
				continue
			}
			trustPeer(ctx, a, wotStore, bootstrap, parts[1])
		case "untrust":
			if len(parts) < 2 {
				fmt.Println("Usage: untrust <urn>")
				continue
			}
			_ = a.Contacts.SetTrusted(parts[1], false)
			fmt.Printf("✅ Untrusted: %s\n", parts[1])
		case "claim":
			if len(parts) < 2 {
				fmt.Println("Usage: claim <subjectURN>")
				continue
			}
			if bootstrap == nil {
				fmt.Println("❌ Error: 'claim' requires a bootstrap platform address for resolution.")
				continue
			}
			issueClaim(ctx, a, wotStore, bootstrap, parts[1])
		case "trustpath":
			if len(parts) < 2 {
				fmt.Println("Usage: trustpath <urn>")
				continue
			}
			checkTrustPath(ctx, wotResolver, parts[1])
		default:
			fmt.Printf("❌ Unknown command: %s\n", cmd)
		}
	}
}

// resolveBootstrapAddress dynamically resolves the PeerID of the platform node.
// It extracts the host name from the bootstrap address and queries its HTTP API for the PeerID.
func resolveBootstrapAddress(ctx context.Context, bootstrapAddr string) (string, error) {
	// Parse multiaddr to extract host/IP
	ma, err := multiaddr.NewMultiaddr(bootstrapAddr)
	if err != nil {
		return "", fmt.Errorf("failed to parse multiaddress: %w", err)
	}

	var host string
	multiaddr.ForEach(ma, func(c multiaddr.Component) bool {
		switch c.Protocol().Code {
		case multiaddr.P_DNS, multiaddr.P_DNS4, multiaddr.P_DNS6, multiaddr.P_DNSADDR:
			host = c.Value()
		case multiaddr.P_IP4, multiaddr.P_IP6:
			host = c.Value()
		}
		return true
	})

	if host == "" {
		return "", fmt.Errorf("could not extract host or IP from bootstrap address %q", bootstrapAddr)
	}

	// Try querying the platform REST API
	// 1. https://<host>/api/v1/bootstrap (production SSL)
	// 2. http://<host>/api/v1/bootstrap (production HTTP)
	// 3. http://<host>:8080/api/v1/bootstrap (default raw port)
	urls := []string{
		"https://" + host + "/api/v1/bootstrap",
		"http://" + host + "/api/v1/bootstrap",
		"http://" + host + ":8080/api/v1/bootstrap",
	}

	var resolvedPeerID string
	var lastErr error
	client := &http.Client{Timeout: 5 * time.Second}

	for _, url := range urls {
		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			lastErr = err
			continue
		}
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("HTTP status %d", resp.StatusCode)
			continue
		}

		var bRes BootstrapResponse
		if err := json.NewDecoder(resp.Body).Decode(&bRes); err != nil {
			lastErr = err
			continue
		}

		if bRes.PeerID != "" {
			resolvedPeerID = bRes.PeerID
			break
		}
	}

	if resolvedPeerID == "" {
		return "", fmt.Errorf("failed to resolve PeerID via HTTP APIs: %w", lastErr)
	}

	// Replace or append /p2p/ segment
	if idx := strings.Index(bootstrapAddr, "/p2p/"); idx != -1 {
		return bootstrapAddr[:idx] + "/p2p/" + resolvedPeerID, nil
	}
	if idx := strings.Index(bootstrapAddr, "/ipfs/"); idx != -1 {
		return bootstrapAddr[:idx] + "/p2p/" + resolvedPeerID, nil
	}
	return bootstrapAddr + "/p2p/" + resolvedPeerID, nil
}

type BootstrapResponse struct {
	PeerID string `json:"peer_id"`
}

// isDirWritable checks if a directory is writable by attempting to write and delete a temporary file.
func isDirWritable(dir string) bool {
	testFile := filepath.Join(dir, ".writable_test")
	err := os.WriteFile(testFile, []byte("test"), 0644)
	if err != nil {
		return false
	}
	_ = os.Remove(testFile)
	return true
}

func startLocalServer(bindAddr string, a *agent.Agent, inbox *Inbox) {
	mux := http.NewServeMux()

	// GET / or /health or /status
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		if r.URL.Path != "/" && r.URL.Path != "/status" && r.URL.Path != "/health" {
			http.NotFound(w, r)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		status := map[string]interface{}{
			"status":    "ok",
			"urn":       a.Keys.Ed25519.URN(),
			"peer_id":   a.Host.ID().String(),
			"timestamp": time.Now().Unix(),
		}
		_ = json.NewEncoder(w).Encode(status)
	})

	// GET /info
	mux.HandleFunc("/info", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		if r.Method != "GET" {
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}

		// Convert public keys to hex
		edPubKeyHex := fmt.Sprintf("%x", a.Keys.Ed25519.PublicKey)
		x25519PubKeyHex := fmt.Sprintf("%x", a.Keys.X25519PK)

		w.Header().Set("Content-Type", "application/json")
		info := map[string]interface{}{
			"urn":                a.Keys.Ed25519.URN(),
			"peer_id":            a.Host.ID().String(),
			"ed25519_public_key": edPubKeyHex,
			"x25519_public_key":  x25519PubKeyHex,
			"local_url":          fmt.Sprintf("http://localhost%s", getPortSuffix(bindAddr)),
		}
		_ = json.NewEncoder(w).Encode(info)
	})

	// POST /contacts
	mux.HandleFunc("/contacts", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		if r.Method != "POST" {
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}

		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			http.Error(w, "Forbidden: unable to verify remote address", http.StatusForbidden)
			return
		}

		if host != "127.0.0.1" && host != "::1" && host != "localhost" {
			http.Error(w, "Forbidden: requests must originate from loopback interfaces", http.StatusForbidden)
			return
		}

		type ContactReq struct {
			ContactURN       string `json:"contact_urn"`
			Alias            string `json:"alias"`
			TrustTier        string `json:"trust_tier"`
			Ed25519PublicKey string `json:"ed25519_public_key"`
			X25519PublicKey  string `json:"x25519_public_key"`
		}

		var req ContactReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Bad Request: "+err.Error(), http.StatusBadRequest)
			return
		}

		if req.ContactURN == "" || req.Ed25519PublicKey == "" || req.X25519PublicKey == "" {
			http.Error(w, "Bad Request: contact_urn, ed25519_public_key, and x25519_public_key are required", http.StatusBadRequest)
			return
		}

		if req.TrustTier == "self" {
			if host != "127.0.0.1" && host != "::1" && host != "localhost" {
				http.Error(w, "Forbidden: self trust tier additions restricted to localhost", http.StatusForbidden)
				return
			}
		}

		edBytes, err := hex.DecodeString(req.Ed25519PublicKey)
		if err != nil {
			http.Error(w, "Bad Request: invalid ed25519_public_key hex", http.StatusBadRequest)
			return
		}

		xBytes, err := hex.DecodeString(req.X25519PublicKey)
		if err != nil {
			http.Error(w, "Bad Request: invalid x25519_public_key hex", http.StatusBadRequest)
			return
		}

		libp2pPub, err := libp2pcrypto.UnmarshalEd25519PublicKey(edBytes)
		if err != nil {
			http.Error(w, "Bad Request: failed to unmarshal ed25519 public key: "+err.Error(), http.StatusBadRequest)
			return
		}

		pid, err := peer.IDFromPublicKey(libp2pPub)
		if err != nil {
			http.Error(w, "Bad Request: failed to derive PeerID: "+err.Error(), http.StatusBadRequest)
			return
		}

		c := &contacts.Contact{
			URN:         req.ContactURN,
			PeerID:      pid.String(),
			X25519PK:    xBytes,
			Ed25519PK:   edBytes,
			DisplayName: req.Alias,
			Trusted:     req.TrustTier == "self" || req.TrustTier == "family" || req.TrustTier == "friend",
			TrustTier:   req.TrustTier,
		}

		if err := a.Contacts.Add(c); err != nil {
			http.Error(w, "Internal Server Error: failed to save contact: "+err.Error(), http.StatusInternalServerError)
			return
		}

		a.Session.SetPeerX25519PK(pid, xBytes)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status":  "success",
			"message": "Contact registered successfully",
		})
	})

	// GET /messages/inbox — Host AI agent polls for incoming user messages.
	// Returns all queued messages and clears the queue. Loopback only.
	mux.HandleFunc("/messages/inbox", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		if r.Method != "GET" {
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}

		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			http.Error(w, "Forbidden: unable to verify remote address", http.StatusForbidden)
			return
		}
		if host != "127.0.0.1" && host != "::1" && host != "localhost" {
			http.Error(w, "Forbidden: requests must originate from loopback interfaces", http.StatusForbidden)
			return
		}

		msgs := inbox.Drain()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"messages": msgs,
		})
	})

	// POST /messages/send — Host AI agent sends a reply to a user.
	// The daemon handles encryption and routing via SendMessage. Loopback only.
	mux.HandleFunc("/messages/send", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		if r.Method != "POST" {
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}

		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			http.Error(w, "Forbidden: unable to verify remote address", http.StatusForbidden)
			return
		}
		if host != "127.0.0.1" && host != "::1" && host != "localhost" {
			http.Error(w, "Forbidden: requests must originate from loopback interfaces", http.StatusForbidden)
			return
		}

		type SendReq struct {
			RecipientURN string `json:"recipient_urn"`
			Content      string `json:"content"`
		}

		var req SendReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Bad Request: "+err.Error(), http.StatusBadRequest)
			return
		}

		if req.RecipientURN == "" || req.Content == "" {
			http.Error(w, "Bad Request: recipient_urn and content are required", http.StatusBadRequest)
			return
		}

		ctx := r.Context()
		err = a.SendMessage(ctx, req.RecipientURN, req.Content)
		if err != nil {
			fmt.Printf("❌ Failed to send message via HTTP API: %v\n", err)
			http.Error(w, "Failed to send message: "+err.Error(), http.StatusInternalServerError)
			return
		}

		fmt.Printf("✅ Message sent via HTTP API to %s\n", req.RecipientURN)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status":  "success",
			"message": "Message sent successfully",
		})
	})

	srv := &http.Server{
		Addr:    bindAddr,
		Handler: mux,
	}

	go func() {
		fmt.Printf("🌐 Starting Local Agent HTTP Server on %s...\n", bindAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Printf("⚠️ Local Agent HTTP Server failed to start on %s: %v\n", bindAddr, err)
		}
	}()
}

func getPortSuffix(addr string) string {
	idx := strings.LastIndex(addr, ":")
	if idx != -1 {
		return addr[idx:]
	}
	return ":8000"
}

func handleInboxCmd(localAddr string) {
	port := "8000"
	if localAddr != "" {
		if strings.Contains(localAddr, ":") {
			parts := strings.Split(localAddr, ":")
			port = parts[len(parts)-1]
		}
	}
	url := fmt.Sprintf("http://127.0.0.1:%s/messages/inbox", port)
	
	resp, err := http.Get(url)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Connection to agent-comm daemon failed: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "❌ Daemon returned error status: %s\n", resp.Status)
		os.Exit(1)
	}
	
	var data struct {
		Messages []InboxMessage `json:"messages"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		fmt.Fprintf(os.Stderr, "❌ Failed to decode daemon response: %v\n", err)
		os.Exit(1)
	}
	
	if data.Messages == nil {
		data.Messages = []InboxMessage{}
	}
	
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(data)
}

func handleReplyCmd(localAddr string) {
	if len(os.Args) < 4 {
		fmt.Fprintln(os.Stderr, "Usage: reply <recipientURN> <message>")
		os.Exit(1)
	}
	recipientURN := os.Args[2]
	messageText := os.Args[3]

	port := "8000"
	if localAddr != "" {
		if strings.Contains(localAddr, ":") {
			parts := strings.Split(localAddr, ":")
			port = parts[len(parts)-1]
		}
	}
	url := fmt.Sprintf("http://127.0.0.1:%s/messages/send", port)

	payload, err := json.Marshal(map[string]string{
		"recipient_urn": recipientURN,
		"content":       messageText,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Failed to marshal payload: %v\n", err)
		os.Exit(1)
	}

	resp, err := http.Post(url, "application/json", bytes.NewReader(payload))
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Connection to agent-comm daemon failed: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		fmt.Fprintf(os.Stderr, "❌ Daemon returned error status %s: %s\n", resp.Status, string(bodyBytes))
		os.Exit(1)
	}

	var resData map[string]interface{}
	_ = json.NewDecoder(resp.Body).Decode(&resData)
	
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(resData)
}