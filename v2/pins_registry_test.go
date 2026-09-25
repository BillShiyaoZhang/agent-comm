package v2

import (
	"encoding/json"
	"os"
	"testing"
)

func TestRegistryPeerPinDoesNotClaimIndependentVerification(t *testing.T) {
	public, _, urn := testIdentity(t)
	dir := t.TempDir()
	if err := PinRegistryPeer(dir, urn, public); err != nil {
		t.Fatal(err)
	}
	if err := PinRegistryPeer(dir, urn, public); err != nil {
		t.Fatalf("same signed identity should be idempotent: %v", err)
	}
	pin, err := LoadPeerPin(dir, urn)
	if err != nil {
		t.Fatal(err)
	}
	if pin.Source != PeerPinSourceRegistry || pin.VerificationNote != "" || pin.VerifiedAt != 0 || pin.DiscoveredAt <= 0 {
		t.Fatalf("registry discovery was represented as independent verification: %+v", pin)
	}
	other, _, _ := testIdentity(t)
	if err := PinRegistryPeer(dir, urn, other); err == nil {
		t.Fatal("accepted a registry key that does not match the URN")
	}
	if err := PinPeer(dir, urn, public, "independently checked"); err != nil {
		t.Fatalf("same-key independent verification did not promote provenance: %v", err)
	}
	promoted, err := LoadPeerPin(dir, urn)
	if err != nil || promoted.Source != "" || promoted.VerificationNote != "independently checked" || promoted.VerifiedAt <= 0 || promoted.DiscoveredAt != 0 {
		t.Fatalf("same-key promotion changed identity or failed to record verification: %+v %v", promoted, err)
	}
	if err := PinRegistryPeer(dir, urn, public); err != nil {
		t.Fatalf("later registry lookup changed a human-verified pin: %v", err)
	}
}

func TestLegacyIndependentPeerPinIsStillAccepted(t *testing.T) {
	public, _, urn := testIdentity(t)
	dir := t.TempDir()
	if err := PinPeer(dir, urn, public, "confirmed through a separate channel"); err != nil {
		t.Fatal(err)
	}
	if err := PinRegistryPeer(dir, urn, public); err != nil {
		t.Fatalf("registry lookup must preserve an existing independent pin: %v", err)
	}
	pin, err := LoadPeerPin(dir, urn)
	if err != nil || pin.Source != "" || pin.VerificationNote == "" {
		t.Fatalf("independent pin was changed: %+v %v", pin, err)
	}
	data, err := os.ReadFile(peerPinPath(dir, urn))
	if err != nil {
		t.Fatal(err)
	}
	var stored PeerPin
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatal(err)
	}
	stored.Source = "unknown"
	bad, err := Canonical(stored)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(peerPinPath(dir, urn), bad, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPeerPin(dir, urn); err == nil {
		t.Fatal("accepted an unknown provenance marker")
	}
}
