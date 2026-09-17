package agent

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/BillShiyaoZhang/agent-comm/mq"
	"github.com/BillShiyaoZhang/agent-comm/registry"
)

func TestPresenceRequiresRecentAuthenticatedHeartbeat(t *testing.T) {
	owner := durableTestKeys(t)
	peerID, err := owner.PeerID()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		age    time.Duration
		tamper bool
		status string
	}{
		{"recent", 10 * time.Second, false, "online"},
		{"stale registration", 2 * time.Minute, false, "offline"},
		{"future timestamp", -2 * time.Minute, false, "unknown"},
		{"forged timestamp", 10 * time.Second, true, "unknown"},
	} {
		t.Run(test.name, func(t *testing.T) {
			timestamp := time.Now().Add(-test.age).Unix()
			signature := ed25519.Sign(owner.Ed25519.PrivateKey, registry.BuildSignedMsg(owner.Ed25519.URN(), peerID, owner.X25519PK, false, timestamp))
			if test.tamper {
				timestamp++
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{"found": true, "urn": owner.Ed25519.URN(), "peer_id": peerID,
					"x25519_pubkey": owner.X25519PK, "ed25519_pubkey": owner.Ed25519.PublicKey, "signature": signature, "timestamp": timestamp})
			}))
			defer server.Close()
			ag := &Agent{MQHTTPClient: mq.NewHTTPClient(server.URL, owner)}
			got := ag.PeerPresence(context.Background(), owner.Ed25519.URN())
			if got.Status != test.status {
				t.Fatalf("status=%s, want %s", got.Status, test.status)
			}
			if test.status == "unknown" && got.LastSeen != nil {
				t.Fatal("unverified heartbeat disclosed as last seen")
			}
		})
	}
}

func TestPresenceUnavailableIsUnknown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "unavailable", 503) }))
	defer server.Close()
	ag := &Agent{MQHTTPClient: mq.NewHTTPClient(server.URL, durableTestKeys(t))}
	if got := ag.PeerPresence(context.Background(), "urn:agent-comm:agent:Unavailable"); got.Status != "unknown" || got.LastSeen != nil {
		t.Fatalf("network failure invented presence: %+v", got)
	}
	if err := (&Agent{}).EnsurePlatformRegistration(context.Background()); err == nil {
		t.Fatal("missing platform accepted")
	}
}
