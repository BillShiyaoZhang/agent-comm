// Package crypto provides key management and identity operations for agent-comm.
package crypto

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"github.com/mr-tron/base58"
	"encoding/pem"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/google/uuid"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

// IdentityKeyPair holds Ed25519 identity keys for an agent.
type IdentityKeyPair struct {
	PrivateKey ed25519.PrivateKey
	PublicKey  ed25519.PublicKey
}

// GenerateIdentityKeyPair generates a new Ed25519 identity key pair.
func GenerateIdentityKeyPair() (*IdentityKeyPair, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("failed to generate Ed25519 key: %w", err)
	}
	return &IdentityKeyPair{PublicKey: pub, PrivateKey: priv}, nil
}

// Fingerprint returns the SHA-256 base58 fingerprint of the public key.
// Format: base58(SHA256(pubkey))[:16]
func (k *IdentityKeyPair) Fingerprint() string {
	h := sha256.Sum256(k.PublicKey)
	// Use base58 encoding (Bitcoin style)
	return base58.Encode(h[:16])
}

// URN returns the URN identifier for this identity.
func (k *IdentityKeyPair) URN() string {
	return fmt.Sprintf("urn:hermes:agent:%s", k.Fingerprint())
}

// PeerID returns the libp2p-compatible peer ID derived from the Ed25519 public key.
// Uses the official libp2p multicodec: identity multihash of the public key.
func (k *IdentityKeyPair) PeerID() (string, error) {
	pubKey, err := crypto.UnmarshalEd25519PublicKey(k.PublicKey[:])
	if err != nil {
		return "", fmt.Errorf("failed to unmarshal Ed25519 public key: %w", err)
	}
	pid, err := peer.IDFromPublicKey(pubKey)
	if err != nil {
		return "", fmt.Errorf("failed to derive peer ID: %w", err)
	}
	return pid.String(), nil
}

// PeerID returns the libp2p peer ID as a string.
func (k *IdentityKeys) PeerID() (string, error) {
	return k.Ed25519.PeerID()
}

// Sign signs data with the identity private key.
func (k *IdentityKeyPair) Sign(data []byte) ([]byte, error) {
	return ed25519.Sign(k.PrivateKey, data), nil
}

// Verify verifies a signature with the identity public key.
func (k *IdentityKeyPair) Verify(data, signature []byte) bool {
	return ed25519.Verify(k.PublicKey, data, signature)
}

// SavePrivatePEM saves the private key as a PEM-encoded file.
func (k *IdentityKeyPair) SavePrivatePEM(path string) error {
	privBytes, err := x509.MarshalPKCS8PrivateKey(k.PrivateKey)
	if err != nil {
		// Fallback to raw format if PKCS8 fails
		privBytes = k.PrivateKey
	}
	pemBlock := &pem.Block{Type: "PRIVATE KEY", Bytes: privBytes}
	return os.WriteFile(path, pem.EncodeToMemory(pemBlock), 0600)
}

// SavePublicPEM saves the public key as a PEM-encoded file.
func (k *IdentityKeyPair) SavePublicPEM(path string) error {
	pubBytes, err := x509.MarshalPKIXPublicKey(k.PublicKey)
	if err != nil {
		return fmt.Errorf("failed to marshal public key: %w", err)
	}
	pemBlock := &pem.Block{Type: "PUBLIC KEY", Bytes: pubBytes}
	return os.WriteFile(path, pem.EncodeToMemory(pemBlock), 0644)
}

// LoadPrivatePEM loads a private key from a PEM file.
func LoadPrivatePEM(path string) (*IdentityKeyPair, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read private key file: %w", err)
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("failed to decode PEM block")
	}

	// Try PKCS8 first
	priv, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		// Try raw Ed25519 private key format
		if len(block.Bytes) == ed25519.PrivateKeySize {
			priv = ed25519.PrivateKey(block.Bytes)
		} else {
			return nil, fmt.Errorf("failed to parse private key: %w", err)
		}
	}
	edPriv, ok := priv.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("key is not an Ed25519 private key")
	}
	return &IdentityKeyPair{PrivateKey: edPriv, PublicKey: edPriv.Public().(ed25519.PublicKey)}, nil
}

// LoadPublicPEM loads a public key from a PEM file.
func LoadPublicPEM(path string) (ed25519.PublicKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read public key file: %w", err)
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("failed to decode PEM block")
	}

	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse public key: %w", err)
	}
	edPub, ok := pub.(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("key is not an Ed25519 public key")
	}
	return edPub, nil
}

