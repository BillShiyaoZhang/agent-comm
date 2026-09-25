package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BillShiyaoZhang/agent-comm/agent"
	"github.com/BillShiyaoZhang/agent-comm/crypto"
	"github.com/BillShiyaoZhang/agent-comm/mq"
	"github.com/BillShiyaoZhang/agent-comm/registry"
	"github.com/BillShiyaoZhang/agent-comm/v2"
)

func TestV2FirstHandshakeDiscoversSignedURNWithoutTrust(t *testing.T) {
	keysDir := t.TempDir()
	keys, err := crypto.LoadOrCreateIdentity(keysDir)
	if err != nil {
		t.Fatal(err)
	}
	mail, err := openMailbox(filepath.Join(keysDir, "mailbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer mail.db.Close()
	peerPublic, peerPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	peerURN := registry.URNFromEd25519PK(peerPublic)
	peerID, err := agent.PeerIDFromEd25519PK(peerPublic)
	if err != nil {
		t.Fatal(err)
	}
	peerX := make([]byte, 32)
	if _, err := rand.Read(peerX); err != nil {
		t.Fatal(err)
	}
	timestamp := time.Now().Unix()
	regSignature := ed25519.Sign(peerPrivate, registry.BuildSignedMsg(peerURN, peerID.String(), peerX, false, timestamp))
	_, rootPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	policy := disclosureTestPolicy(t, rootPrivate, v2.ModePrivate, 1)
	init, _, err := v2.NewInit(policy, peerURN, keys.Ed25519.URN(), v2.KeyID(keys.X25519PK), "first-session", time.Now().Add(30*time.Minute).Unix(), peerPrivate)
	if err != nil {
		t.Fatal(err)
	}
	frame := *init
	frame.Signature = append([]byte(nil), init.Signature...)
	frame.Signature[0] ^= 1
	var currentFrame atomic.Pointer[v2.HandshakeFrame]
	currentFrame.Store(&frame)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/registry/resolve":
			if r.URL.Query().Get("urn") != peerURN {
				http.Error(w, "wrong URN", http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"found": true, "urn": peerURN, "peer_id": peerID.String(), "x25519_pubkey": peerX, "ed25519_pubkey": peerPublic, "signature": regSignature, "timestamp": timestamp, "stores_user_data": false})
		case "/api/v2/handshake/retrieve":
			shown := currentFrame.Load()
			raw, err := v2.Canonical(shown)
			if err != nil {
				t.Error(err)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"frames": []v2.FrameItem{{FrameID: v2.FrameHash(shown), Frame: raw}}})
		case "/api/v2/handshake/store":
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		case "/api/v2/handshake/ack":
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, err := v2.NewHTTPClient(server.URL, keys.Ed25519.URN(), keys.Ed25519.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	ds := &DaemonServer{agent: &agent.Agent{Keys: keys, MQHTTPClient: &mq.HTTPClient{BaseURL: server.URL}}, mailbox: mail}
	engine := &v2Engine{ds: ds, client: client, keysDir: keysDir}
	ds.v2 = engine
	if _, _, err := engine.framePeer(context.Background(), &v2.HandshakeFrame{Type: v2.FrameAccept, SenderURN: peerURN}); !os.IsNotExist(err) {
		t.Fatalf("unknown Accept bypassed first-Init rule: %v", err)
	}
	if err := engine.processFrames(context.Background(), policy); err == nil {
		t.Fatal("badly signed first frame was accepted")
	}
	if _, err := v2.LoadPeerPin(keysDir, peerURN); !os.IsNotExist(err) {
		t.Fatalf("invalid frame caused a persistent pin: %v", err)
	}
	currentFrame.Store(init)
	if err := engine.processFrames(context.Background(), policy); err != nil {
		t.Fatal(err)
	}
	pin, err := v2.LoadPeerPin(keysDir, peerURN)
	if err != nil {
		t.Fatal(err)
	}
	if pin.Source != v2.PeerPinSourceRegistry || pin.VerificationNote != "" || !bytes.Equal(pin.IdentityPublicKey, peerPublic) {
		t.Fatalf("first Init did not retain the correct limited assurance: %+v", pin)
	}
	if _, err := mail.loadV2HandshakeByPeer(peerURN); err != nil {
		t.Fatalf("first valid Init did not start a handshake: %v", err)
	}
}

func TestV2StoreAcceptsURNWithoutPeerPin(t *testing.T) {
	keysDir := t.TempDir()
	keys, err := crypto.LoadOrCreateIdentity(keysDir)
	if err != nil {
		t.Fatal(err)
	}
	mail, err := openMailbox(filepath.Join(keysDir, "mailbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer mail.db.Close()
	rootPublic, rootPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := v2.PinPolicyRoot(keysDir, rootPublic, "platform", "checked installation bundle"); err != nil {
		t.Fatal(err)
	}
	policy := disclosureTestPolicy(t, rootPrivate, v2.ModePrivate, 1)
	if _, err := mail.saveV2Policy(policy); err != nil {
		t.Fatal(err)
	}
	ds := &DaemonServer{agent: &agent.Agent{Keys: keys}, mailbox: mail, outgoing: make(chan struct{}, 1)}
	ds.v2 = &v2Engine{ds: ds, client: &v2.HTTPClient{ExpectedPlatformID: "platform"}, root: rootPublic, keysDir: keysDir, policy: policy}
	peerPublic, peerPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	peerURN := registry.URNFromEd25519PK(peerPublic)
	code, result := helperRequest(t, ds, http.MethodPost, "/api/v2/mq/store", StoreRequest{MessageID: "urn-only-1", RecipientURN: peerURN, MessageFields: MessageFields{Text: "friend request"}})
	if code != http.StatusAccepted || result["status"] != "accepted" {
		t.Fatalf("URN-only send was rejected before registry lookup: %d %+v", code, result)
	}
	if _, err := v2.LoadPeerPin(keysDir, peerURN); !os.IsNotExist(err) {
		t.Fatalf("store endpoint claimed a registry lookup before worker resolution: %v", err)
	}
	peerID, err := agent.PeerIDFromEd25519PK(peerPublic)
	if err != nil {
		t.Fatal(err)
	}
	peerX := make([]byte, 32)
	if _, err := rand.Read(peerX); err != nil {
		t.Fatal(err)
	}
	timestamp := time.Now().Unix()
	validSignature := ed25519.Sign(peerPrivate, registry.BuildSignedMsg(peerURN, peerID.String(), peerX, false, timestamp))
	var serveValid atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/registry/resolve" || r.URL.Query().Get("urn") != peerURN {
			http.NotFound(w, r)
			return
		}
		signature := append([]byte(nil), validSignature...)
		if !serveValid.Load() {
			signature[0] ^= 1
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"found": true, "urn": peerURN, "peer_id": peerID.String(), "x25519_pubkey": peerX, "ed25519_pubkey": peerPublic, "signature": signature, "timestamp": timestamp, "stores_user_data": false})
	}))
	defer server.Close()
	ds.agent.MQHTTPClient = &mq.HTTPClient{BaseURL: server.URL}
	if _, err := ds.v2.resolveRecipient(context.Background(), peerURN); err == nil {
		t.Fatal("outbound discovery accepted an invalid registry signature")
	}
	if _, err := v2.LoadPeerPin(keysDir, peerURN); !os.IsNotExist(err) {
		t.Fatalf("invalid registry bundle caused a persistent pin: %v", err)
	}
	serveValid.Store(true)
	resolvedX, err := ds.v2.resolveRecipient(context.Background(), peerURN)
	if err != nil || !bytes.Equal(resolvedX, peerX) {
		t.Fatalf("outbound discovery failed with a signed bundle: %v", err)
	}
	pin, err := v2.LoadPeerPin(keysDir, peerURN)
	if err != nil || pin.Source != v2.PeerPinSourceRegistry || pin.VerificationNote != "" {
		t.Fatalf("outbound discovery claimed human verification: %+v %v", pin, err)
	}
}
