package main

import (
	"bytes"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/BillShiyaoZhang/agent-comm/agent"
	"github.com/BillShiyaoZhang/agent-comm/crypto"
	"github.com/BillShiyaoZhang/agent-comm/v2"
)

func disclosureTestPolicy(t *testing.T, root ed25519.PrivateKey, mode string, epoch uint64) *v2.Policy {
	t.Helper()
	receiptPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	gateway, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	p := &v2.Policy{Version: v2.Version, PlatformID: "platform", Epoch: epoch, NotBefore: now - 60, ExpiresAt: now + 3600, Mode: mode, Suite: v2.Suite, GatewayKeyID: "gateway", GatewayPublicKey: gateway.PublicKey().Bytes(), ReceiptKeyID: "receipt", ReceiptPublicKey: receiptPub}
	if err := v2.SignPolicy(p, root); err != nil {
		t.Fatal(err)
	}
	return p
}

func helperRequest(t *testing.T, ds *DaemonServer, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var payload []byte
	if body != nil {
		var err error
		payload, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(payload))
	req.Host = "127.0.0.1:45042"
	w := httptest.NewRecorder()
	ds.ServeHTTP(w, req)
	var result map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &result)
	return w.Code, result
}

func TestDisclosureAndLegacyCutover(t *testing.T) {
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
	ds := &DaemonServer{agent: &agent.Agent{Keys: keys}, mailbox: mail, outgoing: make(chan struct{}, 1)}
	store := StoreRequest{MessageID: "legacy-1", RecipientURN: keys.Ed25519.URN(), MessageFields: MessageFields{Text: "old"}}
	code, state := helperRequest(t, ds, http.MethodGet, "/api/v2/disclosure", nil)
	if code != http.StatusOK || state["state"] != "legacy_unconfigured" || state["legacy_send_code"] != "policy_root_required" || state["platform_can_decrypt"] != nil || state["platform_id"] != nil || state["gateway_key_id"] != nil {
		t.Fatalf("unknown policy was reported as known: %d %+v", code, state)
	}
	code, result := helperRequest(t, ds, http.MethodPost, "/api/v1/mq/store", store)
	if code != http.StatusPreconditionRequired || result["code"] != "policy_root_required" {
		t.Fatalf("new helper sent v1 without a policy root: %d %+v", code, result)
	}
	// Simulate a durable v1 request accepted by an older helper before upgrade.
	if _, err := mail.accept(store); err != nil {
		t.Fatal(err)
	}
	rootPub, rootPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := v2.PinPolicyRoot(keysDir, rootPub, "platform", "operator verified independently"); err != nil {
		t.Fatal(err)
	}
	ds.v2 = &v2Engine{ds: ds, client: &v2.HTTPClient{ExpectedPlatformID: "platform"}, root: rootPub, keysDir: keysDir}
	code, state = helperRequest(t, ds, http.MethodGet, "/api/v2/disclosure", nil)
	if code != http.StatusOK || state["state"] != "policy_unavailable" || state["platform_can_decrypt"] != nil {
		t.Fatalf("unfetched policy was treated as verified: %d %+v", code, state)
	}
	code, result = helperRequest(t, ds, http.MethodPost, "/api/v1/mq/store", store)
	if code != http.StatusServiceUnavailable || result["code"] != "policy_unavailable" {
		t.Fatalf("v1 fell back without signed policy: %d %+v", code, result)
	}
	private := disclosureTestPolicy(t, rootPriv, v2.ModePrivate, 1)
	if _, err := mail.saveV2Policy(private); err != nil {
		t.Fatal(err)
	}
	ds.v2.policy = private
	code, state = helperRequest(t, ds, http.MethodGet, "/api/v2/disclosure", nil)
	if code != http.StatusOK || state["state"] != "ready" || state["mode"] != "private" || state["platform_can_decrypt"] != false || state["policy_verified"] != true || state["platform_id"] != "platform" || state["gateway_key_id"] != nil || state["expires_at"] == nil {
		t.Fatalf("private disclosure incorrect: %d %+v", code, state)
	}
	code, result = helperRequest(t, ds, http.MethodPost, "/api/v1/mq/store", StoreRequest{MessageID: "legacy-2", RecipientURN: keys.Ed25519.URN(), MessageFields: MessageFields{Text: "blocked"}})
	if code != http.StatusConflict || result["code"] != "upgrade_required" {
		t.Fatalf("v1 continued after v2 opt-in: %d %+v", code, result)
	}
	if _, err := mail.acceptV2(StoreRequest{MessageID: "pending-v2", RecipientURN: "peer", MessageFields: MessageFields{Text: "pending"}}); err != nil {
		t.Fatal(err)
	}
	compliance := disclosureTestPolicy(t, rootPriv, v2.ModeCompliance, 2)
	if _, err := mail.saveV2Policy(compliance); err != nil {
		t.Fatal(err)
	}
	ds.v2.policy = compliance
	code, state = helperRequest(t, ds, http.MethodGet, "/api/v2/disclosure", nil)
	if code != http.StatusOK || state["state"] != "consent_required" || state["mode"] != "compliance" || state["platform_can_decrypt"] != true || state["gateway_key_id"] != "gateway" || state["quarantined_v2"] != float64(1) || state["quarantined_v1"] != float64(1) {
		t.Fatalf("compliance cutover state incorrect: %d %+v", code, state)
	}
	code, result = helperRequest(t, ds, http.MethodPost, "/api/v1/mq/store", store)
	if code != http.StatusForbidden || result["code"] != "consent_required" {
		t.Fatalf("v1 did not surface consent: %d %+v", code, result)
	}
	code, result = helperRequest(t, ds, http.MethodPost, "/api/v2/mq/store", store)
	if code != http.StatusForbidden || result["code"] != "consent_required" {
		t.Fatalf("v2 did not require local consent: %d %+v", code, result)
	}
	if err := v2.AllowCompliance(keysDir, compliance, v2.PolicyHash(compliance), "owner approved gateway decryption"); err != nil {
		t.Fatal(err)
	}
	code, state = helperRequest(t, ds, http.MethodGet, "/api/v2/disclosure", nil)
	if code != http.StatusOK || state["state"] != "ready" || state["local_compliance_authorized"] != true || state["v2_send_ready"] != true {
		t.Fatalf("authorized compliance not ready: %d %+v", code, state)
	}
	rotated := disclosureTestPolicy(t, rootPriv, v2.ModeCompliance, 3)
	rotated.GatewayKeyID = "gateway-2"
	if err := v2.SignPolicy(rotated, rootPriv); err != nil {
		t.Fatal(err)
	}
	if _, err := mail.saveV2Policy(rotated); err != nil {
		t.Fatal(err)
	}
	ds.v2.policy = rotated
	code, state = helperRequest(t, ds, http.MethodGet, "/api/v2/disclosure", nil)
	if code != http.StatusOK || state["state"] != "consent_required" || state["local_compliance_authorized"] != false {
		t.Fatalf("old consent leaked across policy rotation: %d %+v", code, state)
	}
	if err := v2.AllowCompliance(keysDir, rotated, v2.PolicyHash(rotated), "owner reviewed new gateway"); err != nil {
		t.Fatal(err)
	}
	if err := v2.DisallowCompliance(keysDir, "owner revoked gateway access"); err != nil {
		t.Fatal(err)
	}
	code, state = helperRequest(t, ds, http.MethodGet, "/api/v2/disclosure", nil)
	if code != http.StatusOK || state["state"] != "consent_required" || state["local_compliance_authorized"] != false {
		t.Fatalf("revocation did not close v2 send: %d %+v", code, state)
	}
	code, result = helperRequest(t, ds, http.MethodPost, "/api/v2/mq/store", store)
	if code != http.StatusForbidden || result["code"] != "consent_required" {
		t.Fatalf("running helper accepted send after revocation: %d %+v", code, result)
	}
}

