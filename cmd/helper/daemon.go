package main

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/BillShiyaoZhang/agent-comm/agent"
	"github.com/BillShiyaoZhang/agent-comm/contacts"
	"github.com/BillShiyaoZhang/agent-comm/crypto"
	pb "github.com/BillShiyaoZhang/agent-comm/proto"
	libp2pcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
	goproto "google.golang.org/protobuf/proto"
)

type ClientChan chan struct{}

type DaemonServer struct {
	agent     *agent.Agent
	clients   map[ClientChan]bool
	clientsMu sync.Mutex
	mailbox   *mailbox
	transport durableTransport
	outgoing  chan struct{}
}

type ContactRequest struct {
	URN         string   `json:"urn"`
	PeerID      string   `json:"peer_id"`
	X25519PK    string   `json:"x25519_pk"`  // Hex encoded
	Ed25519PK   string   `json:"ed25519_pk"` // Hex encoded
	DisplayName string   `json:"display_name"`
	Trusted     bool     `json:"trusted"`
	Addrs       []string `json:"addrs"` // Optional addresses to inject into Peerstore

	// Mappings for agent-collaboration-web compatibility:
	ContactURN       string `json:"contact_urn"`
	Alias            string `json:"alias"`
	TrustTier        string `json:"trust_tier"`
	Ed25519PublicKey string `json:"ed25519_public_key"`
	X25519PublicKey  string `json:"x25519_public_key"`
}

func runDaemon() error {
	if len(os.Args) < 4 {
		return errors.New("usage: agent-comm-helper daemon <keys_dir> <platform_url> [local_port]")
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
	var bootstraps []peer.AddrInfo
	if err != nil {
		log.Printf("Bootstrap unavailable; starting with HTTPS messaging: %v", err)
	} else {
		bootstraps = []peer.AddrInfo{*bootstrapInfo}
		fmt.Printf("Platform Bootstrap PeerID: %s\n", bootstrapInfo.ID)
	}

	// 2. Initialize Agent
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	agentCfg := agent.Config{
		KeysDir:         keysDir,
		ListenAddrs:     []string{"/ip4/0.0.0.0/tcp/0", "/ip4/0.0.0.0/udp/0/quic-v1"},
		EnableRelay:     true,
		BootstrapNodes:  bootstraps,
		PlatformHTTPURL: platformURL,
	}

	log.Println("Initializing P2P Agent...")
	ag, err := agent.InitIdentity(ctx, agentCfg)
	if err != nil {
		return fmt.Errorf("initialize identity agent: %w", err)
	}
	defer ag.Close()

	log.Printf("Agent initialized. URN: %s, PeerID: %s\n", ag.Keys.Ed25519.URN(), ag.Host.ID())

	// 3. Set up Daemon HTTP Server
	mail, err := openMailbox(filepath.Join(ag.Keys.KeysDir, "mailbox.db"))
	if err != nil {
		return fmt.Errorf("open durable mailbox: %w", err)
	}
	defer mail.db.Close()
	ds := &DaemonServer{
		agent:     ag,
		clients:   make(map[ClientChan]bool),
		mailbox:   mail,
		transport: ag,
		outgoing:  make(chan struct{}, 1),
	}

	server := ds.httpServer(ctx, fmt.Sprintf("127.0.0.1:%d", localPort))
	// Reserve the API port before any mailbox worker can send or acknowledge.
	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		return fmt.Errorf("local HTTP API unavailable: %w", err)
	}
	defer listener.Close()
	// Cloud ACK follows the durable commit, independently of connector presence.
	incomingDone := ag.StartListeningDurable(ctx, ds.receiveMessage)
	workerDone := make(chan struct{})
	go func() { defer close(workerDone); ds.runOutbox(ctx) }()

	// Handle graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigChan)

	serveDone := make(chan error, 1)
	go func() {
		log.Printf("Local HTTP API listening on http://%s\n", listener.Addr())
		serveDone <- server.Serve(listener)
	}()

	var serveErr error
	select {
	case <-sigChan:
	case err := <-serveDone:
		if !errors.Is(err, http.ErrServerClosed) {
			serveErr = fmt.Errorf("HTTP server failed: %w", err)
		}
	}
	log.Println("Shutting down daemon...")
	cancel()

	shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutCancel()
	if err := server.Shutdown(shutCtx); err != nil {
		log.Printf("Closing outstanding HTTP requests: %v", err)
		_ = server.Close()
	}
	<-workerDone
	<-incomingDone
	log.Println("Daemon stopped cleanly.")
	return serveErr
}

