// Bootstrap node: runs as a DHT server to serve as entry point for the agent network.
// This is the main entry point for running a bootstrap/registry node.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/BillShiyaoZhang/agent-comm/crypto"
	"github.com/BillShiyaoZhang/agent-comm/dht"
	"github.com/BillShiyaoZhang/agent-comm/libp2p"
	"github.com/BillShiyaoZhang/agent-comm/mq"
	"github.com/BillShiyaoZhang/agent-comm/registry"
	"github.com/BillShiyaoZhang/agent-comm/wot"
)

func main() {
	fmt.Println("=== agent-comm Bootstrap Node ===")
	fmt.Println("This node serves as a DHT entry point and URN registry for other agents.")
	fmt.Println()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Step 1: Load or create persistent identity
	keysDir := os.Getenv("AGENT_KEYSDIR")
	keys, err := crypto.LoadOrCreateIdentity(keysDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load identity: %v\n", err)
		os.Exit(1)
	}

	peerID, err := keys.PeerID()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to derive PeerID: %v\n", err)
		os.Exit(1)
	}
	urn := keys.Ed25519.URN()

	fmt.Printf("Identity:\n")
	fmt.Printf("  URN    : %s\n", urn)
	fmt.Printf("  PeerID : %s\n", peerID)
	fmt.Printf("  Keys   : %s/\n", keys.KeysDir)
	fmt.Println()

	// Step 2: Create libp2p host with persistent identity
	cfg := libp2p.Config{
		ListenAddrs: []string{
			"/ip4/0.0.0.0/tcp/45041",
			"/ip4/0.0.0.0/udp/0/quic",
		},
		EnableRelay:  true,
		PrivKeyBytes: keys.Ed25519.PrivateKey,
	}

	h, err := libp2p.NewHost(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create host: %v\n", err)
		os.Exit(1)
	}
	defer h.Close()

	fmt.Printf("Host created:\n")
	fmt.Printf("  PeerID : %s\n", h.ID())
	fmt.Printf("  Addrs  : %v\n", h.Addrs())
	fmt.Println()

	if h.ID().String() != peerID {
		fmt.Printf("Warning: PeerID mismatch! Expected %s, got %s\n", peerID, h.ID().String())
	}

	// Step 3: Start DHT in server mode
	fmt.Println("Starting DHT (server mode)...")
	dhtCfg := dht.DHTConfig{Mode: dht.ModeServer}
	d, err := dht.NewDHT(ctx, h, dhtCfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create DHT: %v\n", err)
		os.Exit(1)
	}
	defer d.Close()

	fmt.Println("Bootstrapping DHT...")
	if err := dht.Bootstrap(ctx, d); err != nil {
		fmt.Fprintf(os.Stderr, "DHT bootstrap warning: %v (non-fatal)\n", err)
	}
	fmt.Printf("DHT ready\n")
	fmt.Printf("  Routing table size: %d\n", dht.GetRoutingTableSize(d))
	fmt.Println()

	// Step 4: Start URN registry server
	regServer := registry.NewServer(h, registry.NewInMemoryStore())
	regServer.Register()
	regServer.HandleRegister(urn, h.ID(), h.Addrs(), keys.X25519PK)
	fmt.Printf("Registry server started on %s\n", registry.ProtoID)
	fmt.Printf("Registered self: %s -> %s\n", urn, h.ID())
	fmt.Println()

	// Step 5: Start MQ relay server (async message storage). We default to a
// platform-appropriate data directory ($XDG_DATA_HOME/agent-comm on Linux,
// %LOCALAPPDATA%\agent-comm on Windows, ~/Library/Application Support/agent-comm
// on macOS) instead of /tmp so the data survives restarts. The path can
// always be overridden via the MQ_DB_PATH environment variable.
	dbPath := os.Getenv("MQ_DB_PATH")
	if dbPath == "" {
		dbPath = defaultDataPath("relay_mq.db")
	}
	sqliteStore, err := mq.NewSQLiteStore(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create MQ sqlite store: %v\n", err)
		os.Exit(1)
	}
	mqServer, err := mq.NewServer(h, sqliteStore)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create MQ server: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("MQ relay server started on %s\n", mq.ProtoID)
	fmt.Printf("  DB: %s\n", dbPath)
	fmt.Println()

	// Step 6: Start WoT store and register handler. Same defaulting strategy as
// the MQ DB above; override via WOT_DB_PATH.
	wotDBPath := os.Getenv("WOT_DB_PATH")
	if wotDBPath == "" {
		wotDBPath = defaultDataPath("relay_wot.db")
	}
	wotStore, err := wot.NewStore(wotDBPath, keys)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create WoT store: %v\n", err)
		os.Exit(1)
	}
	// Also register our own keys so we can verify self-signed claims
	wotStore.AddKnownPeer(urn, h.ID().String(), keys.X25519PK, keys.Ed25519.PublicKey)
	wot.RegisterWOTHandler(h, wotStore)
	fmt.Printf("WoT server started on %s\n", wot.WoTProtoID)
	fmt.Printf("  DB: %s\n", wotDBPath)
	fmt.Println()

	// Start a goroutine to periodically print registry state
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				urns := regServer.ListURNs()
				if len(urns) > 0 {
					fmt.Printf("[registry] %d registered URN(s): %v\n", len(urns), urns)
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	// Print connection info
	fmt.Println("=== BOOTSTRAP INFO ===")
	fmt.Println("Other agents can connect using:")
	for _, addr := range h.Addrs() {
		fullAddr := fmt.Sprintf("%s/p2p/%s", addr.String(), h.ID().String())
		fmt.Printf("  %s\n", fullAddr)
	}
	fmt.Println("======================")
	fmt.Println()

	// Wait for shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	fmt.Println("Bootstrap node running. Press Ctrl+C to stop.")
	<-sigChan

	fmt.Println("\nShutting down...")
	cancel()
	wg.Wait()
	mqServer.Close()
	wotStore.Close()
	fmt.Println("Shutdown clean")
}

// defaultDataPath returns an OS-appropriate path under the agent-comm data
// directory for the given file name. We prefer os.UserConfigDir() (which
// resolves to ~/Library/Application Support, ~/.config, or %LOCALAPPDATA%
// depending on platform) and fall back to os.TempDir() if even that fails.
//
// Using a real data directory instead of /tmp ensures the bootstrap node's
// SQLite state survives reboots, which is critical for any production
// deployment.
func defaultDataPath(fileName string) string {
	base, err := os.UserConfigDir()
	if err != nil || base == "" {
		base = os.TempDir()
	}
	return filepath.Join(base, "agent-comm", fileName)
}