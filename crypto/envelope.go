package crypto

import (
	"crypto/ed25519"
	"fmt"
	"strings"

	pb "github.com/BillShiyaoZhang/agent-comm/proto"
	goproto "google.golang.org/protobuf/proto"
)

// EnvelopeSignatureDomain separates envelope signatures from other protocols.
const EnvelopeSignatureDomain = "agent-comm-envelope-v1\x00"

// MaxEnvelopeSize bounds network frames and encrypted payloads.
const MaxEnvelopeSize = 1 << 20

// URNMatchesPublicKey checks a self-certifying identity, including custom URN
// namespaces. The entire URN is signed by the protocol using this check.
func URNMatchesPublicKey(urn string, publicKey []byte) bool {
	if len(publicKey) != ed25519.PublicKeySize || !strings.HasPrefix(urn, "urn:") || strings.ContainsAny(urn, " \t\r\n|\x00") {
		return false
	}
	separator := strings.LastIndexByte(urn, ':')
	if separator <= len("urn:") || separator == len(urn)-1 {
		return false
	}
	identity := IdentityKeyPair{PublicKey: publicKey}
	return urn[separator+1:] == identity.Fingerprint()
}

func envelopeSigningBytes(env *pb.EncryptedEnvelope) ([]byte, error) {
	unsigned := goproto.Clone(env).(*pb.EncryptedEnvelope)
	unsigned.Signature = nil
	payload, err := (goproto.MarshalOptions{Deterministic: true}).Marshal(unsigned)
	if err != nil {
		return nil, err
	}
	return append([]byte(EnvelopeSignatureDomain), payload...), nil
}

// SignEnvelope binds the sender identity, destination, message ID and every
// encryption field. Callers must persist and retry this exact signed envelope.
func SignEnvelope(env *pb.EncryptedEnvelope, identity *IdentityKeyPair) error {
	if env == nil || identity == nil || len(identity.PrivateKey) != ed25519.PrivateKeySize {
		return fmt.Errorf("envelope and Ed25519 signing identity are required")
	}
	env.SenderUrn = identity.URN()
	env.SenderEd25519Pubkey = append([]byte(nil), identity.PublicKey...)
	data, err := envelopeSigningBytes(env)
	if err != nil {
		return err
	}
	env.Signature = ed25519.Sign(identity.PrivateKey, data)
	return VerifyEnvelope(env, env.RecipientUrn)
}

// VerifyEnvelope rejects unsigned legacy envelopes and forged sender labels.
// expectedRecipientURN must be the local identity (or MQ storage destination).
func VerifyEnvelope(env *pb.EncryptedEnvelope, expectedRecipientURN string) error {
	if env == nil {
		return fmt.Errorf("missing encrypted envelope")
	}
	if expectedRecipientURN == "" || env.RecipientUrn != expectedRecipientURN {
		return fmt.Errorf("envelope recipient does not match destination")
	}
	if !URNMatchesPublicKey(env.SenderUrn, env.SenderEd25519Pubkey) {
		return fmt.Errorf("envelope sender URN does not match signing key")
	}
	if env.MessageId == "" || len(env.MessageId) > 256 || strings.ContainsAny(env.MessageId, "\x00\r\n") {
		return fmt.Errorf("invalid envelope message ID")
	}
	if len(env.SenderStaticPubkey) != KeySize || len(env.EphemeralPubkey) != KeySize || len(env.Nonce) != NonceSize || len(env.Tag) != TagSize {
		return fmt.Errorf("invalid envelope encryption field lengths")
	}
	if goproto.Size(env) > MaxEnvelopeSize {
		return fmt.Errorf("encrypted envelope exceeds size limit")
	}
	if len(env.Signature) != ed25519.SignatureSize {
		return fmt.Errorf("missing or invalid envelope signature")
	}
	data, err := envelopeSigningBytes(env)
	if err != nil {
		return err
	}
	if !ed25519.Verify(env.SenderEd25519Pubkey, data, env.Signature) {
		return fmt.Errorf("invalid envelope signature")
	}
	return nil
}
