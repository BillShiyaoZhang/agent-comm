package main

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BillShiyaoZhang/agent-comm/agent"
	"github.com/BillShiyaoZhang/agent-comm/crypto"
	"github.com/BillShiyaoZhang/agent-comm/mq"
	"github.com/BillShiyaoZhang/agent-comm/registry"
	"github.com/BillShiyaoZhang/agent-comm/v2"
)

type v2RecoveryFixture struct {
	engine      *v2Engine
	mail        *mailbox
	policy      *v2.Policy
	peerURN     string
	peerPublic  ed25519.PublicKey
	peerPrivate ed25519.PrivateKey
	peerX       []byte
	stored      atomic.Int32
	onRequest   func(string)
}

func newV2RecoveryFixture(t *testing.T) *v2RecoveryFixture {
	t.Helper()
	keysDir := t.TempDir()
	keys, err := crypto.LoadOrCreateIdentity(keysDir)
	if err != nil {
		t.Fatal(err)
	}
	mail, err := openMailbox(filepath.Join(keysDir, "mailbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mail.db.Close() })
	fixture := &v2RecoveryFixture{mail: mail}
	for {
		fixture.peerPublic, fixture.peerPrivate, err = ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		fixture.peerURN = registry.URNFromEd25519PK(fixture.peerPublic)
		if keys.Ed25519.URN() < fixture.peerURN {
			break
		}
	}
	peerID, err := agent.PeerIDFromEd25519PK(fixture.peerPublic)
	if err != nil {
		t.Fatal(err)
	}
	peerX, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	fixture.peerX = peerX.PublicKey().Bytes()
	timestamp := time.Now().Unix()
	signature := ed25519.Sign(fixture.peerPrivate, registry.BuildSignedMsg(fixture.peerURN, peerID.String(), fixture.peerX, false, timestamp))
	_, rootPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	fixture.policy = disclosureTestPolicy(t, rootPrivate, v2.ModePrivate, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fixture.onRequest != nil {
			fixture.onRequest(r.URL.Path)
		}
		switch r.URL.Path {
		case "/api/v1/registry/resolve":
			if r.URL.Query().Get("urn") != fixture.peerURN {
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"found": true, "urn": fixture.peerURN, "peer_id": peerID.String(), "x25519_pubkey": fixture.peerX, "ed25519_pubkey": fixture.peerPublic, "signature": signature, "timestamp": timestamp, "stores_user_data": false})
		case "/api/v2/handshake/store":
			var request map[string][]byte
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			frame, err := v2.ParseFrame(request["frame"])
			if err == nil {
				err = v2.ValidateFrameForRelay(fixture.policy, frame, time.Now())
			}
			if err != nil {
				http.Error(w, "invalid handshake payload", http.StatusBadRequest)
				return
			}
			fixture.stored.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "frame_id": v2.FrameHash(frame)})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	client, err := v2.NewHTTPClient(server.URL, keys.Ed25519.URN(), keys.Ed25519.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	ds := &DaemonServer{agent: &agent.Agent{Keys: keys, MQHTTPClient: &mq.HTTPClient{BaseURL: server.URL}}, mailbox: mail}
	fixture.engine = &v2Engine{ds: ds, client: client, keysDir: keysDir, policy: fixture.policy}
	ds.v2 = fixture.engine
	return fixture
}

func (f *v2RecoveryFixture) persistInit(t *testing.T, peerURN, sessionID string, expired bool) *v2.HandshakeFrame {
	t.Helper()
	frame, secret, err := v2.NewInit(f.policy, f.engine.client.URN, peerURN, v2.KeyID(f.peerX), sessionID, time.Now().Add(10*time.Minute).Unix(), f.engine.client.IdentityPrivate)
	if err != nil {
		t.Fatal(err)
	}
	if expired {
		var payload v2.InitPayload
		if err := json.Unmarshal(frame.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		payload.Expiry = time.Now().Add(-time.Minute).Unix()
		frame.Payload, err = v2.Canonical(payload)
		if err != nil {
			t.Fatal(err)
		}
		if err := v2.SignFrame(frame, f.engine.client.IdentityPrivate); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := v2.Canonical(frame)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.mail.saveV2Handshake(v2HandshakeRecord{sessionID: sessionID, peerURN: peerURN, role: "initiator", initFrame: raw, ephemeralPrivate: secret, status: "init-sent"}); err != nil {
		t.Fatal(err)
	}
	return frame
}

func TestV2ExpiredPersistedInitRecoversWithoutChangingOutbox(t *testing.T) {
	f := newV2RecoveryFixture(t)
	old := f.persistInit(t, f.peerURN, "expired-init", true)
	if _, err := f.mail.acceptV2(StoreRequest{MessageID: "stable-message", RecipientURN: f.peerURN, MessageFields: MessageFields{Text: "retained"}}); err != nil {
		t.Fatal(err)
	}
	if err := f.engine.resendPending(context.Background(), f.policy); err != nil {
		t.Fatal(err)
	}
	if f.stored.Load() != 0 {
		t.Fatal("expired frame was sent to Platform again")
	}
	if _, err := f.mail.loadV2HandshakeByID(old.SessionID); err == nil {
		t.Fatal("expired Init still occupies peer handshake slot")
	}
	if err := f.engine.startHandshake(context.Background(), f.policy, f.peerURN); err != nil {
		t.Fatal(err)
	}
	fresh, err := f.mail.loadV2HandshakeByPeer(f.peerURN)
	if err != nil || fresh.sessionID == old.SessionID || f.stored.Load() != 1 {
		t.Fatalf("fresh handshake did not replace expired Init: %+v, %v", fresh, err)
	}
	status, err := f.mail.v2OutgoingStatus("stable-message")
	if err != nil || status["status"] != "accepted" || status["attempts"] != 0 {
		t.Fatalf("original outbox message changed: %+v, %v", status, err)
	}
}

func TestV2UnexpiredPersistedInitReplaysExactBytes(t *testing.T) {
	f := newV2RecoveryFixture(t)
	old := f.persistInit(t, f.peerURN, "live-init", false)
	raw, _ := v2.Canonical(old)
	if err := f.engine.resendPending(context.Background(), f.policy); err != nil {
		t.Fatal(err)
	}
	record, err := f.mail.loadV2HandshakeByID(old.SessionID)
	if err != nil || !bytes.Equal(record.initFrame, raw) || f.stored.Load() != 1 {
		t.Fatalf("unexpired handshake did not replay intact: %+v, %v", record, err)
	}
}

func TestV2ExpiredInitKeepsDependentUnfinishedSession(t *testing.T) {
	f := newV2RecoveryFixture(t)
	f.persistInit(t, f.peerURN, "unfinished-session", true)
	if err := f.mail.saveV2Session(f.peerURN, &v2.Session{ID: "unfinished-session", InitiatorURN: f.engine.client.URN, ResponderURN: f.peerURN}); err != nil {
		t.Fatal(err)
	}
	if err := f.engine.resendPending(context.Background(), f.policy); err == nil {
		t.Fatal("unfinished session dependency was silently retired")
	}
	if _, err := f.mail.loadV2HandshakeByID("unfinished-session"); err != nil {
		t.Fatalf("dependent handshake was deleted: %v", err)
	}
	session, err := f.mail.loadV2Session(f.peerURN)
	if err != nil || session.ID != "unfinished-session" {
		t.Fatalf("dependent session was changed: %+v, %v", session, err)
	}
	if f.stored.Load() != 0 {
		t.Fatal("expired Init was replayed despite dependent session")
	}
}

func TestV2ExpiredResponderDoesNotDropFreshPeerInit(t *testing.T) {
	f := newV2RecoveryFixture(t)
	oldInit, _, err := v2.NewInit(f.policy, f.peerURN, f.engine.client.URN, v2.KeyID(f.engine.ds.agent.Keys.X25519PK), "old-peer-init", time.Now().Add(10*time.Minute).Unix(), f.peerPrivate)
	if err != nil {
		t.Fatal(err)
	}
	oldAccept, secret, err := v2.NewAccept(f.policy, oldInit, f.peerPublic, f.engine.client.IdentityPrivate, v2.KeyID(f.engine.ds.agent.Keys.X25519PK), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var payload v2.AcceptPayload
	if err := json.Unmarshal(oldAccept.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	payload.Expiry = time.Now().Add(-time.Minute).Unix()
	oldAccept.Payload, err = v2.Canonical(payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := v2.SignFrame(oldAccept, f.engine.client.IdentityPrivate); err != nil {
		t.Fatal(err)
	}
	oldRaw, _ := v2.Canonical(oldInit)
	acceptRaw, _ := v2.Canonical(oldAccept)
	if err := f.mail.saveV2Handshake(v2HandshakeRecord{sessionID: oldInit.SessionID, peerURN: f.peerURN, role: "responder", initFrame: oldRaw, acceptFrame: acceptRaw, ephemeralPrivate: secret, status: "accept-sent"}); err != nil {
		t.Fatal(err)
	}
	freshInit, _, err := v2.NewInit(f.policy, f.peerURN, f.engine.client.URN, v2.KeyID(f.engine.ds.agent.Keys.X25519PK), "fresh-peer-init", time.Now().Add(10*time.Minute).Unix(), f.peerPrivate)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.engine.handleInit(context.Background(), f.policy, freshInit, f.peerPublic); err != nil {
		t.Fatal(err)
	}
	record, err := f.mail.loadV2HandshakeByPeer(f.peerURN)
	if err != nil || record.sessionID != freshInit.SessionID || f.stored.Load() != 2 {
		t.Fatalf("fresh peer Init was dropped behind expired responder: %+v, %v", record, err)
	}
}

func TestV2HandshakeWaitDoesNotStarveLaterOutbound(t *testing.T) {
	f := newV2RecoveryFixture(t)
	f.persistInit(t, f.peerURN, "pending-first", false)
	secondPublic, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	secondURN := registry.URNFromEd25519PK(secondPublic)
	f.persistInit(t, secondURN, "pending-second", false)
	for _, item := range []struct{ id, recipient string }{{"a-first", f.peerURN}, {"b-second", secondURN}} {
		if _, err := f.mail.acceptV2(StoreRequest{MessageID: item.id, RecipientURN: item.recipient, MessageFields: MessageFields{Text: item.id}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.engine.deliverNext(context.Background(), f.policy); err != nil {
		t.Fatal(err)
	}
	next, err := f.mail.nextV2Outgoing()
	if err != nil || next == nil || next.request.MessageID != "b-second" {
		t.Fatalf("earlier handshake wait starved later peer: %+v, %v", next, err)
	}
	status, err := f.mail.v2OutgoingStatus("a-first")
	if err != nil || status["status"] != "accepted" || status["attempts"] != 0 {
		t.Fatalf("waiting message was rewritten: %+v, %v", status, err)
	}
}

func TestV2PermanentIDConflictStopsRetryWithoutChangingQueuedBytes(t *testing.T) {
	f := newV2RecoveryFixture(t)
	for _, id := range []string{"a-conflict", "b-later"} {
		if _, err := f.mail.acceptV2(StoreRequest{MessageID: id, RecipientURN: f.peerURN, MessageFields: MessageFields{Text: id}}); err != nil {
			t.Fatal(err)
		}
	}
	envelope, cek := []byte("persisted-envelope"), []byte("persisted-cek")
	if _, err := f.mail.db.Exec("UPDATE helper_v2_outbox SET envelope=?,cek=? WHERE message_id=?", envelope, cek, "a-conflict"); err != nil {
		t.Fatal(err)
	}
	cause := &v2.HTTPError{Path: "/api/v2/mq/store", StatusCode: http.StatusConflict, Body: "v2 message ID conflict\n"}
	if err := f.engine.retryV2("a-conflict", 8, cause); err != cause {
		t.Fatalf("unexpected conflict error: %v", err)
	}
	status, err := f.mail.v2OutgoingStatus("a-conflict")
	if err != nil || status["status"] != "conflict" || status["attempts"] != 9 || status["receipt_verified"] != false {
		t.Fatalf("permanent conflict was retried: %+v, %v", status, err)
	}
	var keptEnvelope, keptCEK, request []byte
	if err := f.mail.db.QueryRow("SELECT envelope,cek,request FROM helper_v2_outbox WHERE message_id=?", "a-conflict").Scan(&keptEnvelope, &keptCEK, &request); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(keptEnvelope, envelope) || !bytes.Equal(keptCEK, cek) || !bytes.Contains(request, []byte("a-conflict")) {
		t.Fatal("conflict changed persisted request or ciphertext")
	}
	next, err := f.mail.nextV2Outgoing()
	if err != nil || next == nil || next.request.MessageID != "b-later" {
		t.Fatalf("permanent conflict blocked later outbound: %+v, %v", next, err)
	}
}

func TestV2OtherHTTP409StillRetries(t *testing.T) {
	f := newV2RecoveryFixture(t)
	if _, err := f.mail.acceptV2(StoreRequest{MessageID: "policy-retry", RecipientURN: f.peerURN, MessageFields: MessageFields{Text: "keep"}}); err != nil {
		t.Fatal(err)
	}
	for _, cause := range []*v2.HTTPError{
		{Path: "/api/v2/mq/store", StatusCode: http.StatusConflict, Body: "v2 policy mismatch\n"},
		{Path: "/api/v2/handshake/store", StatusCode: http.StatusConflict, Body: "v2 message ID conflict\n"},
	} {
		_ = f.engine.retryV2("policy-retry", 0, cause)
		status, err := f.mail.v2OutgoingStatus("policy-retry")
		if err != nil || status["status"] != "accepted" {
			t.Fatalf("unrelated 409 was treated as permanent: %+v, %v", status, err)
		}
	}
}

type v2OutboxSnapshot struct {
	request, envelope, cek, receipt  []byte
	policyHash, sessionID, status    string
	attempts, nextAttempt, createdAt int64
	lastError                        string
}

func snapshotV2Outbox(t *testing.T, m *mailbox, id string) v2OutboxSnapshot {
	t.Helper()
	var got v2OutboxSnapshot
	err := m.db.QueryRow(`SELECT request,envelope,cek,receipt,policy_hash,session_id,
		status,attempts,next_attempt,last_error,created_at FROM helper_v2_outbox
		WHERE message_id=?`, id).Scan(&got.request, &got.envelope, &got.cek,
		&got.receipt, &got.policyHash, &got.sessionID, &got.status,
		&got.attempts, &got.nextAttempt, &got.lastError, &got.createdAt)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestV2PersistedConflictMigrationIsExactAndPreservesEvidence(t *testing.T) {
	f := newV2RecoveryFixture(t)
	const oldError = "v2 platform /api/v2/mq/store: HTTP 409: v2 message ID conflict"
	cases := []struct {
		id, status, lastError string
		attempts              int
		retire                bool
	}{
		{"old-lf", "accepted", oldError + "\n", 8, true},
		{"old-crlf", "accepted", oldError + "\r\n", 3, true},
		{"old-bare", "accepted", oldError, 1, true},
		{"different-409", "accepted", "v2 platform /api/v2/mq/store: HTTP 409: v2 policy mismatch\n", 8, false},
		{"similar-error", "accepted", oldError + " plus more\n", 8, false},
		{"never-tried", "accepted", oldError + "\n", 0, false},
		{"already-terminal", "conflict", oldError + "\n", 8, false},
		{"other-message", "accepted", "", 4, false},
	}
	before := make(map[string]v2OutboxSnapshot)
	for _, tc := range cases {
		if _, err := f.mail.acceptV2(StoreRequest{MessageID: tc.id, RecipientURN: f.peerURN, MessageFields: MessageFields{Text: tc.id}}); err != nil {
			t.Fatal(err)
		}
		if _, err := f.mail.db.Exec(`UPDATE helper_v2_outbox SET envelope=?,cek=?,receipt=?,policy_hash=?,
			session_id=?,status=?,attempts=?,next_attempt=?,last_error=? WHERE message_id=?`,
			[]byte("envelope-"+tc.id), []byte("cek-"+tc.id), []byte("receipt-"+tc.id),
			"policy-"+tc.id, "session-"+tc.id, tc.status, tc.attempts, int64(123456789), tc.lastError, tc.id); err != nil {
			t.Fatal(err)
		}
		before[tc.id] = snapshotV2Outbox(t, f.mail, tc.id)
	}
	sessionRaw := []byte(`{"session":"untouched"}`)
	if _, err := f.mail.db.Exec(`INSERT INTO helper_v2_sessions(peer_urn,data) VALUES(?,?)`, f.peerURN, sessionRaw); err != nil {
		t.Fatal(err)
	}
	identityBefore := make(map[string][]byte)
	for _, name := range []string{"identity_sk.pem", "identity_pk.pem", "identity_x25519_sk.pem", "identity_x25519_pk.pem"} {
		data, err := os.ReadFile(filepath.Join(f.engine.keysDir, name))
		if err != nil {
			t.Fatal(err)
		}
		identityBefore[name] = data
	}
	retired, err := f.mail.retirePersistedV2Conflicts()
	if err != nil || retired != 3 {
		t.Fatalf("retired %d rows: %v", retired, err)
	}
	for _, tc := range cases {
		want := before[tc.id]
		if tc.retire {
			want.status = "conflict"
		}
		if got := snapshotV2Outbox(t, f.mail, tc.id); !reflect.DeepEqual(got, want) {
			t.Fatalf("migration changed unexpected fields in %s: got %+v, want %+v", tc.id, got, want)
		}
	}
	var sessionAfter []byte
	if err := f.mail.db.QueryRow(`SELECT data FROM helper_v2_sessions WHERE peer_urn=?`, f.peerURN).Scan(&sessionAfter); err != nil || !bytes.Equal(sessionAfter, sessionRaw) {
		t.Fatalf("session changed: %v", err)
	}
	for name, want := range identityBefore {
		got, err := os.ReadFile(filepath.Join(f.engine.keysDir, name))
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("identity file %s changed: %v", name, err)
		}
	}
	if retired, err := f.mail.retirePersistedV2Conflicts(); err != nil || retired != 0 {
		t.Fatalf("migration was not idempotent: retired %d: %v", retired, err)
	}
}

func TestV2TickMigratesOldConflictBeforeAnyNetworkRequest(t *testing.T) {
	f := newV2RecoveryFixture(t)
	f.engine.lastRefresh = time.Now()
	const id = "old-conflict"
	if _, err := f.mail.acceptV2(StoreRequest{MessageID: id, RecipientURN: f.peerURN, MessageFields: MessageFields{Text: "old"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.mail.db.Exec(`UPDATE helper_v2_outbox SET envelope=?,policy_hash=?,attempts=8,
		last_error=? WHERE message_id=?`, []byte("old-ciphertext"), v2.PolicyHash(f.policy),
		"v2 platform /api/v2/mq/store: HTTP 409: v2 message ID conflict\n", id); err != nil {
		t.Fatal(err)
	}
	var networkCalls, mqStores atomic.Int32
	var prematureNetwork atomic.Bool
	f.onRequest = func(path string) {
		networkCalls.Add(1)
		if path == "/api/v2/mq/store" {
			mqStores.Add(1)
		}
		status, err := f.mail.v2OutgoingStatus(id)
		if err != nil || status["status"] != "conflict" {
			prematureNetwork.Store(true)
		}
	}
	_ = f.engine.tick(context.Background()) // Fixture does not serve retrieval routes.
	if networkCalls.Load() == 0 || prematureNetwork.Load() || mqStores.Load() != 0 {
		t.Fatalf("old conflict retried before migration: requests=%d stores=%d premature=%v",
			networkCalls.Load(), mqStores.Load(), prematureNetwork.Load())
	}
	status, err := f.mail.v2OutgoingStatus(id)
	if err != nil || status["status"] != "conflict" || status["attempts"] != 8 {
		t.Fatalf("tick did not retire old conflict: %+v, %v", status, err)
	}
}
