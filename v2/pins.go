package v2

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type PeerPin struct {
	URN               string `json:"urn"`
	IdentityPublicKey []byte `json:"identity_public_key"`
	VerificationNote  string `json:"verification_note"`
	VerifiedAt        int64  `json:"verified_at"`
}

type RootPin struct {
	PublicKey        []byte `json:"public_key"`
	PlatformID       string `json:"platform_id"`
	VerificationNote string `json:"verification_note"`
	VerifiedAt       int64  `json:"verified_at"`
}

func peerPinPath(keysDir, urn string) string {
	h := sha256.Sum256([]byte(urn))
	return filepath.Join(keysDir, "v2_peers", hex.EncodeToString(h[:])+".json")
}

func rootPinPath(keysDir string) string { return filepath.Join(keysDir, "v2_policy_root.json") }

func compliancePermissionPath(keysDir string) string {
	return filepath.Join(keysDir, "v2_allow_compliance.json")
}

type complianceGrant struct {
	Authorized       bool   `json:"authorized"`
	PlatformID       string `json:"platform_id"`
	PolicyEpoch      uint64 `json:"policy_epoch"`
	PolicyHash       string `json:"policy_hash"`
	GatewayKeyID     string `json:"gateway_key_id"`
	VerificationNote string `json:"verification_note"`
	RecordedAt       int64  `json:"recorded_at"`
}

func writeComplianceGrant(keysDir string, grant complianceGrant) error {
	data, err := Canonical(grant)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(compliancePermissionPath(keysDir), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := file.Chmod(0600); err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		return err
	}
	return file.Sync()
}

// AllowCompliance records permission for one exact, currently valid policy.
// A new epoch, gateway key, or any other signed policy change needs a new
// decision; the platform cannot widen the local authorization by rotation.
func AllowCompliance(keysDir string, policy *Policy, expectedHash, verificationNote string) error {
	if strings.TrimSpace(verificationNote) == "" {
		return errors.New("explicit compliance authorization note required")
	}
	pin, err := LoadPolicyRootPin(keysDir)
	if err != nil {
		return err
	}
	if err := VerifyPolicy(policy, ed25519.PublicKey(pin.PublicKey), time.Now()); err != nil {
		return err
	}
	if policy.PlatformID != pin.PlatformID {
		return errors.New("policy platform ID differs from independently pinned platform")
	}
	if policy.Mode != ModeCompliance || expectedHash == "" || PolicyHash(policy) != expectedHash {
		return errors.New("explicit current compliance policy hash required")
	}
	return writeComplianceGrant(keysDir, complianceGrant{true, policy.PlatformID, policy.Epoch, expectedHash, policy.GatewayKeyID, verificationNote, time.Now().Unix()})
}

// DisallowCompliance stops future local compliance work. It cannot retract
// messages already admitted by the platform or plaintext already disclosed.
func DisallowCompliance(keysDir, revocationNote string) error {
	if strings.TrimSpace(revocationNote) == "" {
		return errors.New("explicit compliance revocation note required")
	}
	grant := complianceGrant{VerificationNote: revocationNote, RecordedAt: time.Now().Unix()}
	if data, err := os.ReadFile(compliancePermissionPath(keysDir)); err == nil {
		var previous complianceGrant
		if json.Unmarshal(data, &previous) == nil {
			grant.PlatformID, grant.PolicyEpoch, grant.PolicyHash, grant.GatewayKeyID = previous.PlatformID, previous.PolicyEpoch, previous.PolicyHash, previous.GatewayKeyID
		}
	}
	return writeComplianceGrant(keysDir, grant)
}

func ComplianceAllowed(keysDir string, policy *Policy) bool {
	if policy == nil || policy.Mode != ModeCompliance {
		return false
	}
	pin, err := LoadPolicyRootPin(keysDir)
	if err != nil || policy.PlatformID != pin.PlatformID || VerifyPolicy(policy, ed25519.PublicKey(pin.PublicKey), time.Now()) != nil {
		return false
	}
	data, err := os.ReadFile(compliancePermissionPath(keysDir))
	if err != nil {
		return false
	}
	var grant complianceGrant
	return json.Unmarshal(data, &grant) == nil && grant.Authorized && strings.TrimSpace(grant.VerificationNote) != "" && grant.PlatformID == policy.PlatformID && grant.PolicyEpoch == policy.Epoch && grant.PolicyHash == PolicyHash(policy) && grant.GatewayKeyID == policy.GatewayKeyID
}