// GenerateX25519KeyPair generates a new X25519 key pair for ECIES encryption.
func GenerateX25519KeyPair() ([]byte, []byte, error) {
	e := NewECIES()
	return e.GenerateKeyPair()
}

// SaveX25519PrivatePEM saves X25519 private key as PEM.
func SaveX25519PrivatePEM(path string, priv []byte) error {
	block := &pem.Block{Type: "PRIVATE KEY", Bytes: priv}
	return os.WriteFile(path, pem.EncodeToMemory(block), 0600)
}

// SaveX25519PublicPEM saves X25519 public key as PEM.
func SaveX25519PublicPEM(path string, pub []byte) error {
	block := &pem.Block{Type: "PUBLIC KEY", Bytes: pub}
	return os.WriteFile(path, pem.EncodeToMemory(block), 0644)
}

// LoadX25519PrivatePEM loads X25519 private key from PEM.
func LoadX25519PrivatePEM(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("failed to decode PEM block")
	}
	return block.Bytes, nil
}

// LoadX25519PublicPEM loads X25519 public key from PEM.
func LoadX25519PublicPEM(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("failed to decode PEM block")
	}
	return block.Bytes, nil
}

// GenerateMessageID generates a unique message ID.
func GenerateMessageID() string {
	return "msg_" + uuid.NewString()
}

// Token represents a one-time token for contact exchange.
type Token struct {
	Value     string
	ExpiresAt int64
	Used      bool
}

// GenerateToken generates a new 256-bit random token.
func GenerateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", fmt.Errorf("failed to generate token: %w", err)
	}
	return fmt.Sprintf("%x", b), nil
}

// IdentityKeys holds all key material for an agent.
type IdentityKeys struct {
	Ed25519    *IdentityKeyPair
	X25519SK   []byte // X25519 static private key
	X25519PK   []byte // X25519 static public key
	KeysDir    string
}

// DefaultKeysDir returns the default directory for storing keys.
func DefaultKeysDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".hermes", "agent-comm", "contacts")
}

// EnsureKeysDir ensures the keys directory exists.
func EnsureKeysDir() (string, error) {
	dir := DefaultKeysDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("failed to create keys directory: %w", err)
	}
	return dir, nil
}

// LoadOrCreateIdentity loads existing identity or creates a new one.
// If keysDir is empty, uses DefaultKeysDir().
func LoadOrCreateIdentity(keysDir string) (*IdentityKeys, error) {
	if keysDir == "" {
		keysDir = DefaultKeysDir()
	}

	if err := os.MkdirAll(keysDir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create keys directory: %w", err)
	}

	privPath := filepath.Join(keysDir, "identity_sk.pem")
	pubPath := filepath.Join(keysDir, "identity_pk.pem")
	x25519SKPath := filepath.Join(keysDir, "identity_x25519_sk.pem")
	x25519PKPath := filepath.Join(keysDir, "identity_x25519_pk.pem")

	// Try to load existing keys
	if _, err := os.Stat(privPath); err == nil {
		edKey, err := LoadPrivatePEM(privPath)
		if err != nil {
			return nil, fmt.Errorf("failed to load Ed25519 identity: %w", err)
		}

		x25519SK, err := LoadX25519PrivatePEM(x25519SKPath)
		if err != nil {
			return nil, fmt.Errorf("failed to load X25519 private key: %w", err)
		}

		x25519PK, err := LoadX25519PublicPEM(x25519PKPath)
		if err != nil {
			return nil, fmt.Errorf("failed to load X25519 public key: %w", err)
		}

		return &IdentityKeys{
			Ed25519:  edKey,
			X25519SK: x25519SK,
			X25519PK: x25519PK,
			KeysDir:  keysDir,
		}, nil
	}

	// Generate new keys
	edKey, err := GenerateIdentityKeyPair()
	if err != nil {
		return nil, err
	}

	x25519SK, x25519PK, err := GenerateX25519KeyPair()
	if err != nil {
		return nil, err
	}

	// Save all keys
	if err := edKey.SavePrivatePEM(privPath); err != nil {
		return nil, err
	}
	if err := edKey.SavePublicPEM(pubPath); err != nil {
		return nil, err
	}
	if err := SaveX25519PrivatePEM(x25519SKPath, x25519SK); err != nil {
		return nil, err
	}
	if err := SaveX25519PublicPEM(x25519PKPath, x25519PK); err != nil {
		return nil, err
	}

	return &IdentityKeys{
		Ed25519:  edKey,
		X25519SK: x25519SK,
		X25519PK: x25519PK,
		KeysDir:  keysDir,
	}, nil
}