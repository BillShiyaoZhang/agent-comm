package agent

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/BillShiyaoZhang/agent-comm/mq"
	"github.com/BillShiyaoZhang/agent-comm/registry"
	"github.com/BillShiyaoZhang/agent-comm/session"
)

func TestPreparedHTTPRetryPreservesSignedEnvelope(t *testing.T) {
	alice, bob := durableTestKeys(t), durableTestKeys(t)
	urn := bob.Ed25519.URN()
	peerID, err := bob.PeerID()
	if err != nil {
		t.Fatal(err)
	}
	timestamp := time.Now().Unix()
	signature := ed25519.Sign(bob.Ed25519.PrivateKey, registry.BuildSignedMsg(urn, peerID, bob.X25519PK, false, timestamp))
	var attempts [][]byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/registry/resolve":
			if r.URL.Query().Get("urn") != urn {
				t.Error("wrong recipient lookup")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"found": true, "urn": urn, "peer_id": peerID, "x25519_pubkey": bob.X25519PK, "ed25519_pubkey": bob.Ed25519.PublicKey, "signature": signature, "timestamp": timestamp})
		case "/api/v1/mq/store":
			var body struct {
				Payload []byte `json:"payload_proto"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			attempts = append(attempts, body.Payload)
			if len(attempts) == 1 {
				http.Error(w, "acceptance response lost", http.StatusServiceUnavailable)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "message_id": "persisted-outbox-id"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	a := &Agent{Keys: alice, Session: session.NewManager(nil, alice), MQHTTPClient: mq.NewHTTPClient(server.URL, alice)}
	env, err := a.PrepareMessage(context.Background(), urn, "retry safely", "persisted-outbox-id")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.DeliverEnvelope(context.Background(), env); err == nil {
		t.Fatal("lost acceptance should remain retryable")
	}
	if err := a.DeliverEnvelope(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 2 || !bytes.Equal(attempts[0], attempts[1]) {
		t.Fatal("retry changed the signed ciphertext")
	}
}

func TestRegistryRecipientMustBindSignatureIdentityAndPeer(t *testing.T) {
	bob := durableTestKeys(t)
	pid, err := PeerIDFromEd25519PK(bob.Ed25519.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	result := &registry.ResolveResult{X25519PubKey: bob.X25519PK, Ed25519PubKey: bob.Ed25519.PublicKey, Timestamp: time.Now().Unix()}
	result.ID = pid
	if err := verifyRecipient(bob.Ed25519.URN(), result); err == nil {
		t.Fatal("unsigned registry result accepted")
	}
	result.Signature = ed25519.Sign(bob.Ed25519.PrivateKey, registry.BuildSignedMsg(bob.Ed25519.URN(), pid.String(), bob.X25519PK, false, result.Timestamp))
	if err := verifyRecipient(bob.Ed25519.URN(), result); err != nil {
		t.Fatal(err)
	}
	result.X25519PubKey = durableTestKeys(t).X25519PK
	if err := verifyRecipient(bob.Ed25519.URN(), result); err == nil {
		t.Fatal("substituted encryption key accepted")
	}
}
