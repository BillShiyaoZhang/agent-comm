// Package v2 implements the independently authenticated Agent Comm v2 wire
// format. V1 protobuf envelopes must never be parsed as v2 messages.
package v2

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mr-tron/base58"
)

const (
	Version                   = 2
	ModePrivate               = "private"
	ModeCompliance            = "compliance"
	Suite                     = "X25519-HKDF-SHA256-AES256GCM"
	RoleRecipient             = "recipient"
	RoleGateway               = "gateway"
	ResultDecryptedAdmitted   = "decrypted-admitted"
	ResultAcceptedUninspected = "accepted-uninspected"
	ContentTypeAgentJSON      = "application/agent-comm+json"
)

const (
	policyDomain   = "agent-comm-v2-policy\x00"
	envelopeDomain = "agent-comm-v2-envelope\x00"
	receiptDomain  = "agent-comm-v2-receipt\x00"
	frameDomain    = "agent-comm-v2-handshake\x00"
)

// JSON has an intentionally fixed field order. All signed fields are present,
// including null values. A parser must reject any alternate JSON encoding.
// Byte slices are standard base64 strings in JSON.
type Policy struct {
	Version                int    `json:"version"`
	PlatformID             string `json:"platform_id"`
	Epoch                  uint64 `json:"epoch"`
	NotBefore              int64  `json:"not_before"`
	ExpiresAt              int64  `json:"expires_at"`
	Mode                   string `json:"mode"`
	Suite                  string `json:"suite"`
	GatewayKeyID           string `json:"gateway_key_id"`
	GatewayPublicKey       []byte `json:"gateway_public_key"`
	ReceiptKeyID           string `json:"receipt_key_id"`
	ReceiptPublicKey       []byte `json:"receipt_public_key"`
	AllowV1                bool   `json:"allow_v1"`
	ManagedIssuerPublicKey []byte `json:"managed_issuer_public_key"`
	Signature              []byte `json:"signature"`
}

type Header struct {
	Version        int      `json:"version"`
	PlatformID     string   `json:"platform_id"`
	PolicyEpoch    uint64   `json:"policy_epoch"`
	PolicyHash     string   `json:"policy_hash"`
	Mode           string   `json:"mode"`
	Suite          string   `json:"suite"`
	SenderURN      string   `json:"sender_urn"`
	RecipientURN   string   `json:"recipient_urn"`
	SessionID      string   `json:"session_id"`
	Direction      string   `json:"direction"`
	Sequence       uint64   `json:"sequence"`
	MessageID      string   `json:"message_id"`
	Expiry         int64    `json:"expiry"`
	ContentType    string   `json:"content_type"`
	RecipientKeyID string   `json:"recipient_key_id"`
	GatewayKeyID   string   `json:"gateway_key_id"`
	SlotRoles      []string `json:"slot_roles"`
}

type Slot struct {
	Role       string `json:"role"`
	KeyID      string `json:"key_id"`
	Enc        []byte `json:"enc"`
	Ciphertext []byte `json:"ciphertext"`
}

type Envelope struct {
	Header     Header `json:"header"`
	Nonce      []byte `json:"nonce"`
	Ciphertext []byte `json:"ciphertext"`
	Slots      []Slot `json:"slots"`
	Signature  []byte `json:"signature"`
}

type Receipt struct {
	Version      int    `json:"version"`
	PlatformID   string `json:"platform_id"`
	EnvelopeHash string `json:"envelope_hash"`
	PolicyHash   string `json:"policy_hash"`
	GatewayKeyID string `json:"gateway_key_id"`
	ReceiptKeyID string `json:"receipt_key_id"`
	AdmittedAt   int64  `json:"admitted_at"`
	Result       string `json:"result"`
	Proof        []byte `json:"proof"`
	Signature    []byte `json:"signature"`
}

// Canonical encodes structs in the specified order, without HTML escaping or
// trailing newline. Maps and interface values are not accepted by Parse*.
func Canonical(value any) ([]byte, error) {
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(out.Bytes(), []byte{'\n'}), nil
}

