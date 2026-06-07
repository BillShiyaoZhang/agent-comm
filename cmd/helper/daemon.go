package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/BillShiyaoZhang/agent-comm/agent"
	"github.com/BillShiyaoZhang/agent-comm/contacts"
	pb "github.com/BillShiyaoZhang/agent-comm/proto"
	libp2pcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
	goproto "google.golang.org/protobuf/proto"
)

type ClientChan chan string

type DaemonServer struct {
	agent     *agent.Agent
	clients   map[ClientChan]bool
	clientsMu sync.Mutex
}

type StoreRequest struct {
	RecipientURN string `json:"recipient_urn"`
	Text         string `json:"text"`
}

type ContactRequest struct {
	URN         string   `json:"urn"`
	PeerID      string   `json:"peer_id"`
	X25519PK    string   `json:"x25519_pk"`  // Hex encoded
	Ed25519PK   string   `json:"ed25519_pk"` // Hex encoded
	DisplayName string   `json:"display_name"`
	Trusted     bool     `json:"trusted"`
	Addrs       []string `json:"addrs"`      // Optional addresses to inject into Peerstore

	// Mappings for agent-collaboration-web compatibility:
	ContactURN       string `json:"contact_urn"`
	Alias            string `json:"alias"`
	TrustTier        string `json:"trust_tier"`
	Ed25519PublicKey string `json:"ed25519_public_key"`
	X25519PublicKey  string `json:"x25519_public_key"`
}

func runDaemon() {
	if len(os.Args) < 4 {
		fmt.Println(`Usage: agent-comm-helper daemon <keys_dir> <platform_url> [local_port]`)
		os.Exit(1)
	}

	keysDir := os.Args[2]
	platformURL := os.Args[3]

	localPort := 45042
	if len(os.Args) >= 5 {
		if p, err := strconv.Atoi(os.Args[4]); err == nil {
			localPort = p
		}
	}

	fmt.Printf("=== Starting agent-comm daemon ===\n")
	fmt.Printf("Keys Directory: %s\n", keysDir)
	fmt.Printf("Platform URL  : %s\n", platformURL)
	fmt.Printf("Local Port    : %d\n", localPort)

	// 1. Resolve Platform Bootstrap Node
	bootstrapInfo, err := resolvePlatformBootstrap(platformURL)
	if err != nil {
		log.Fatalf("Failed to resolve platform bootstrap node: %v", err)
	}
	fmt.Printf("Platform Bootstrap PeerID: %s\n", bootstrapInfo.ID)
	fmt.Printf("Platform Bootstrap Addrs : %v\n", bootstrapInfo.Addrs)

	// 2. Initialize Agent
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	agentCfg := agent.Config{
		KeysDir:        keysDir,
		ListenAddrs:    []string{"/ip4/0.0.0.0/tcp/0", "/ip4/0.0.0.0/udp/0/quic-v1"},
		EnableRelay:    true,
		BootstrapNodes: []peer.AddrInfo{*bootstrapInfo},
	}

	log.Println("Initializing P2P Agent...")
	ag, err := agent.InitIdentity(ctx, agentCfg)
	if err != nil {
		log.Fatalf("Failed to initialize identity agent: %v", err)
	}
	defer ag.Close()

	log.Printf("Agent initialized. URN: %s, PeerID: %s\n", ag.Keys.Ed25519.URN(), ag.Host.ID())

	// 3. Set up Daemon HTTP Server
	ds := &DaemonServer{
		agent:   ag,
		clients: make(map[ClientChan]bool),
	}

	// Register OnMessage callback to broadcast decrypted messages to SSE subscribers
	ag.OnMessage(ctx, func(senderURN string, rawPayload string) {
		var chatMsg pb.ChatMessage
		err := goproto.Unmarshal([]byte(rawPayload), &chatMsg)
		var text string
		if err == nil {
			if txt := chatMsg.GetText(); txt != nil {
				text = txt.Text
			} else {
				text = rawPayload
			}
		} else {
			text = rawPayload
		}

		log.Printf("Received message from %s: %s\n", senderURN, text)
		msgMap := map[string]string{
			"sender_urn": senderURN,
			"text":       text,
		}
		jsonBytes, _ := json.Marshal(msgMap)
		ds.broadcast(string(jsonBytes))
	})

	server := &http.Server{
		Addr:    fmt.Sprintf("127.0.0.1:%d", localPort),
		Handler: ds,
	}

	// Handle graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		log.Printf("Local HTTP API listening on http://127.0.0.1:%d\n", localPort)
		if err := server.ListenAndServe(); err != http.ErrServerClosed {
			log.Fatalf("HTTP server failed: %v", err)
		}
	}()

	<-sigChan
	log.Println("Shutting down daemon...")
	cancel()

	shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutCancel()
	server.Shutdown(shutCtx)
	log.Println("Daemon stopped cleanly.")
}

