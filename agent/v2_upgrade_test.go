package agent

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/BillShiyaoZhang/agent-comm/crypto"
	"github.com/BillShiyaoZhang/agent-comm/mq"
	"github.com/BillShiyaoZhang/agent-comm/v2"
)

func TestSendMessageRequiresV2AfterRootPin(t *testing.T) {
	keysDir := t.TempDir()
	keys, err := crypto.LoadOrCreateIdentity(keysDir)
	if err != nil {
		t.Fatal(err)
	}
	a := &Agent{Keys: keys}
	if err := a.legacyDirectSendGuard(); err != nil {
		t.Fatalf("legacy unconfigured identity unexpectedly gated: %v", err)
	}
	a.MQHTTPClient = &mq.HTTPClient{}
	var upgrade *LegacySendError
	if err := a.SendMessage(context.Background(), "urn:agent-comm:agent:peer", "hello"); !errors.As(err, &upgrade) || upgrade.Code != "policy_root_required" {
		t.Fatalf("platform-connected SendMessage bypassed missing policy root: %v", err)
	}
	root, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := v2.PinPolicyRoot(keysDir, root, "platform", "verified out of band"); err != nil {
		t.Fatal(err)
	}
	if err := a.SendMessage(context.Background(), "urn:agent-comm:agent:peer", "hello"); !errors.As(err, &upgrade) || upgrade.Code != "upgrade_required" {
		t.Fatalf("SendMessage silently used v1 after v2 opt-in: %v", err)
	}
	if err := os.WriteFile(filepath.Join(keysDir, "v2_policy_root.json"), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.SendMessage(context.Background(), "urn:agent-comm:agent:peer", "hello"); !errors.As(err, &upgrade) || upgrade.Code != "policy_unavailable" {
		t.Fatalf("invalid policy root fell back to v1: %v", err)
	}
}