func parseCanonical(raw []byte, value any, limit int) error {
	if len(raw) == 0 || len(raw) > limit {
		return errors.New("invalid v2 message length")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(value); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err == nil {
		return errors.New("trailing JSON value")
	}
	encoded, err := Canonical(value)
	if err != nil {
		return err
	}
	if !bytes.Equal(raw, encoded) {
		return errors.New("noncanonical v2 JSON")
	}
	return nil
}

func ParsePolicy(raw []byte) (*Policy, error) {
	var p Policy
	if err := parseCanonical(raw, &p, 16<<10); err != nil {
		return nil, err
	}
	return &p, nil
}

func ParseEnvelope(raw []byte) (*Envelope, error) {
	var e Envelope
	if err := parseCanonical(raw, &e, 512<<10); err != nil {
		return nil, err
	}
	return &e, nil
}

func ParseReceipt(raw []byte) (*Receipt, error) {
	var r Receipt
	if err := parseCanonical(raw, &r, 16<<10); err != nil {
		return nil, err
	}
	return &r, nil
}

func signedBytes(domain string, value any) ([]byte, error) {
	data, err := Canonical(value)
	if err != nil {
		return nil, err
	}
	return append([]byte(domain), data...), nil
}

func policySignable(p *Policy) Policy {
	copy := *p
	copy.Signature = nil
	return copy
}

func SignPolicy(p *Policy, private ed25519.PrivateKey) error {
	if len(private) != ed25519.PrivateKeySize {
		return errors.New("invalid policy signing key")
	}
	if err := validatePolicyFields(p); err != nil {
		return err
	}
	preimage, err := signedBytes(policyDomain, policySignable(p))
	if err != nil {
		return err
	}
	p.Signature = ed25519.Sign(private, preimage)
	return nil
}

func VerifyPolicy(p *Policy, root ed25519.PublicKey, now time.Time) error {
	if len(root) != ed25519.PublicKeySize {
		return errors.New("pinned policy root required")
	}
	if err := validatePolicyFields(p); err != nil {
		return err
	}
	if now.Unix() < p.NotBefore || now.Unix() >= p.ExpiresAt {
		return errors.New("policy not currently valid")
	}
	preimage, err := signedBytes(policyDomain, policySignable(p))
	if err != nil {
		return err
	}
	if !ed25519.Verify(root, preimage, p.Signature) {
		return errors.New("policy signature invalid")
	}
	return nil
}

func validatePolicyFields(p *Policy) error {
	if p == nil || p.Version != Version || p.PlatformID == "" || p.Epoch == 0 || p.NotBefore <= 0 || p.ExpiresAt <= p.NotBefore || p.Suite != Suite || (p.Mode != ModePrivate && p.Mode != ModeCompliance) {
		return errors.New("invalid v2 policy fields")
	}
	if len(p.ReceiptPublicKey) != ed25519.PublicKeySize || p.ReceiptKeyID == "" {
		return errors.New("policy requires receipt signing key")
	}
	if p.Mode == ModeCompliance && (len(p.GatewayPublicKey) != 32 || p.GatewayKeyID == "" || p.AllowV1) {
		return errors.New("compliance policy requires gateway and receipt keys")
	}
	if len(p.ManagedIssuerPublicKey) != 0 && len(p.ManagedIssuerPublicKey) != ed25519.PublicKeySize {
		return errors.New("invalid managed issuer public key")
	}
	return nil
}

func PolicyHash(p *Policy) string {
	if p == nil {
		return ""
	}
	raw, err := Canonical(p)
	if err != nil {
		return ""
	}
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}

func EnvelopeHash(raw []byte) string {
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}

// KeyID is the stable 128-bit lowercase hex identifier of an X25519 public
// key. It is an identifier, not a substitute for the signed registry bundle.
func KeyID(public []byte) string {
	if len(public) != 32 {
		return ""
	}
	h := sha256.Sum256(public)
	return hex.EncodeToString(h[:16])
}

func envelopeSignable(e *Envelope) Envelope {
	copy := *e
	copy.Signature = nil
	return copy
}

func SignEnvelope(e *Envelope, private ed25519.PrivateKey) error {
	if len(private) != ed25519.PrivateKeySize {
		return errors.New("invalid sender signing key")
	}
	preimage, err := signedBytes(envelopeDomain, envelopeSignable(e))
	if err != nil {
		return err
	}
	e.Signature = ed25519.Sign(private, preimage)
	return nil
}

// VerifyEnvelopeSignature checks canonical encoding, sender URN binding, and
// the sender signature without requiring the current policy. This permits
// lookup of an already admitted envelope after a policy epoch changes.
func VerifyEnvelopeSignature(sender ed25519.PublicKey, raw []byte) (*Envelope, error) {
	if len(sender) != ed25519.PublicKeySize {
		return nil, errors.New("authenticated sender public key required")
	}
	e, err := ParseEnvelope(raw)
	if err != nil {
		return nil, err
	}
	if !URNMatchesPublicKey(e.Header.SenderURN, sender) {
		return nil, errors.New("sender URN does not match public key")
	}
	preimage, err := signedBytes(envelopeDomain, envelopeSignable(e))
	if err != nil {
		return nil, err
	}
	if !ed25519.Verify(sender, preimage, e.Signature) {
		return nil, errors.New("sender signature invalid")
	}
	return e, nil
}

// VerifyEnvelope checks policy, structure, and the externally authenticated
// sender key. It does not assert that the peer identity was verified out of band.
func VerifyEnvelope(policy *Policy, sender ed25519.PublicKey, raw []byte, now time.Time) (*Envelope, error) {
	e, err := VerifyEnvelopeSignature(sender, raw)
	if err != nil {
		return nil, err
	}
	h := e.Header
	if policy == nil || h.Version != Version || h.PlatformID != policy.PlatformID || h.PolicyEpoch != policy.Epoch || h.PolicyHash != PolicyHash(policy) || h.Mode != policy.Mode || h.Suite != policy.Suite {
		return nil, errors.New("envelope policy mismatch")
	}
	if !URNMatchesPublicKey(h.SenderURN, sender) || !strings.HasPrefix(h.RecipientURN, "urn:") || h.SenderURN == h.RecipientURN || h.SessionID == "" || h.MessageID == "" || h.RecipientKeyID == "" || h.ContentType != ContentTypeAgentJSON || (h.Direction != "a_to_b" && h.Direction != "b_to_a") || h.Expiry <= now.Unix() || h.Expiry > policy.ExpiresAt || h.Sequence == 0 || len(e.Nonce) != 12 || len(e.Ciphertext) < 16 {
		return nil, errors.New("invalid v2 envelope header or ciphertext")
	}
	if policy.Mode == ModePrivate {
		if h.GatewayKeyID != "" || len(h.SlotRoles) != 0 || len(e.Slots) != 0 {
			return nil, errors.New("private envelope has key slots")
		}
	} else {
		if h.GatewayKeyID != policy.GatewayKeyID || len(h.SlotRoles) != 2 || h.SlotRoles[0] != RoleRecipient || h.SlotRoles[1] != RoleGateway || len(e.Slots) != 2 || e.Slots[0].Role != RoleRecipient || e.Slots[0].KeyID != h.RecipientKeyID || e.Slots[1].Role != RoleGateway || e.Slots[1].KeyID != policy.GatewayKeyID {
			return nil, errors.New("compliance envelope must have exactly recipient and gateway slots")
		}
		for _, slot := range e.Slots {
			if len(slot.Enc) != 32 || len(slot.Ciphertext) != 48 {
				return nil, errors.New("invalid HPKE slot")
			}
		}
	}
	return e, nil
}

// URNMatchesPublicKey verifies the self-certifying suffix while allowing a
// configured URN namespace. It does not prove who the human intended to meet.
func URNMatchesPublicKey(urn string, key ed25519.PublicKey) bool {
	if len(key) != ed25519.PublicKeySize || !strings.HasPrefix(urn, "urn:") || strings.ContainsAny(urn, " \t\r\n|\x00") {
		return false
	}
	last := strings.LastIndexByte(urn, ':')
	if last <= len("urn:") || last == len(urn)-1 {
		return false
	}
	digest := sha256.Sum256(key)
	return urn[last+1:] == base58.Encode(digest[:16])
}

func VerifyReceipt(policy *Policy, receipt *Receipt, rawEnvelope, cek []byte, now time.Time) error {
	if policy == nil || receipt == nil || len(policy.ReceiptPublicKey) != ed25519.PublicKeySize {
		return errors.New("receipt signing key unavailable")
	}
	if receipt.Version != Version || receipt.PlatformID != policy.PlatformID || receipt.PolicyHash != PolicyHash(policy) || receipt.EnvelopeHash != EnvelopeHash(rawEnvelope) || receipt.ReceiptKeyID != policy.ReceiptKeyID || receipt.GatewayKeyID != policy.GatewayKeyID || receipt.AdmittedAt < policy.NotBefore || receipt.AdmittedAt >= policy.ExpiresAt || receipt.AdmittedAt > now.Unix()+60 {
		return errors.New("receipt policy or envelope mismatch")
	}
	if policy.Mode == ModeCompliance {
		if receipt.Result != ResultDecryptedAdmitted || len(cek) != 32 {
			return errors.New("missing compliance admission")
		}
		expected := Proof(cek, rawEnvelope)
		if !constantTimeEqual(expected, receipt.Proof) {
			return errors.New("gateway did not prove the recipient CEK")
		}
	} else if receipt.Result != ResultAcceptedUninspected || len(receipt.Proof) != 0 {
		return errors.New("invalid private admission result")
	}
	copy := *receipt
	copy.Signature = nil
	preimage, err := signedBytes(receiptDomain, copy)
	if err != nil {
		return err
	}
	if !ed25519.Verify(policy.ReceiptPublicKey, preimage, receipt.Signature) {
		return errors.New("receipt signature invalid")
	}
	return nil
}

func MakeReceipt(policy *Policy, rawEnvelope, cek []byte, private ed25519.PrivateKey, admittedAt int64, result string) (*Receipt, error) {
	if policy == nil || len(private) != ed25519.PrivateKeySize {
		return nil, errors.New("receipt policy and signing key required")
	}
	if admittedAt < policy.NotBefore || admittedAt >= policy.ExpiresAt {
		return nil, errors.New("receipt admission outside policy period")
	}
	if result == ResultDecryptedAdmitted && (policy.Mode != ModeCompliance || len(cek) != 32) {
		return nil, errors.New("compliance CEK required")
	}
	if result == ResultAcceptedUninspected && policy.Mode != ModePrivate {
		return nil, errors.New("uninspected receipt requires private policy")
	}
	if result != ResultDecryptedAdmitted && result != ResultAcceptedUninspected {
		return nil, fmt.Errorf("unknown admission result %q", result)
	}
	r := &Receipt{Version: Version, PlatformID: policy.PlatformID, EnvelopeHash: EnvelopeHash(rawEnvelope), PolicyHash: PolicyHash(policy), GatewayKeyID: policy.GatewayKeyID, ReceiptKeyID: policy.ReceiptKeyID, AdmittedAt: admittedAt, Result: result, Proof: []byte{}}
	if result == ResultDecryptedAdmitted {
		r.Proof = Proof(cek, rawEnvelope)
	}
	copy := *r
	copy.Signature = nil
	preimage, err := signedBytes(receiptDomain, copy)
	if err != nil {
		return nil, err
	}
	r.Signature = ed25519.Sign(private, preimage)
	return r, nil
}
