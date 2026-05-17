// Package wot provides Web of Trust: signed trust claims and transitive trust path resolution.
package wot

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/nousresearch/hermes-agent/agent-comm/crypto"
	"github.com/nousresearch/hermes-agent/agent-comm/proto"
)

// TrustClaim wraps the proto TrustClaim with helper methods.
type TrustClaim struct {
	*proto.TrustClaim
}

// NewTrustClaim creates a new TRUSTED claim from issuer to subject.
// The caller provides the subject's known identity info.
// The claim is signed with issuer's Ed25519 key.
func NewTrustClaim(issuerKeys *crypto.IdentityKeys, subjectURN, subjectPeerID string, subjectX25519PK []byte, level proto.TrustLevel) (*TrustClaim, error) {
	if len(subjectX25519PK) != 32 {
		return nil, fmt.Errorf("subject X25519 pubkey must be 32 bytes, got %d", len(subjectX25519PK))
	}
	pid, err := peer.Decode(subjectPeerID)
	if err != nil {
		return nil, fmt.Errorf("invalid peer ID: %w", err)
	}
	_ = pid // validated

	issuedAt := time.Now().Unix()

	// Build the canonical bytes to sign: issuer || subject || peerID || pk || level || issuedAt
	h := sha256.New()
	h.Write([]byte(issuerKeys.Ed25519.URN()))
	h.Write([]byte(subjectURN))
	h.Write([]byte(subjectPeerID))
	h.Write(subjectX25519PK)
	h.Write([]byte{byte(level)})
	binary.Write(h, binary.BigEndian, issuedAt)
	digest := h.Sum(nil)

	sig := ed25519.Sign(issuerKeys.Ed25519.PrivateKey, digest)

	return &TrustClaim{
		TrustClaim: &proto.TrustClaim{
			IssuerUrn:         issuerKeys.Ed25519.URN(),
			SubjectUrn:        subjectURN,
			SubjectPeerId:     subjectPeerID,
			SubjectX25519Pk:   subjectX25519PK,
			Level:             level,
			IssuerSignature:   sig,
			IssuedAtUnix:      issuedAt,
		},
	}, nil
}

// NewDirectTrustClaim is a convenience for creating a direct TRUSTED claim.
// Use when you have verified a peer's key via some out-of-band mechanism.
func NewDirectTrustClaim(issuerKeys *crypto.IdentityKeys, subjectURN, subjectPeerID string, subjectX25519PK []byte) (*TrustClaim, error) {
	return NewTrustClaim(issuerKeys, subjectURN, subjectPeerID, subjectX25519PK, proto.TrustLevel_TRUSTED)
}

// Verify checks the Ed25519 signature on this claim.
// Uses the issuer's Ed25519 public key (derived from IssuerURN).
func (c *TrustClaim) Verify(issuerEd25519PK []byte) error {
	if len(issuerEd25519PK) != ed25519.PublicKeySize {
		return fmt.Errorf("issuer Ed25519 pubkey must be %d bytes", ed25519.PublicKeySize)
	}

	h := sha256.New()
	h.Write([]byte(c.IssuerUrn))
	h.Write([]byte(c.SubjectUrn))
	h.Write([]byte(c.SubjectPeerId))
	h.Write(c.SubjectX25519Pk)
	h.Write([]byte{byte(c.Level)})
	binary.Write(h, binary.BigEndian, c.IssuedAtUnix)
	digest := h.Sum(nil)

	if !ed25519.Verify(issuerEd25519PK, digest, c.IssuerSignature) {
		return fmt.Errorf("signature verification failed")
	}
	return nil
}

// TrustedPubkey returns the claimed X25519 public key if the claim is TRUSTED.
func (c *TrustClaim) TrustedPubkey() []byte {
	if c.Level == proto.TrustLevel_TRUSTED {
		return c.SubjectX25519Pk
	}
	return nil
}

// IsExpired returns true if the claim is older than maxAge.
func (c *TrustClaim) IsExpired(maxAge time.Duration) bool {
	age := time.Since(time.Unix(c.IssuedAtUnix, 0))
	return age > maxAge
}

// ClaimSet is a collection of TrustClaims, keyed by issuer URN.
type ClaimSet map[string][]*TrustClaim

// Add adds a claim to the set.
func (cs ClaimSet) Add(c *TrustClaim) {
	cs[c.IssuerUrn] = append(cs[c.IssuerUrn], c)
}

// GetByIssuer returns all claims issued by a given URN.
func (cs ClaimSet) GetByIssuer(issuerURN string) []*TrustClaim {
	return cs[issuerURN]
}

// GetBySubject returns all claims about a given subject URN.
func (cs ClaimSet) GetBySubject(subjectURN string) []*TrustClaim {
	var out []*TrustClaim
	for _, claims := range cs {
		for _, c := range claims {
			if c.SubjectUrn == subjectURN {
				out = append(out, c)
			}
		}
	}
	return out
}

// ToProto converts the set back to proto TrustClaim list.
func (cs ClaimSet) ToProto() []*proto.TrustClaim {
	var out []*proto.TrustClaim
	for _, claims := range cs {
		for _, c := range claims {
			out = append(out, c.TrustClaim)
		}
	}
	return out
}