func PinPeer(keysDir, urn string, public ed25519.PublicKey, verificationNote string) error {
	if !URNMatchesPublicKey(urn, public) {
		return errors.New("peer URN does not match full Ed25519 public key")
	}
	if strings.TrimSpace(verificationNote) == "" {
		return errors.New("independent verification note required")
	}
	if err := os.MkdirAll(filepath.Join(keysDir, "v2_peers"), 0700); err != nil {
		return err
	}
	path := peerPinPath(keysDir, urn)
	if _, err := os.Stat(path); err == nil {
		return errors.New("peer already pinned; identity rotation requires a separate explicit workflow")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	data, err := Canonical(PeerPin{URN: urn, IdentityPublicKey: public, VerificationNote: verificationNote, VerifiedAt: time.Now().Unix()})
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.Write(data); err != nil {
		return err
	}
	return file.Sync()
}

func LoadPeerPin(keysDir, urn string) (*PeerPin, error) {
	data, err := os.ReadFile(peerPinPath(keysDir, urn))
	if err != nil {
		return nil, err
	}
	var pin PeerPin
	if err := json.Unmarshal(data, &pin); err != nil {
		return nil, err
	}
	if pin.URN != urn || !URNMatchesPublicKey(urn, pin.IdentityPublicKey) || strings.TrimSpace(pin.VerificationNote) == "" {
		return nil, errors.New("invalid stored peer pin")
	}
	return &pin, nil
}

func PinPolicyRoot(keysDir string, public ed25519.PublicKey, platformID, verificationNote string) error {
	if len(public) != ed25519.PublicKeySize || platformID == "" || len(platformID) > 256 || strings.TrimSpace(platformID) != platformID || strings.ContainsAny(platformID, " \t\r\n") || strings.TrimSpace(verificationNote) == "" {
		return errors.New("independently verified policy root, platform ID and note required")
	}
	if err := os.MkdirAll(keysDir, 0700); err != nil {
		return err
	}
	path := rootPinPath(keysDir)
	if _, err := os.Stat(path); err == nil {
		// An early v2 pin stored only the root. Attach the independently
		// verified platform ID only when that exact root is already present.
		// Never overwrite a different root or a pinned platform ID.
		oldData, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		var old RootPin
		if json.Unmarshal(oldData, &old) != nil || len(old.PublicKey) != ed25519.PublicKeySize || !ed25519.PublicKey(old.PublicKey).Equal(public) || old.PlatformID != "" || strings.TrimSpace(old.VerificationNote) == "" {
			return errors.New("policy root or platform already pinned; rotation requires authenticated update")
		}
		data, marshalErr := Canonical(RootPin{PublicKey: public, PlatformID: platformID, VerificationNote: verificationNote, VerifiedAt: time.Now().Unix()})
		if marshalErr != nil {
			return marshalErr
		}
		return os.WriteFile(path, data, 0600)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	data, err := Canonical(RootPin{PublicKey: public, PlatformID: platformID, VerificationNote: verificationNote, VerifiedAt: time.Now().Unix()})
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.Write(data); err != nil {
		return err
	}
	return file.Sync()
}

func LoadPolicyRootPin(keysDir string) (*RootPin, error) {
	data, err := os.ReadFile(rootPinPath(keysDir))
	if err != nil {
		return nil, fmt.Errorf("v2 policy root not pinned: %w", err)
	}
	var pin RootPin
	if err := json.Unmarshal(data, &pin); err != nil {
		return nil, err
	}
	if len(pin.PublicKey) != ed25519.PublicKeySize || pin.PlatformID == "" || len(pin.PlatformID) > 256 || strings.TrimSpace(pin.PlatformID) != pin.PlatformID || strings.ContainsAny(pin.PlatformID, " \t\r\n") || strings.TrimSpace(pin.VerificationNote) == "" {
		return nil, errors.New("invalid stored policy root pin")
	}
	return &pin, nil
}

func LoadPolicyRoot(keysDir string) (ed25519.PublicKey, error) {
	pin, err := LoadPolicyRootPin(keysDir)
	if err != nil {
		return nil, err
	}
	return ed25519.PublicKey(pin.PublicKey), nil
}
