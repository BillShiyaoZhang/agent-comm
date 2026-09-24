package v2

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/mr-tron/base58"
)

func testIdentity(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey, string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(pub)
	return pub, priv, "urn:agent-comm:agent:" + base58.Encode(digest[:16])
}

func testPolicy(t *testing.T, mode string) (*Policy, ed25519.PrivateKey, []byte) {
	t.Helper()
	_, rootPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	receiptPublic, receiptPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	gatewayPrivate, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	p := &Policy{Version: Version, PlatformID: "test-platform", Epoch: 1, NotBefore: now - 60, ExpiresAt: now + 3600, Mode: mode, Suite: Suite, GatewayKeyID: "gateway-1", GatewayPublicKey: gatewayPrivate.PublicKey().Bytes(), ReceiptKeyID: "receipt-1", ReceiptPublicKey: receiptPublic, ManagedIssuerPublicKey: []byte{}}
	if err := SignPolicy(p, rootPrivate); err != nil {
		t.Fatal(err)
	}
	if err := VerifyPolicy(p, rootPrivate.Public().(ed25519.PublicKey), time.Now()); err != nil {
		t.Fatal(err)
	}
	return p, receiptPrivate, gatewayPrivate.Bytes()
}

func TestComplianceSameBodyAndReceipt(t *testing.T) {
	p, receiptPrivate, gatewayPrivate := testPolicy(t, ModeCompliance)
	senderPub, senderPrivate, senderURN := testIdentity(t)
	_, _, recipientURN := testIdentity(t)
	recipientPrivate, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	h := Header{Version: Version, PlatformID: p.PlatformID, PolicyEpoch: p.Epoch, PolicyHash: PolicyHash(p), Mode: p.Mode, Suite: p.Suite, SenderURN: senderURN, RecipientURN: recipientURN, SessionID: "session-1", Direction: "a_to_b", Sequence: 1, MessageID: "message-1", Expiry: time.Now().Add(time.Hour).Unix(), ContentType: "application/agent-comm+json", RecipientKeyID: "recipient-1"}
	env, originalCEK, err := SealCompliance(p, h, []byte("one exact body"), recipientPrivate.PublicKey().Bytes(), senderPrivate)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := Canonical(env)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := VerifyEnvelope(p, senderPub, raw, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyEnvelopeSignature(senderPub, raw); err != nil {
		t.Fatalf("old envelope signature must remain verifiable: %v", err)
	}
	newPolicy := *p
	newPolicy.Epoch++
	if _, err := VerifyEnvelope(&newPolicy, senderPub, raw, time.Now()); err == nil {
		t.Fatal("old envelope admitted under new policy")
	}
	wrongPub, _, _ := testIdentity(t)
	if _, err := VerifyEnvelopeSignature(wrongPub, raw); err == nil {
		t.Fatal("envelope signature accepted wrong sender key")
	}
	gatewayCEK, gatewayBody, err := GatewayOpen(p, parsed, gatewayPrivate)
	if err != nil {
		t.Fatal(err)
	}
	recipientCEK, recipientBody, err := RecipientOpenCompliance(p, parsed, recipientPrivate.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(originalCEK, gatewayCEK) || !bytes.Equal(gatewayCEK, recipientCEK) || !bytes.Equal(gatewayBody, recipientBody) || string(recipientBody) != "one exact body" {
		t.Fatal("gateway and recipient did not open the same CEK and body")
	}
	receipt, err := MakeReceipt(p, raw, gatewayCEK, receiptPrivate, time.Now().Unix(), ResultDecryptedAdmitted)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyReceipt(p, receipt, raw, recipientCEK, time.Now()); err != nil {
		t.Fatal(err)
	}
	wrongCEK := bytes.Repeat([]byte{1}, 32)
	if err := VerifyReceipt(p, receipt, raw, wrongCEK, time.Now()); err == nil {
		t.Fatal("receipt accepted wrong recipient CEK")
	}
	withoutGateway := *env
	withoutGateway.Slots = []Slot{env.Slots[0]}
	if err := SignEnvelope(&withoutGateway, senderPrivate); err != nil {
		t.Fatal(err)
	}
	bad, _ := Canonical(&withoutGateway)
	if _, err := VerifyEnvelope(p, senderPub, bad, time.Now()); err == nil {
		t.Fatal("missing gateway slot accepted")
	}
	withThird := *env
	withThird.Slots = append(append([]Slot{}, env.Slots...), env.Slots[0])
	if err := SignEnvelope(&withThird, senderPrivate); err != nil {
		t.Fatal(err)
	}
	bad, _ = Canonical(&withThird)
	if _, err := VerifyEnvelope(p, senderPub, bad, time.Now()); err == nil {
		t.Fatal("third slot accepted")
	}
	broken := *env
	broken.Ciphertext = append([]byte{}, env.Ciphertext...)
	broken.Ciphertext[0] ^= 1
	if err := SignEnvelope(&broken, senderPrivate); err != nil {
		t.Fatal(err)
	}
	if _, _, err := GatewayOpen(p, &broken, gatewayPrivate); err == nil {
		t.Fatal("tampered body accepted by gateway")
	}
}

func TestPrivateEphemeralHandshake(t *testing.T) {
	p, receiptPrivate, _ := testPolicy(t, ModePrivate)
	aPub, aPrivate, aURN := testIdentity(t)
	bPub, bPrivate, bURN := testIdentity(t)
	init, aEphemeral, err := NewInit(p, aURN, bURN, "b-key", "session-private", time.Now().Add(time.Hour).Unix(), aPrivate)
	if err != nil {
		t.Fatal(err)
	}
	accept, bEphemeral, err := NewAccept(p, init, aPub, bPrivate, "b-key", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	aSession, err := CompleteInitiator(p, init, accept, aEphemeral, aPub, bPub, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	bSession, err := CompleteResponder(p, init, accept, bEphemeral, aPub, bPub, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	aFinished, err := MakeFinished(aSession, aURN, aPrivate)
	if err != nil {
		t.Fatal(err)
	}
	bFinished, err := MakeFinished(bSession, bURN, bPrivate)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyFinished(aSession, bFinished, bPub, aURN); err != nil {
		t.Fatal(err)
	}
	if err := VerifyFinished(bSession, aFinished, aPub, bURN); err != nil {
		t.Fatal(err)
	}
	aKey, err := aSession.PrivateMessageKey("a_to_b", 1)
	if err != nil {
		t.Fatal(err)
	}
	bKey, err := bSession.PrivateMessageKey("a_to_b", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(aKey, bKey) {
		t.Fatal("private message keys differ")
	}
	h := Header{Version: Version, PlatformID: p.PlatformID, PolicyEpoch: p.Epoch, PolicyHash: PolicyHash(p), Mode: p.Mode, Suite: p.Suite, SenderURN: aURN, RecipientURN: bURN, SessionID: init.SessionID, Direction: "a_to_b", Sequence: 1, MessageID: "private-1", Expiry: time.Now().Add(time.Hour).Unix(), ContentType: "application/agent-comm+json", RecipientKeyID: "b-key"}
	env, err := SealPrivate(p, h, []byte("private body"), aKey, aPrivate)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := Canonical(env)
	parsed, err := VerifyEnvelope(p, aPub, raw, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	opened, err := OpenPrivate(p, parsed, bKey)
	if err != nil || string(opened) != "private body" {
		t.Fatalf("private open: %v", err)
	}
	receipt, err := MakeReceipt(p, raw, nil, receiptPrivate, time.Now().Unix(), ResultAcceptedUninspected)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyReceipt(p, receipt, raw, nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	if !aSession.Ready() || !bSession.Ready() {
		t.Fatal("sessions not finished")
	}
}

func TestHPKEX25519OfficialKEMVector(t *testing.T) {
	// RFC 9180 Appendix A.1.1, KEM values. The KEM output is independent of
	// whether the later AEAD choice is AES-128-GCM or our AES-256-GCM.
	ephemeral, _ := hex.DecodeString("52c4a758a802cd8b936eceea314432798d5baf2d7e9235dc084ab1b9cfa2f736")
	recipient, _ := hex.DecodeString("3948cfe0ad1ddb695d780e59077195da6c56506b027329794ab02bca80815c4d")
	expected, _ := hex.DecodeString("fe0e18c9f024ce43799ae393c7e8fe8fce9d218875e8227b0187c04e7d2ea1fc")
	private, err := ecdh.X25519().NewPrivateKey(ephemeral)
	if err != nil {
		t.Fatal(err)
	}
	shared, err := hpkeSharedSecret(private, recipient, private.PublicKey().Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(shared, expected) {
		t.Fatalf("RFC 9180 KEM mismatch: %x", shared)
	}
}

func TestComplianceRequiresLocalAuthorization(t *testing.T) {
	dir := t.TempDir()
	if ComplianceAllowed(dir, nil) {
		t.Fatal("compliance enabled by default")
	}
	root, rootPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	receipt, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	gateway, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	policy := &Policy{Version: Version, PlatformID: "platform", Epoch: 1, NotBefore: now - 60, ExpiresAt: now + 3600, Mode: ModeCompliance, Suite: Suite, GatewayKeyID: "gateway", GatewayPublicKey: gateway.PublicKey().Bytes(), ReceiptKeyID: "receipt", ReceiptPublicKey: receipt}
	if err := SignPolicy(policy, rootPrivate); err != nil {
		t.Fatal(err)
	}
	hash := PolicyHash(policy)
	if err := AllowCompliance(dir, policy, hash, "owner approval"); err == nil {
		t.Fatal("authorized without pinned root")
	}
	if err := PinPolicyRoot(dir, root, "platform", "verified through deployment package"); err != nil {
		t.Fatal(err)
	}
	other := *policy
	other.PlatformID = "another-platform"
	if err := SignPolicy(&other, rootPrivate); err != nil {
		t.Fatal(err)
	}
	if err := AllowCompliance(dir, &other, PolicyHash(&other), "same root, wrong platform"); err == nil {
		t.Fatal("a signed policy for another platform was authorized")
	}
	if err := AllowCompliance(dir, policy, hash, ""); err == nil {
		t.Fatal("empty authorization note accepted")
	}
	if err := AllowCompliance(dir, policy, "wrong", "owner approval"); err == nil {
		t.Fatal("wrong policy hash authorized")
	}
	if err := AllowCompliance(dir, policy, hash, "owner explicitly permits gateway decryption"); err != nil {
		t.Fatal(err)
	}
	if !ComplianceAllowed(dir, policy) {
		t.Fatal("explicit authorization not loaded")
	}
	rotated := *policy
	rotated.Epoch++
	rotated.GatewayKeyID = "gateway-2"
	if err := SignPolicy(&rotated, rootPrivate); err != nil {
		t.Fatal(err)
	}
	if ComplianceAllowed(dir, &rotated) {
		t.Fatal("old authorization applied to a new epoch")
	}
	if err := AllowCompliance(dir, &rotated, PolicyHash(&rotated), "owner reviewed new epoch"); err != nil {
		t.Fatal(err)
	}
	if !ComplianceAllowed(dir, &rotated) {
		t.Fatal("new epoch authorization not loaded")
	}
	if err := DisallowCompliance(dir, "owner revoked gateway access"); err != nil {
		t.Fatal(err)
	}
	if ComplianceAllowed(dir, &rotated) {
		t.Fatal("revocation did not stop compliance")
	}
}

func TestAttachPlatformIDToLegacyRootPin(t *testing.T) {
	dir := t.TempDir()
	root, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	old, err := Canonical(RootPin{PublicKey: root, VerificationNote: "earlier independently checked root", VerifiedAt: time.Now().Unix()})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rootPinPath(dir), old, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPolicyRootPin(dir); err == nil {
		t.Fatal("legacy pin without platform ID accepted")
	}
	if err := PinPolicyRoot(dir, root, "platform-peer-id", "owner checked platform ID out of band"); err != nil {
		t.Fatal(err)
	}
	pin, err := LoadPolicyRootPin(dir)
	if err != nil || pin.PlatformID != "platform-peer-id" {
		t.Fatalf("platform pin migration failed: %+v %v", pin, err)
	}
	if err := PinPolicyRoot(dir, root, "other-platform", "different ID"); err == nil {
		t.Fatal("existing platform ID was overwritten")
	}
}

func TestFetchPolicyRequiresIndependentPlatformPin(t *testing.T) {
	policy, _, _ := testPolicy(t, ModePrivate)
	root, rootPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := SignPolicy(policy, rootPrivate); err != nil {
		t.Fatal(err)
	}
	raw, err := Canonical(policy)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string][]byte{"policy": raw})
	}))
	defer server.Close()
	_, private, urn := testIdentity(t)
	client, err := NewHTTPClient(server.URL, urn, private)
	if err != nil {
		t.Fatal(err)
	}
	// The policy is signed by the correct root, but its platform ID must also
	// match an independently verified pin. A bootstrap response is not a pin.
	if _, err := client.FetchPolicy(context.Background(), root, 0); err == nil {
		t.Fatal("policy fetched without a platform pin")
	}
	client.ExpectedPlatformID = "another-platform"
	if _, err := client.FetchPolicy(context.Background(), root, 0); err == nil {
		t.Fatal("signed policy for another platform accepted")
	}
	client.ExpectedPlatformID = policy.PlatformID
	if _, err := client.FetchPolicy(context.Background(), root, 0); err != nil {
		t.Fatalf("correct pinned platform rejected: %v", err)
	}
}

func TestGatewayBodyTypeValidation(t *testing.T) {
	if err := ValidateBody([]byte(`{"agent_comm":2,"text":"hello","kind":"message","hop_limit":8}`)); err != nil {
		t.Fatal(err)
	}
	for _, raw := range [][]byte{
		[]byte(`{"agent_comm":2,"text":""}`),
		[]byte(`{"agent_comm":1,"text":"hello"}`),
		[]byte(`{"agent_comm":2,"text":"hello","opaque_ciphertext":"secret"}`),
		[]byte(`{"agent_comm":2,"text":"hello","text":"other"}`),
		[]byte(`{"agent_comm":2,"text":"hello"}{"agent_comm":2,"text":"other"}`),
	} {
		if err := ValidateBody(raw); err == nil {
			t.Fatalf("invalid body accepted: %s", raw)
		}
	}
}