func (ds *DaemonServer) httpServer(ctx context.Context, address string) *http.Server {
	return &http.Server{
		Addr: address, Handler: ds, ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout: 30 * time.Second,
		BaseContext: func(net.Listener) context.Context { return ctx },
	}
}

func (ds *DaemonServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// This plaintext API is local IPC. Do not expose it to arbitrary web origins.
	host := r.Host
	if parsed, _, err := net.SplitHostPort(host); err == nil {
		host = parsed
	}
	host = strings.Trim(host, "[]")
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		http.Error(w, "Local Host header required", http.StatusForbidden)
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || u.Host != r.Host || (u.Scheme != "http" && u.Scheme != "https") {
			http.Error(w, "Cross-origin access denied", http.StatusForbidden)
			return
		}
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxMessageBytes+8192)

	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	switch r.URL.Path {
	case "/info":
		ds.handleInfo(w, r)
	case "/api/v1/platform/register":
		ds.handleRegisterPlatform(w, r)
	case "/api/v1/presence":
		ds.handlePresence(w, r)
	case "/api/v1/mq/store":
		ds.handleStore(w, r)
	case "/api/v1/mq/subscribe":
		ds.handleSubscribe(w, r)
	case "/api/v1/mq/retrieve":
		ds.handleRetrieve(w, r)
	case "/api/v1/mq/ack":
		ds.handleAck(w, r)
	case "/api/v1/mq/status":
		ds.handleStatus(w, r)
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

func (ds *DaemonServer) handleRegisterPlatform(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if err := ds.agent.EnsurePlatformRegistration(ctx); err != nil {
		http.Error(w, "Local agent registration failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(map[string]interface{}{"registered": true, "urn": ds.agent.Keys.Ed25519.URN()})
}

func (ds *DaemonServer) handlePresence(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	urn := r.URL.Query().Get("urn")
	if !strings.HasPrefix(urn, "urn:") || len(urn) > 256 || strings.ContainsAny(urn, " \t\r\n") {
		http.Error(w, "Valid URN required", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(ds.agent.PeerPresence(ctx, urn))
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

	if !recipientURNPattern.MatchString(req.RecipientURN) || len(req.RecipientURN) > 256 {
		http.Error(w, "Invalid recipient_urn", http.StatusBadRequest)
		return
	}
	if req.MessageID == "" {
		req.MessageID = crypto.GenerateMessageID()
	}
	if !messageIDPattern.MatchString(req.MessageID) {
		http.Error(w, "Invalid message_id", http.StatusBadRequest)
		return
	}
	if err := req.MessageFields.validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	status, err := ds.mailbox.accept(req)
	if err != nil {
		code := http.StatusInternalServerError
		if errors.Is(err, errMessageConflict) {
			code = http.StatusConflict
		}
		http.Error(w, err.Error(), code)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]any{"success": true, "message_id": req.MessageID, "status": status})
	select {
	case ds.outgoing <- struct{}{}:
	default:
	}
}

func (ds *DaemonServer) handleSubscribe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch := make(ClientChan, 1)
	ds.clientsMu.Lock()
	ds.clients[ch] = true
	ds.clientsMu.Unlock()

	defer func() {
		ds.clientsMu.Lock()
		delete(ds.clients, ch)
		ds.clientsMu.Unlock()
	}()

	// Send initial connection verification SSE event
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(15 * time.Second))
	fmt.Fprintf(w, "data: {\"event\":\"connected\"}\n\n")
	flusher.Flush()
	if err := ds.replayInbox(w); err != nil {
		return
	}
	flusher.Flush()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ch:
		case <-ticker.C:
		case <-r.Context().Done():
			return
		}
		if err := ds.replayInbox(w); err != nil {
			return
		}
		if _, err := fmt.Fprint(w, ": heartbeat\n\n"); err != nil {
			return
		}
		flusher.Flush()
	}
}

func (ds *DaemonServer) broadcast() {
	ds.clientsMu.Lock()
	defer ds.clientsMu.Unlock()
	for ch := range ds.clients {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (ds *DaemonServer) replayInbox(w http.ResponseWriter) error {
	messages, err := ds.mailbox.pending()
	if err != nil {
		return err
	}
	controller := http.NewResponseController(w)
	_ = controller.SetWriteDeadline(time.Now().Add(15 * time.Second))
	for _, msg := range messages {
		data, err := json.Marshal(msg)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "id: %s\ndata: %s\n\n", msg.MessageID, data); err != nil {
			return err
		}
	}
	return nil
}

func (ds *DaemonServer) handleRetrieve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	messages, err := ds.mailbox.pending()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"messages": messages})
}

