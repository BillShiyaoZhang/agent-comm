//go:build ignore

// Run with: go run ./v2/testdata/generate.go > v2/testdata/interop.json
package main

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"os"

	"github.com/BillShiyaoZhang/agent-comm/v2"
	"github.com/mr-tron/base58"
)

func ed(seed byte) (ed25519.PublicKey, ed25519.PrivateKey) {
	bytes := make([]byte, 32)
	for i := range bytes {
		bytes[i] = seed
	}
	private := ed25519.NewKeyFromSeed(bytes)
	return private.Public().(ed25519.PublicKey), private
}

func x(seed byte) *ecdh.PrivateKey {
	bytes := make([]byte, 32)
	for i := range bytes {
		bytes[i] = seed
	}
	private, err := ecdh.X25519().NewPrivateKey(bytes)
	if err != nil {
		panic(err)
	}
	return private
}

func urn(public ed25519.PublicKey) string {
	h := sha256.Sum256(public)
	return "urn:agent-comm:agent:" + base58.Encode(h[:16])
}

func must(raw []byte, err error) []byte {
	if err != nil {
		panic(err)
	}
	return raw
}

func main() {
	rootPub, rootPriv := ed(1)
	receiptPub, receiptPriv := ed(2)
	issuerPub, issuerPriv := ed(3)
	senderPub, senderPriv := ed(4)
	recipientPub, _ := ed(5)
	consolePub, _ := ed(6)
	recipient := x(7)
	gateway := x(8)
	p := &v2.Policy{Version: 2, PlatformID: "interop-platform", Epoch: 7, NotBefore: 1700000000, ExpiresAt: 2300000000, Mode: v2.ModeCompliance, Suite: v2.Suite, GatewayKeyID: "gateway-fixture", GatewayPublicKey: gateway.PublicKey().Bytes(), ReceiptKeyID: "receipt-fixture", ReceiptPublicKey: receiptPub, AllowV1: false, ManagedIssuerPublicKey: issuerPub}
	if err := v2.SignPolicy(p, rootPriv); err != nil {
		panic(err)
	}
	h := v2.Header{Version: 2, PlatformID: p.PlatformID, PolicyEpoch: p.Epoch, PolicyHash: v2.PolicyHash(p), Mode: p.Mode, Suite: p.Suite, SenderURN: urn(senderPub), RecipientURN: urn(recipientPub), SessionID: "fixture-session", Direction: "a_to_b", Sequence: 1, MessageID: "fixture-message", Expiry: 2200000000, ContentType: "application/agent-comm+json", RecipientKeyID: "recipient-fixture"}
	plaintext := []byte("hello from Go v2")
	env, cek, err := v2.SealCompliance(p, h, plaintext, recipient.PublicKey().Bytes(), senderPriv)
	if err != nil {
		panic(err)
	}
	rawEnvelope := must(v2.Canonical(env))
	receipt, err := v2.MakeReceipt(p, rawEnvelope, cek, receiptPriv, 2000000000, v2.ResultDecryptedAdmitted)
	if err != nil {
		panic(err)
	}
	cert := &v2.ManagedIdentityCertificate{Version: 2, Role: v2.ManagedConsoleRole, PlatformID: p.PlatformID, URN: urn(consolePub), IdentityPublicKey: consolePub, NotBefore: 1700000000, ExpiresAt: 2300000000, Serial: "fixture-managed-1"}
	if err := v2.SignManagedCertificate(cert, issuerPriv); err != nil {
		panic(err)
	}
	output := struct {
		Policy              []byte `json:"policy"`
		Envelope            []byte `json:"envelope"`
		Receipt             []byte `json:"receipt"`
		ManagedCertificate  []byte `json:"managed_certificate"`
		RootPublicKey       []byte `json:"root_public_key"`
		SenderPublicKey     []byte `json:"sender_public_key"`
		RecipientPrivateKey []byte `json:"recipient_private_key"`
		GatewayPrivateKey   []byte `json:"gateway_private_key"`
		CEK                 []byte `json:"cek"`
		Plaintext           []byte `json:"plaintext"`
		PolicyHash          string `json:"policy_hash"`
		EnvelopeHash        string `json:"envelope_hash"`
	}{must(v2.Canonical(p)), rawEnvelope, must(v2.Canonical(receipt)), must(v2.Canonical(cert)), rootPub, senderPub, recipient.Bytes(), gateway.Bytes(), cek, plaintext, v2.PolicyHash(p), v2.EnvelopeHash(rawEnvelope)}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(output); err != nil {
		panic(err)
	}
}
