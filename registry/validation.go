package registry

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"time"

	"github.com/BillShiyaoZhang/agent-comm/crypto"
	p2pcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

// ErrUnsignedRegistration is returned by the deprecated unsigned registration
// APIs. Use RegisterWithSignature with the URN owner's signing identity instead.
var ErrUnsignedRegistration = errors.New("unsigned registration is disabled; use RegisterWithSignature")

// ValidateRegistration checks ownership and the signature of a new registration.
// Registrations must be signed within the last five minutes, allowing at most
// one minute of clock skew into the future.
func ValidateRegistration(urn, peerID string, x25519PK, ed25519PK, signature []byte, storesUserData bool, timestamp int64) error {
	return validateRegistrationAt(urn, peerID, x25519PK, ed25519PK, signature, storesUserData, timestamp, time.Now().Unix())
}

func validateRegistrationAt(urn, peerID string, x25519PK, ed25519PK, signature []byte, storesUserData bool, timestamp, now int64) error {
	if err := VerifyRegistration(urn, peerID, x25519PK, ed25519PK, signature, storesUserData, timestamp); err != nil {
		return err
	}
	// Avoid subtracting the untrusted timestamp: that difference can overflow.
	if timestamp < now-300 || timestamp > now+60 {
		return fmt.Errorf("registration timestamp is outside the allowed window")
	}
	return nil
}

// VerifyRegistration checks the self-certifying URN, its Ed25519-derived PeerID,
// required fields and signature. It does not enforce freshness: a persisted
// record remains authentic after its admission window has passed. Its expiration
// policy is enforced separately by the store.
//
// The existing signature format covers URN, PeerID, X25519 key, storage policy
// and timestamp. Addresses and relay addresses are untrusted routing hints and
// are not authenticated by this signature.
func VerifyRegistration(urn, peerID string, x25519PK, ed25519PK, signature []byte, storesUserData bool, timestamp int64) error {
	if urn == "" || peerID == "" {
		return fmt.Errorf("urn and peer_id are required")
	}
	if len(x25519PK) != 32 {
		return fmt.Errorf("x25519 public key must be 32 bytes")
	}
	if len(ed25519PK) != ed25519.PublicKeySize {
		return fmt.Errorf("ed25519 public key must be %d bytes", ed25519.PublicKeySize)
	}
	if len(signature) != ed25519.SignatureSize {
		return fmt.Errorf("registration signature must be %d bytes", ed25519.SignatureSize)
	}
	if timestamp <= 0 {
		return fmt.Errorf("registration timestamp must be positive")
	}
	if !crypto.URNMatchesPublicKey(urn, ed25519PK) {
		return fmt.Errorf("identity public key does not match urn")
	}
	pid, err := peer.Decode(peerID)
	if err != nil {
		return fmt.Errorf("invalid peer_id: %w", err)
	}
	// Stored and resolved PeerIDs use this encoding. Accepting another textual
	// encoding would change the signed message when the record is resolved.
	if peerID != pid.String() {
		return fmt.Errorf("peer_id must use its canonical encoding")
	}
	identityKey, err := p2pcrypto.UnmarshalEd25519PublicKey(ed25519PK)
	if err != nil {
		return fmt.Errorf("invalid ed25519 public key: %w", err)
	}
	expectedPID, err := peer.IDFromPublicKey(identityKey)
	if err != nil {
		return fmt.Errorf("derive identity peer_id: %w", err)
	}
	if pid != expectedPID {
		return fmt.Errorf("peer_id does not match identity public key")
	}
	if !ed25519.Verify(ed25519.PublicKey(ed25519PK), BuildSignedMsg(urn, peerID, x25519PK, storesUserData, timestamp), signature) {
		return fmt.Errorf("invalid registration signature")
	}
	return nil
}