func (ds *DaemonServer) handleAck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		MessageIDs []string `json:"message_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.MessageIDs) == 0 || len(req.MessageIDs) > 1000 {
		http.Error(w, "Expected 1 to 1000 message_ids", http.StatusBadRequest)
		return
	}
	count, err := ds.mailbox.ack(req.MessageIDs)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"success": true, "acked": count})
}

func (ds *DaemonServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	status, err := ds.mailbox.outgoingStatus(r.URL.Query().Get("message_id"))
	if err != nil {
		code := http.StatusInternalServerError
		if errors.Is(err, sql.ErrNoRows) {
			code = http.StatusNotFound
		}
		http.Error(w, err.Error(), code)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

func (ds *DaemonServer) receiveMessage(env *pb.EncryptedEnvelope, payload []byte) error {
	text := string(payload)
	var chat pb.ChatMessage
	if err := goproto.Unmarshal(payload, &chat); err == nil && chat.GetText() != nil {
		text = chat.GetText().Text
	}
	fields := MessageFields{Text: text}
	var wire wireMessage
	if json.Unmarshal([]byte(text), &wire) == nil && wire.Version == 1 {
		fields = wire.MessageFields
	}
	if err := fields.validate(); err != nil {
		return err
	}
	if err := ds.mailbox.receive(InboxMessage{MessageID: env.MessageId, SenderURN: env.SenderUrn, MessageFields: fields}); err != nil {
		return err
	}
	ds.broadcast()
	return nil
}

func (ds *DaemonServer) runOutbox(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		processed, err := ds.deliverNext(ctx)
		if err != nil {
			log.Printf("Outbox: %v", err)
		}
		if processed && err == nil {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-ds.outgoing:
		}
	}
}

func (ds *DaemonServer) deliverNext(ctx context.Context) (bool, error) {
	entry, err := ds.mailbox.nextOutgoing()
	if err != nil || entry == nil {
		return false, err
	}
	req := entry.request
	if req.MessageFields.expired(time.Now()) {
		return true, ds.mailbox.updateOutgoing(req.MessageID, "expired", "deadline elapsed", entry.attempts)
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var env pb.EncryptedEnvelope
	if len(entry.envelope) > 0 {
		err = goproto.Unmarshal(entry.envelope, &env)
	} else {
		data, marshalErr := json.Marshal(wireMessage{Version: 1, MessageFields: req.MessageFields})
		err = marshalErr
		if err == nil {
			var prepared *pb.EncryptedEnvelope
			prepared, err = ds.transport.PrepareMessage(ctx, req.RecipientURN, string(data), req.MessageID)
			if err == nil {
				entry.envelope, err = goproto.Marshal(prepared)
				if err == nil {
					entry.envelope, err = ds.mailbox.saveEnvelope(req.MessageID, entry.envelope)
				}
				if err == nil {
					err = goproto.Unmarshal(entry.envelope, &env)
				}
			}
		}
	}
	if err == nil {
		if req.MessageFields.expired(time.Now()) {
			return true, ds.mailbox.updateOutgoing(req.MessageID, "expired", "deadline elapsed", entry.attempts)
		}
		err = ds.transport.DeliverEnvelope(ctx, &env)
	}
	if err != nil {
		if updateErr := ds.mailbox.updateOutgoing(req.MessageID, "accepted", err.Error(), entry.attempts+1); updateErr != nil {
			return true, updateErr
		}
		return true, err
	}
	return true, ds.mailbox.updateOutgoing(req.MessageID, "platform_queued", "", entry.attempts+1)
}

// resolvePlatformBootstrap contacts the public Platform's HTTP API to discover
// its current libp2p PeerID, then builds an AddrInfo suitable for dialing the
// Platform over QUIC (preferred) or TCP.
//
// The HTTP API is treated as the source of truth for the PeerID. When the API
// is unreachable we fall back to the well-known DefaultPlatformPeerID shipped
// with the helper. Both QUIC and TCP multiaddrs are constructed and exposed
// to libp2p so it can pick the best transport at dial time.
//
// In addition to the canonical /dns4/<host>/... and /ip4/<host>/... entries
// we opportunistically resolve the host via a DNSCache and inject any cached
// IPs as additional /ip4/<cached>/... addresses. This means DNS outages do not
// take the daemon down: libp2p simply falls back to the last-known IP.
func resolvePlatformBootstrap(platformURL string) (*peer.AddrInfo, error) {
	parsed, err := url.Parse(platformURL)
	if err != nil {
		return nil, err
	}

	host := parsed.Hostname()
	if host == "" {
		host = parsed.Path
	}

	// Try fetching bootstrap info from API.
	apiURL := platformURL
	if !strings.HasSuffix(apiURL, "/") {
		apiURL += "/"
	}
	apiURL += "api/v1/bootstrap"

	var peerIDStr string

	client := &http.Client{Timeout: 5 * time.Second}
	if resp, err := client.Get(apiURL); err == nil {
		if resp.StatusCode == http.StatusOK {
			var data struct {
				PeerID string `json:"peer_id"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&data); err == nil {
				peerIDStr = data.PeerID
			}
		}
		resp.Body.Close()
	}

	if peerIDStr == "" {
		// Fall back to the bundled default peer ID for the well-known public
		// platform. Hosts other than the bundled default do not get a magic
		// fallback because we cannot know their PeerID.
		if host == agent.DefaultPlatformDomain || host == "agent-communication.online" {
			peerIDStr = agent.DefaultPlatformPeerID
		} else {
			return nil, fmt.Errorf("failed to fetch bootstrap info from platform, and no fallback peer ID for host %s", host)
		}
	}

	port := agent.DefaultPlatformP2PPort

	// Build candidate multiaddrs. We always include a /dns4/ entry so libp2p
	// can re-resolve on its own; we additionally include /ip4/<ip>/ entries
	// resolved via the DNSCache to act as a fallback if DNS is unreachable.
	var candidates []multiaddr.Multiaddr

	appendCandidates := func(hostOrIP string, isIPLiteral bool) {
		// QUIC is the preferred transport; TCP is the safety net.
		quicTemplate := "/%s/%s/udp/%d/quic-v1/p2p/%s"
		tcpTemplate := "/%s/%s/tcp/%d/p2p/%s"
		proto := "dns4"
		if isIPLiteral {
			proto = "ip4"
		}

		if ma, err := multiaddr.NewMultiaddr(fmt.Sprintf(quicTemplate, proto, hostOrIP, port, peerIDStr)); err == nil {
			candidates = append(candidates, ma)
		}
		if ma, err := multiaddr.NewMultiaddr(fmt.Sprintf(tcpTemplate, proto, hostOrIP, port, peerIDStr)); err == nil {
			candidates = append(candidates, ma)
		}
	}

	if !isIPLiteral(host) {
		// Always expose the DNS form first.
		appendCandidates(host, false)

		// Try to also inject cached IPs. Failures here are non-fatal; libp2p
		// will still be able to use the /dns4/ entry on its own.
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if cache, cacheErr := agent.NewDNSCache(""); cacheErr == nil {
			if ips, lookupErr := cache.LookupHost(ctx, host); lookupErr == nil {
				for _, ip := range ips {
					appendCandidates(ip, true)
				}
			}
		}
	} else {
		appendCandidates(host, true)
	}

	if len(candidates) == 0 {
		return nil, fmt.Errorf("failed to construct any bootstrap multiaddr for host %s", host)
	}

	pid, err := peer.Decode(peerIDStr)
	if err != nil {
		return nil, fmt.Errorf("invalid peer id from bootstrap: %w", err)
	}
	return &peer.AddrInfo{ID: pid, Addrs: candidates}, nil
}

// isIPLiteral reports whether host is a bare IPv4 or IPv6 address with no
// DNS lookup required. Bracketed IPv6 forms (e.g. "[::1]") are also accepted.
func isIPLiteral(host string) bool {
	if host == "" {
		return false
	}
	if h := strings.TrimPrefix(strings.TrimSuffix(host, "]"), "["); h != host {
		host = h
	}
	if ip := net.ParseIP(host); ip != nil {
		return true
	}
	// Heuristic: anything that contains only digits and dots is treated as
	// an IPv4 literal even if it cannot be parsed by net.ParseIP (e.g. with
	// a leading zero). This mirrors the behaviour of libp2p's multiaddr
	// /ip4/ parser.
	allDigitsAndDots := true
	for _, c := range host {
		if (c < '0' || c > '9') && c != '.' {
			allDigitsAndDots = false
			break
		}
	}
	return allDigitsAndDots && strings.Contains(host, ".")
}