func (ds *DaemonServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// CORS Headers
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")

	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	switch r.URL.Path {
	case "/info":
		ds.handleInfo(w, r)
	case "/api/v1/mq/store":
		ds.handleStore(w, r)
	case "/api/v1/mq/subscribe":
		ds.handleSubscribe(w, r)
	case "/api/v1/contacts":
		ds.handleAddContact(w, r)
	default:
		// Also support paths without /api/v1/mq context in case connectors pass them directly
		if strings.HasSuffix(r.URL.Path, "/store") {
			ds.handleStore(w, r)
		} else if strings.HasSuffix(r.URL.Path, "/subscribe") {
			ds.handleSubscribe(w, r)
		} else if strings.HasSuffix(r.URL.Path, "/contacts") {
			ds.handleAddContact(w, r)
		} else {
			http.NotFound(w, r)
		}
	}
}

func (ds *DaemonServer) handleInfo(w http.ResponseWriter, r *http.Request) {
	var addrs []string
	for _, a := range ds.agent.Host.Addrs() {
		addrs = append(addrs, a.String())
	}
	info := map[string]interface{}{
		"urn":     ds.agent.Keys.Ed25519.URN(),
		"peer_id": ds.agent.Host.ID().String(),
		"addrs":   addrs,
		"status":  "running",
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(info)
}

func (ds *DaemonServer) handleAddContact(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req ContactRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	urn := req.URN
	if urn == "" {
		urn = req.ContactURN
	}
	displayName := req.DisplayName
	if displayName == "" {
		displayName = req.Alias
	}
	x25519PKHex := req.X25519PK
	if x25519PKHex == "" {
		x25519PKHex = req.X25519PublicKey
	}
	ed25519PKHex := req.Ed25519PK
	if ed25519PKHex == "" {
		ed25519PKHex = req.Ed25519PublicKey
	}
	trusted := req.Trusted
	if !trusted && req.TrustTier != "" {
		trusted = req.TrustTier == "self" || req.TrustTier == "family" || req.TrustTier == "friend"
	}

	if urn == "" || x25519PKHex == "" {
		http.Error(w, "Missing URN or X25519 Public Key", http.StatusBadRequest)
		return
	}

	x25519Bytes, err := hex.DecodeString(x25519PKHex)
	if err != nil {
		http.Error(w, "Invalid x25519_pk hex", http.StatusBadRequest)
		return
	}

	var ed25519Bytes []byte
	if ed25519PKHex != "" {
		ed25519Bytes, err = hex.DecodeString(ed25519PKHex)
		if err != nil {
			http.Error(w, "Invalid ed25519_pk hex", http.StatusBadRequest)
			return
		}
	}

	peerIDStr := req.PeerID
	if peerIDStr == "" && len(ed25519Bytes) > 0 {
		libp2pPub, err := libp2pcrypto.UnmarshalEd25519PublicKey(ed25519Bytes)
		if err != nil {
			http.Error(w, "Failed to unmarshal ed25519 public key: "+err.Error(), http.StatusBadRequest)
			return
		}
		pid, err := peer.IDFromPublicKey(libp2pPub)
		if err != nil {
			http.Error(w, "Failed to derive PeerID: "+err.Error(), http.StatusBadRequest)
			return
		}
		peerIDStr = pid.String()
	}

	if peerIDStr == "" {
		http.Error(w, "Missing peer_id and unable to derive it without ed25519_public_key", http.StatusBadRequest)
		return
	}

	c := &contacts.Contact{
		URN:         urn,
		PeerID:      peerIDStr,
		X25519PK:    x25519Bytes,
		Ed25519PK:   ed25519Bytes,
		DisplayName: displayName,
		Trusted:     trusted,
		TrustTier:   req.TrustTier,
	}

	log.Printf("Adding contact: URN=%s, PeerID=%s\n", urn, peerIDStr)
	if err := ds.agent.Contacts.Add(c); err != nil {
		log.Printf("Failed to add contact: %v\n", err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	// Inject addresses into Peerstore if provided
	if len(req.Addrs) > 0 {
		var peerAddrs []multiaddr.Multiaddr
		for _, addrStr := range req.Addrs {
			ma, err := multiaddr.NewMultiaddr(addrStr)
			if err == nil {
				peerAddrs = append(peerAddrs, ma)
			} else {
				log.Printf("Failed to parse multiaddr '%s': %v\n", addrStr, err)
			}
		}
		if len(peerAddrs) > 0 {
			peerID, err := peer.Decode(peerIDStr)
			if err == nil {
				ds.agent.Host.Peerstore().AddAddrs(peerID, peerAddrs, 24*time.Hour)
				log.Printf("Injected peerstore addrs for %s: %v\n", peerIDStr, peerAddrs)
			} else {
				log.Printf("Failed to decode peer ID '%s': %v\n", peerIDStr, err)
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"success": true})
}

func (ds *DaemonServer) handleStore(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req StoreRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	if req.RecipientURN == "" || req.Text == "" {
		http.Error(w, "Missing recipient_urn or text", http.StatusBadRequest)
		return
	}

	log.Printf("Outgoing message to %s\n", req.RecipientURN)

	// Send message using the Go agent
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	err := ds.agent.SendMessage(ctx, req.RecipientURN, req.Text)
	if err != nil {
		log.Printf("SendMessage error: %v\n", err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"success": true})
}

func (ds *DaemonServer) handleSubscribe(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch := make(ClientChan, 10)
	ds.clientsMu.Lock()
	ds.clients[ch] = true
	ds.clientsMu.Unlock()

	defer func() {
		ds.clientsMu.Lock()
		delete(ds.clients, ch)
		ds.clientsMu.Unlock()
		close(ch)
	}()

	// Send initial connection verification SSE event
	fmt.Fprintf(w, "data: {\"event\":\"connected\"}\n\n")
	flusher.Flush()

	for {
		select {
		case msg := <-ch:
			fmt.Fprintf(w, "data: %s\n\n", msg)
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func (ds *DaemonServer) broadcast(msg string) {
	ds.clientsMu.Lock()
	defer ds.clientsMu.Unlock()
	for ch := range ds.clients {
		select {
		case ch <- msg:
		default:
			// Client channel full, skip
		}
	}
}

func resolvePlatformBootstrap(platformURL string) (*peer.AddrInfo, error) {
	parsed, err := url.Parse(platformURL)
	if err != nil {
		return nil, err
	}

	host := parsed.Hostname()
	if host == "" {
		host = parsed.Path
	}

	// Try fetching bootstrap info from API
	apiURL := platformURL
	if !strings.HasSuffix(apiURL, "/") {
		apiURL += "/"
	}
	apiURL += "api/v1/bootstrap"

	var peerIDStr string

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(apiURL)
	if err == nil && resp.StatusCode == 200 {
		var data struct {
			PeerID string `json:"peer_id"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&data); err == nil {
			peerIDStr = data.PeerID
		}
		resp.Body.Close()
	}

	if peerIDStr == "" {
		// Fallback for public platform
		if host == "8.130.40.38" {
			peerIDStr = "12D3KooWKjNBA3pgLKryRytwHpJ9dPQo9H3gvCKUekktYtXQXfib"
		} else {
			return nil, fmt.Errorf("failed to fetch bootstrap info from platform, and no fallback peer ID for host %s", host)
		}
	}

	// Construct multiaddr
	var maddrStr string
	// Check if host is IP
	isIP := true
	for _, char := range host {
		if (char < '0' || char > '9') && char != '.' && char != ':' {
			isIP = false
			break
		}
	}

	if isIP {
		maddrStr = fmt.Sprintf("/ip4/%s/udp/45041/quic-v1/p2p/%s", host, peerIDStr)
	} else {
		maddrStr = fmt.Sprintf("/dns4/%s/udp/45041/quic-v1/p2p/%s", host, peerIDStr)
	}

	maddr, err := multiaddr.NewMultiaddr(maddrStr)
	if err != nil {
		// Try TCP fallback
		if isIP {
			maddrStr = fmt.Sprintf("/ip4/%s/tcp/45041/p2p/%s", host, peerIDStr)
		} else {
			maddrStr = fmt.Sprintf("/dns4/%s/tcp/45041/p2p/%s", host, peerIDStr)
		}
		maddr, err = multiaddr.NewMultiaddr(maddrStr)
		if err != nil {
			return nil, err
		}
	}

	return peer.AddrInfoFromP2pAddr(maddr)
}
