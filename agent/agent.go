package agent

import (
	"context"
	"fmt"
	"sync"

	"github.com/BillShiyaoZhang/agent-comm/contacts"
	"github.com/BillShiyaoZhang/agent-comm/crypto"
	
	p2p "github.com/BillShiyaoZhang/agent-comm/libp2p"
	"github.com/BillShiyaoZhang/agent-comm/mq"
	"github.com/BillShiyaoZhang/agent-comm/registry"
	"github.com/BillShiyaoZhang/agent-comm/dr"

	"github.com/BillShiyaoZhang/agent-comm/session"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/multiformats/go-multiaddr"
	"github.com/BillShiyaoZhang/agent-comm/dht"
	kad "github.com/libp2p/go-libp2p-kad-dht"
	"path/filepath"
)

// Agent is the high-level wrapper unifying the P2P networking, 
// identity management, and messaging protocol.
type Agent struct {
	Host           host.Host
	Keys           *crypto.IdentityKeys
	Session        *session.Manager
	Contacts       *contacts.Store
	
	MQClient       *mq.Client
	Registry       *registry.Client
	DHT            *kad.IpfsDHT
	BootstrapNodes []peer.AddrInfo
	DRStore        *dr.DRStore

	drPeers        map[string]*dr.DRSession
	drPeersMu      sync.RWMutex
}

// InitIdentity initializes or loads the Agent's cryptographic identity,
// spins up the P2P host, and prepares state managers.
func InitIdentity(ctx context.Context, cfg Config) (*Agent, error) {
	if cfg.KeysDir == "" {
		cfg.KeysDir = crypto.DefaultKeysDir()
	}
	if len(cfg.ListenAddrs) == 0 {
		// Use auto port assignment by default
		cfg.ListenAddrs = []string{"/ip4/0.0.0.0/tcp/0", "/ip4/0.0.0.0/udp/0/quic"}
	}

	if cfg.DBPath == "" {
		cfg.DBPath = filepath.Join(cfg.KeysDir, "agent_dr_store.db")
	}

	drStore, err := dr.NewDRStore(cfg.DBPath)
	if err != nil {
		return nil, fmt.Errorf("failed to mount DR store: %w", err)
	}

	contactStore, err := contacts.NewStore(cfg.DBPath)
	if err != nil {
		return nil, fmt.Errorf("failed to mount contacts store: %w", err)
	}

	keys, err := crypto.LoadOrCreateIdentity(cfg.KeysDir)
	if err != nil {
		return nil, fmt.Errorf("failed to load/create identity: %w", err)
	}

	p2pCfg := p2p.Config{
		ListenAddrs:  cfg.ListenAddrs,
		EnableRelay:  cfg.EnableRelay,
		PrivKeyBytes: keys.Ed25519.PrivateKey,
	}

	h, err := p2p.NewHost(p2pCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to start libp2p host: %w", err)
	}

	// Initialize DHT
	dhtCfg := dht.DHTConfig{Mode: dht.ModeClient, Bootstraps: cfg.BootstrapNodes}
	d, err := dht.NewDHT(ctx, h, dhtCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create DHT: %w", err)
	}
	dht.Bootstrap(ctx, d)

	a := &Agent{
		Host:           h,
		Keys:           keys,
		Session:        session.NewManager(h, keys),
		DRStore:        drStore,
		Contacts:       contactStore,
		MQClient:       mq.NewClient(h),
		Registry:       registry.NewClient(h),
		DHT:            d,
		BootstrapNodes: cfg.BootstrapNodes,
		drPeers:        make(map[string]*dr.DRSession),
	}

	// Try to register self with bootstrap registries
	urn := keys.Ed25519.URN()
	for _, node := range cfg.BootstrapNodes {
		go func(n peer.AddrInfo) {
			_ = a.Registry.Register(n, urn, h.Addrs(), keys.X25519PK)
		}(node)
	}

	return a, nil
}

