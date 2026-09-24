package v2

import (
	"crypto/ed25519"
	"errors"
	"time"
)

const managedDomain = "agent-comm-v2-managed-console\x00"
const ManagedConsoleRole = "managed_console"

// ManagedIdentityCertificate is a narrowly scoped signed exception allowing
// a hosted Web identity to use the explicitly managed v1 control route.
// It never certifies Agent-to-Agent privacy or compliance.
type ManagedIdentityCertificate struct {
	Version           int    `json:"version"`
	Role              string `json:"role"`
	PlatformID        string `json:"platform_id"`
	URN               string `json:"urn"`
	IdentityPublicKey []byte `json:"identity_public_key"`
	NotBefore         int64  `json:"not_before"`
	ExpiresAt         int64  `json:"expires_at"`
	Serial            string `json:"serial"`
	Signature         []byte `json:"signature"`
}

func ParseManagedCertificate(raw []byte) (*ManagedIdentityCertificate, error) {
	var cert ManagedIdentityCertificate
	if err := parseCanonical(raw, &cert, 8<<10); err != nil {
		return nil, err
	}
	return &cert, nil
}

func managedSignable(cert *ManagedIdentityCertificate) ManagedIdentityCertificate {
	copy := *cert
	copy.Signature = nil
	return copy
}

func validateManagedFields(cert *ManagedIdentityCertificate) error {
	if cert == nil || cert.Version != Version || cert.Role != ManagedConsoleRole || cert.PlatformID == "" || len(cert.URN) > 256 || !URNMatchesPublicKey(cert.URN, cert.IdentityPublicKey) || cert.NotBefore <= 0 || cert.ExpiresAt <= cert.NotBefore || cert.Serial == "" {
		return errors.New("invalid managed console certificate")
	}
	return nil
}

func SignManagedCertificate(cert *ManagedIdentityCertificate, issuerPrivate ed25519.PrivateKey) error {
	if len(issuerPrivate) != ed25519.PrivateKeySize {
		return errors.New("managed issuer signing key required")
	}
	if err := validateManagedFields(cert); err != nil {
		return err
	}
	preimage, err := signedBytes(managedDomain, managedSignable(cert))
	if err != nil {
		return err
	}
	cert.Signature = ed25519.Sign(issuerPrivate, preimage)
	return nil
}

func VerifyManagedCertificate(cert *ManagedIdentityCertificate, policy *Policy, now time.Time) error {
	if err := validateManagedFields(cert); err != nil {
		return err
	}
	if policy == nil || len(policy.ManagedIssuerPublicKey) != ed25519.PublicKeySize || cert.PlatformID != policy.PlatformID || now.Unix() < cert.NotBefore || now.Unix() >= cert.ExpiresAt {
		return errors.New("managed certificate issuer, platform, or lifetime invalid")
	}
	preimage, err := signedBytes(managedDomain, managedSignable(cert))
	if err != nil {
		return err
	}
	if !ed25519.Verify(policy.ManagedIssuerPublicKey, preimage, cert.Signature) {
		return errors.New("managed certificate signature invalid")
	}
	return nil
}