func TestManagedControlRouteRequiresSavedRequest(t *testing.T) {
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
	ds := &DaemonServer{agent: &agent.Agent{Keys: keys}, mailbox: mail, outgoing: make(chan struct{}, 1)}
	console := "urn:agent-comm:agent:console"
	requestID := "request-1"
	deadline := time.Now().Add(time.Minute).UTC().Format(time.RFC3339)
	requestText, _ := json.Marshal(map[string]any{"protocol": "agent-comm-control/v1", "type": "request", "request_id": requestID, "method": "capabilities", "params": map[string]any{}, "agent_urn": keys.Ed25519.URN(), "console_urn": console, "deadline": deadline})
	if err := mail.receive(InboxMessage{MessageID: requestID, SenderURN: console, MessageFields: MessageFields{Text: string(requestText), Kind: "control.request", ConversationID: "control:" + requestID, Deadline: deadline}}); err != nil {
		t.Fatal(err)
	}
	responseText, _ := json.Marshal(map[string]any{"protocol": "agent-comm-control/v1", "type": "response", "request_id": requestID, "method": "capabilities", "result": map[string]any{"ok": true}, "agent_urn": keys.Ed25519.URN(), "console_urn": console, "deadline": deadline})
	digest := sha256.Sum256(responseText)
	reply := StoreRequest{MessageID: "control-response-" + hex.EncodeToString(digest[:24]), RecipientURN: console, MessageFields: MessageFields{Text: string(responseText), Kind: "control.response", InReplyTo: requestID, ConversationID: "control:" + requestID, Deadline: deadline}}
	code, result := helperRequest(t, ds, http.MethodPost, "/api/v1/managed/mq/store", reply)
	if code != http.StatusAccepted || result["protocol"] != "v1" {
		t.Fatalf("valid managed response rejected: %d %+v", code, result)
	}
	reply.MessageID = "other-response"
	code, _ = helperRequest(t, ds, http.MethodPost, "/api/v1/managed/mq/store", reply)
	if code != http.StatusForbidden {
		t.Fatalf("unbound managed response accepted: %d", code)
	}
	reply.Kind = "message"
	code, _ = helperRequest(t, ds, http.MethodPost, "/api/v1/managed/mq/store", reply)
	if code != http.StatusForbidden {
		t.Fatalf("ordinary message used managed route: %d", code)
	}
}