// SendMessage executes the fallback routing logic:
// 1. Concurrent discovery via DHT and Registry
// 2. Direct/Relay connection attempt via Real-time Stream
// 3. Fallback to MQ blind-store via Double Ratchet / ECIES Envelope
func (a *Agent) SendMessage(ctx context.Context, recipientURN string, plaintext string) error {
	var targetID peer.ID
	var recipientPubKey []byte

	// 1. Concurrent Discovery (DHT vs Registry)
	fmt.Printf("[Agent] Starting concurrent discovery for %s...\n", recipientURN)
	
	type resolveRes struct {
		err  error
		res  *registry.ResolveResult
		src  string
	}
	resChan := make(chan resolveRes, len(a.BootstrapNodes))

	// Launch registry resolutions
	for _, n := range a.BootstrapNodes {
		go func(node peer.AddrInfo) {
			res, err := a.Registry.Resolve(node, recipientURN)
			resChan <- resolveRes{err: err, res: &res, src: "Registry"}
		}(n)
	}

	// Launch DHT lookup - DHT maps URN string to peerID (not natively supported by vanilla Kademlia out of box unless doing provider records, but assume our Registry is the main mapping).
	// NOTE: As per Phase 2, DHT discovery typically maps URN hash -> PeerID. We rely on Registry here quickly.
	
	// Wait for fastest successful registry result
	found := false
	var targetAddrs []multiaddr.Multiaddr
	for i := 0; i < len(a.BootstrapNodes); i++ {
		r := <-resChan
		if r.err == nil && r.res != nil && len(r.res.X25519PubKey) == 32 {
			fmt.Printf("[Agent] Discovery won by: %s\n", r.src)
			targetID = r.res.ID
			recipientPubKey = r.res.X25519PubKey
			targetAddrs = r.res.Addrs
			found = true
			break
		}
	}

	if !found {
		fmt.Printf("[Agent] Remote registry discovery failed for %s. Checking local contacts database...\n", recipientURN)
		contact, err := a.Contacts.Get(recipientURN)
		if err == nil && len(contact.X25519PK) == 32 {
			pid, err := peer.Decode(contact.PeerID)
			if err == nil {
				targetID = pid
				recipientPubKey = contact.X25519PK
				targetAddrs = a.Host.Peerstore().Addrs(targetID)
				found = true
				fmt.Printf("[Agent] Found contact details locally (PeerID: %s)\n", targetID)
			}
		}
	}

	if !found {
		return fmt.Errorf("failed to discover peer %s", recipientURN)
	}

	// Add resolved addresses to peerstore so host knows how to dial this peer
	if len(targetAddrs) > 0 {
		a.Host.Peerstore().AddAddrs(targetID, targetAddrs, peerstore.TempAddrTTL)
	}

	// Add to Session
	a.Session.SetPeerX25519PK(targetID, recipientPubKey)


	// 2. Fallback: Direct TCP/QUIC -> Relay -> MQ
	fmt.Printf("[Agent] Attempting Direct/Relay P2P stream to %s...\n", targetID)
	
	// Create Double Ratchet session
	drSession, err := dr.NewDRSessionInitiator(ctx, a.Session, a.Keys, targetID, recipientPubKey, recipientURN)
	if err != nil {
		return fmt.Errorf("failed to init DR session: %w", err)
	}

	// Try real-time stream direct delivery via Double Ratchet
	err = drSession.SendMessage(ctx, plaintext)
	if err == nil {
		fmt.Println("[Agent] Message delivered directly via DR realtime stream.")
		// Save advanced ratchet state to store
		// a.DRStore.SaveSession(recipientURN, targetID.String(), drSession.GetRatchetState()) 
		return nil
	}

	fmt.Printf("[Agent] Realtime DR stream failed (%v), falling back to offline MQ blind-store...\n", err)

	// 3. Fallback to MQ Store (Offline Envelope blind drop)
	// We fallback to standard envelope if DR offline envelope builder is not exposed yet.
	env, err := a.Session.BuildEnvelope(recipientPubKey, plaintext)
	if err != nil {
		return fmt.Errorf("failed to build encrypted envelope: %w", err)
	}

	// Drop the envelope to any available bootstrap node (Platform MQ)
	if len(a.BootstrapNodes) == 0 {
		return fmt.Errorf("no MQ nodes available for offline drop")
	}

	_, err = a.MQClient.Store(ctx, a.BootstrapNodes[0], recipientURN, env, 7)
	if err != nil {
		return fmt.Errorf("MQ blind-store failed: %w", err)
	}

	fmt.Println("[Agent] Message successfully blind-stored to Platform MQ.")
	return nil
}


// OnMessage registers the listener callback for incoming Realtime streams and polling MQ.
func (a *Agent) OnMessage(ctx context.Context, handler func(senderURN string, msg string)) {
	a.StartListening(ctx, handler)
}

// Close closes the Agent's resources (DRStore, Contacts database, DHT, Host).
func (a *Agent) Close() error {
	if a.DRStore != nil {
		a.DRStore.Close()
	}
	if a.Contacts != nil {
		a.Contacts.Close()
	}
	if a.DHT != nil {
		a.DHT.Close()
	}
	if a.Host != nil {
		a.Host.Close()
	}
	return nil
}
