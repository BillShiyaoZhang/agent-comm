package agent

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
	"github.com/BillShiyaoZhang/agent-comm/contacts"
	"github.com/BillShiyaoZhang/agent-comm/crypto"
	p2phost "github.com/BillShiyaoZhang/agent-comm/libp2p"
	"github.com/BillShiyaoZhang/agent-comm/session"
)

func TestContactCardGenerateParseImport(t *testing.T) {

	// 1. Generate keys
	tempKeysDir := t.TempDir()
	keys, err := crypto.LoadOrCreateIdentity(tempKeysDir)
	if err != nil {
		t.Fatalf("failed to create identity keys: %v", err)
	}

	// 2. Start a mock/real libp2p host
	p2pCfg := p2phost.Config{
		ListenAddrs: []string{"/ip4/127.0.0.1/tcp/0"},
	}
	h, err := p2phost.NewHost(p2pCfg)
	if err != nil {
		t.Fatalf("failed to create libp2p host: %v", err)
	}
	defer h.Close()

	// Dummy bootstrap node
	dummyPid, _ := peer.Decode("12D3KooWRsYuopRwdiyNLhiTrxY1innpSRCCkAygdoMqeVyn2x8f")
	dummyAddr, _ := multiaddr.NewMultiaddr("/dns4/agent-communication.online/tcp/45041")
	dummyBootstrap := []peer.AddrInfo{
		{
			ID:    dummyPid,
			Addrs: []multiaddr.Multiaddr{dummyAddr},
		},
	}

	// 3. Generate Contact Card
	card, err := GenerateContactCard(keys, h, dummyBootstrap)
	if err != nil {
		t.Fatalf("failed to generate contact card: %v", err)
	}

	if !stringsContains(card, "-----BEGIN AGENT-COMM CONTACT CARD-----") {
		t.Errorf("expected card to contain BEGIN header, got: %s", card)
	}
	if !stringsContains(card, "Ed25519PK:") {
		t.Errorf("expected card to contain Ed25519PK, got: %s", card)
	}
	if !stringsContains(card, "X25519PK:") {
		t.Errorf("expected card to contain X25519PK, got: %s", card)
	}
	if !stringsContains(card, "Addrs:") {
		t.Errorf("expected card to contain Addrs, got: %s", card)
	}
	if !stringsContains(card, "Bootstrap:") {
		t.Errorf("expected card to contain Bootstrap, got: %s", card)
	}

	// 4. Parse Contact Card
	info, err := ParseContactCard(card)
	if err != nil {
		t.Fatalf("failed to parse contact card: %v", err)
	}

	// Validate parsed fields
	expectedURN := keys.Ed25519.URN()
	derivedURN := URNFromEd25519PK(info.Ed25519PK)
	if derivedURN != expectedURN {
		t.Errorf("expected derived URN %s, got %s", expectedURN, derivedURN)
	}

	expectedPeerID, _ := keys.PeerID()
	derivedPeerID, _ := PeerIDFromEd25519PK(info.Ed25519PK)
	if derivedPeerID.String() != expectedPeerID {
		t.Errorf("expected derived PeerID %s, got %s", expectedPeerID, derivedPeerID)
	}

	if len(info.X25519PK) != 32 {
		t.Errorf("expected X25519PK length 32, got %d", len(info.X25519PK))
	}

	if len(info.Addrs) == 0 {
		t.Errorf("expected at least one address, got 0")
	}

	if len(info.BootstrapAddrs) != 1 || info.BootstrapAddrs[0].ID != dummyPid {
		t.Errorf("expected bootstrap node to match dummy bootstrap")
	}

	// 5. Import Contact Card
	// Setup a separate temporary contacts store for importer
	tempDBPath := filepath.Join(t.TempDir(), "contacts_test.db")
	store, err := contacts.NewStore(tempDBPath)
	if err != nil {
		t.Fatalf("failed to create contacts store: %v", err)
	}
	defer store.Close()

	// Separate host for importing (receiver)
	receiverHost, err := p2phost.NewHost(p2pCfg)
	if err != nil {
		t.Fatalf("failed to create receiver host: %v", err)
	}
	defer receiverHost.Close()

	receiverKeys, _ := crypto.LoadOrCreateIdentity(t.TempDir())
	receiverMgr := session.NewManager(receiverHost, receiverKeys)

	// Attempt to import the card
	importedContact, err := ImportContactCard(receiverHost, receiverMgr, store, card, "Test Sender")
	if err != nil {
		t.Fatalf("failed to import contact card: %v", err)
	}

	if importedContact.URN != expectedURN {
		t.Errorf("expected imported contact URN %s, got %s", expectedURN, importedContact.URN)
	}
	if importedContact.DisplayName != "Test Sender" {
		t.Errorf("expected display name 'Test Sender', got: %s", importedContact.DisplayName)
	}
	if !importedContact.Trusted {
		t.Errorf("expected imported contact to be trusted")
	}

	// Verify it was persisted to SQLite db
	dbContact, err := store.Get(expectedURN)
	if err != nil {
		t.Fatalf("failed to retrieve contact from db: %v", err)
	}
	if dbContact.PeerID != expectedPeerID {
		t.Errorf("expected database contact PeerID %s, got %s", expectedPeerID, dbContact.PeerID)
	}

	// Verify addresses were added to receiver's peerstore
	peerID, _ := peer.Decode(expectedPeerID)
	peerStoreAddrs := receiverHost.Peerstore().Addrs(peerID)
	if len(peerStoreAddrs) == 0 {
		t.Errorf("expected peerstore to contain addresses for imported peer")
	}
}

func stringsContains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 || (len(s) > 0 && strings.Index(s, substr) >= 0))
}
