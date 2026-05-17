// Package crypto provides ECIES encryption and key management for agent-comm.
// ECIES: X25519 ECDH + HKDF-SHA256 + AES-256-GCM
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/hkdf"
)

const (
	// NonceSize is the size of the AES-GCM nonce (12 bytes)
	NonceSize = 12
	// TagSize is the size of the GCM authentication tag (16 bytes)
	TagSize = 16
	// KeySize is the size of the AES-256 key (32 bytes)
	KeySize = 32
)

// NewECIES creates a new ECIES instance
func NewECIES() *ECIES {
	return &ECIES{}
}

// ECIES encapsulates the ECIES encryption state
type ECIES struct{}

// GenerateKeyPair generates a new X25519 key pair.
// Returns private key (32 bytes) and public key (32 bytes).
func (e *ECIES) GenerateKeyPair() ([]byte, []byte, error) {
	var privateKey [32]byte

	if _, err := io.ReadFull(rand.Reader, privateKey[:]); err != nil {
		return nil, nil, fmt.Errorf("failed to generate random key: %w", err)
	}

	// Clamp private key for X25519
	privateKey[0] &= 248
	privateKey[31] &= 127
	privateKey[31] |= 64

	publicKey, err := curve25519.X25519(privateKey[:], curve25519.Basepoint)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to derive public key: %w", err)
	}

	return privateKey[:], publicKey, nil
}

// GenerateEphemeralKeyPair generates a fresh ephemeral key pair for a single message.
func (e *ECIES) GenerateEphemeralKeyPair() ([]byte, []byte, error) {
	return e.GenerateKeyPair()
}

// ComputeSharedSecret computes the X25519 ECDH shared secret.
func (e *ECIES) ComputeSharedSecret(privateKey, publicKey []byte) ([]byte, error) {
	if len(privateKey) != 32 || len(publicKey) != 32 {
		return nil, errors.New("invalid key sizes: expected 32 bytes each")
	}

	sharedSecret, err := curve25519.X25519(privateKey, publicKey)
	if err != nil {
		return nil, fmt.Errorf("ECDH failed: %w", err)
	}

	// Check for low-order point attack (shared secret == 0)
	var zero [32]byte
	if subtle.ConstantTimeCompare(sharedSecret, zero[:]) == 1 {
		return nil, errors.New("low-order point: possible attack detected")
	}

	return sharedSecret, nil
}

// DeriveKeys derives encryption keys from shared secret using HKDF.
func (e *ECIES) DeriveKeys(sharedSecret, info []byte) ([]byte, error) {
	hkdfReader := hkdf.New(sha256.New, sharedSecret, nil, info)
	encKey := make([]byte, KeySize)
	if _, err := io.ReadFull(hkdfReader, encKey); err != nil {
		return nil, fmt.Errorf("failed to derive key: %w", err)
	}
	return encKey, nil
}

// EncryptWithSharedSecret encrypts using a pre-computed ECDH shared secret.
// The ephemeral is derived deterministically from sharedSecret via HKDF("agent-comm-ephemeral-v1"),
// so recipient can re-derive it without it being transmitted. This provides per-message key
// separation even when using a static shared secret.
func (e *ECIES) EncryptWithSharedSecret(sharedSecret, plaintext, aad []byte) (
	ephemeral, nonce, ciphertext, tag []byte, err error) {
	// Derive ephemeral deterministically so recipient can recompute it
	hkdfEphem := hkdf.New(sha256.New, sharedSecret, nil, []byte("agent-comm-ephemeral-v1"))
	ephemeral = make([]byte, 32)
	if _, err := io.ReadFull(hkdfEphem, ephemeral); err != nil {
		return nil, nil, nil, nil, fmt.Errorf("derive ephemeral: %w", err)
	}

	// Derive encKey from sharedSecret + ephemeral info
	encKey, err := e.DeriveKeys(sharedSecret, ephemeral)
	if err != nil {
		return nil, nil, nil, nil, err
	}

	// Generate random nonce
	nonce = make([]byte, NonceSize)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, nil, nil, nil, fmt.Errorf("failed to generate nonce: %w", err)
	}

	// AES-256-GCM with AAD = aad only (no key material in AAD, keeping it simple)
	block, err := aes.NewCipher(encKey)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("AES cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("GCM: %w", err)
	}

	ciphertextAndTag := gcm.Seal(nil, nonce, plaintext, aad)
	ctLen := len(ciphertextAndTag) - TagSize
	return ephemeral, nonce, ciphertextAndTag[:ctLen], ciphertextAndTag[ctLen:], nil
}

// DecryptWithSharedSecret decrypts a ciphertext produced by EncryptWithSharedSecret.
// senderEphemeral must match the ephemeral value used during encryption.
func (e *ECIES) DecryptWithSharedSecret(
	sharedSecret, senderEphemeral, nonce, ciphertext, tag, aad []byte,
) ([]byte, error) {
	// Re-derive same encKey using the transmitted ephemeral
	encKey, err := e.DeriveKeys(sharedSecret, senderEphemeral)
	if err != nil {
		return nil, err
	}

	block, err := aes.NewCipher(encKey)
	if err != nil {
		return nil, fmt.Errorf("AES cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("GCM: %w", err)
	}

	ciphertextAndTag := make([]byte, len(ciphertext)+TagSize)
	copy(ciphertextAndTag, ciphertext)
	copy(ciphertextAndTag[len(ciphertext):], tag)

	plaintext, err := gcm.Open(nil, nonce, ciphertextAndTag, aad)
	if err != nil {
		return nil, fmt.Errorf("decryption failed (wrong key or tampered data): %w", err)
	}
	return plaintext, nil
}

// DeriveEphemeral derives the same ephemeral that EncryptWithSharedSecret derives,
// so the recipient can recompute it from the shared secret alone (without it being transmitted).
func (e *ECIES) DeriveEphemeral(sharedSecret []byte) ([]byte, error) {
	hkdfEphem := hkdf.New(sha256.New, sharedSecret, nil, []byte("agent-comm-ephemeral-v1"))
	ephemeral := make([]byte, 32)
	if _, err := io.ReadFull(hkdfEphem, ephemeral); err != nil {
		return nil, fmt.Errorf("derive ephemeral: %w", err)
	}
	return ephemeral, nil
}

// Encrypt encrypts plaintext using ECIES.
// Returns: ephemeral public key, nonce, ciphertext, tag.
func (e *ECIES) Encrypt(recipientStaticPublicKey, plaintext, aad []byte) (
	ephemeralPublicKey, nonce, ciphertext, tag []byte, err error,
) {
	// 1. Generate ephemeral key pair
	ephemeralPriv, ephemeralPub, err := e.GenerateEphemeralKeyPair()
	if err != nil {
		return nil, nil, nil, nil, err
	}

	// 2. Compute shared secret
	sharedSecret, err := e.ComputeSharedSecret(ephemeralPriv, recipientStaticPublicKey)
	if err != nil {
		return nil, nil, nil, nil, err
	}

	// 3. Derive encryption key via HKDF
	info := []byte("agent-comm-v1")
	encKey, err := e.DeriveKeys(sharedSecret, info)
	if err != nil {
		return nil, nil, nil, nil, err
	}

	// 4. Generate random nonce
	nonce = make([]byte, NonceSize)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, nil, nil, nil, fmt.Errorf("failed to generate nonce: %w", err)
	}

	// 5. Encrypt with AES-256-GCM
	block, err := aes.NewCipher(encKey)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("failed to create AES cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("failed to create GCM: %w", err)
	}

	// AAD: ephemeral public key + recipient public key + sender's fingerprint
	fullAAD := make([]byte, 0, len(ephemeralPub)+len(recipientStaticPublicKey)+len(aad))
	fullAAD = append(fullAAD, ephemeralPub...)
	fullAAD = append(fullAAD, recipientStaticPublicKey...)
	fullAAD = append(fullAAD, aad...)

	ciphertextAndTag := gcm.Seal(nil, nonce, plaintext, fullAAD)
	ctLen := len(ciphertextAndTag) - TagSize
	ciphertext = ciphertextAndTag[:ctLen]
	tag = ciphertextAndTag[ctLen:]

	return ephemeralPub, nonce, ciphertext, tag, nil
}

// Decrypt decrypts an ECIES ciphertext.
func (e *ECIES) Decrypt(
	staticPrivateKey, senderEphemeralPublicKey []byte,
	nonce, ciphertext, tag, aad []byte,
) ([]byte, error) {
	if len(staticPrivateKey) != 32 || len(senderEphemeralPublicKey) != 32 {
		return nil, errors.New("invalid key sizes")
	}
	if len(nonce) != NonceSize {
		return nil, errors.New("invalid nonce size")
	}

	// 1. Compute shared secret (static private * ephemeral public)
	sharedSecret, err := curve25519.X25519(staticPrivateKey, senderEphemeralPublicKey)
	if err != nil {
		return nil, fmt.Errorf("ECDH failed: %w", err)
	}

	// Check for low-order point
	var zero [32]byte
	if subtle.ConstantTimeCompare(sharedSecret, zero[:]) == 1 {
		return nil, errors.New("low-order point detected")
	}

	// 2. Derive same encryption key
	info := []byte("agent-comm-v1")
	encKey, err := e.DeriveKeys(sharedSecret, info)
	if err != nil {
		return nil, err
	}

	// 3. Reconstruct AAD (must match what was used in Encrypt)
	// For decryption we need sender's ephemeral pubkey + recipient's pubkey + AAD
	// We can reconstruct recipient pubkey from staticPrivateKey
	recipientPub, err := curve25519.X25519(staticPrivateKey, curve25519.Basepoint)
	if err != nil {
		return nil, err
	}

	fullAAD := make([]byte, 0, len(senderEphemeralPublicKey)+len(recipientPub)+len(aad))
	fullAAD = append(fullAAD, senderEphemeralPublicKey...)
	fullAAD = append(fullAAD, recipientPub...)
	fullAAD = append(fullAAD, aad...)

	// 4. Decrypt
	block, err := aes.NewCipher(encKey)
	if err != nil {
		return nil, fmt.Errorf("failed to create AES cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("failed to create GCM: %w", err)
	}

	ciphertextAndTag := make([]byte, len(ciphertext)+TagSize)
	copy(ciphertextAndTag, ciphertext)
	copy(ciphertextAndTag[len(ciphertext):], tag)

	plaintext, err := gcm.Open(nil, nonce, ciphertextAndTag, fullAAD)
	if err != nil {
		return nil, fmt.Errorf("decryption failed (wrong key or tampered data): %w", err)
	}

	return plaintext, nil
}

// EncryptMessage is a high-level encrypt function.
func (e *ECIES) EncryptMessage(recipientStaticPublicKey, plaintext, senderFingerprint []byte) (
	ephemeralPublicKey, nonce, ciphertext, tag []byte, err error,
) {
	return e.Encrypt(recipientStaticPublicKey, plaintext, senderFingerprint)
}

// DecryptMessage is a high-level decrypt function.
func (e *ECIES) DecryptMessage(
	staticPrivateKey, senderEphemeralPublicKey []byte,
	nonce, ciphertext, tag []byte,
	senderFingerprint []byte,
) ([]byte, error) {
	return e.Decrypt(staticPrivateKey, senderEphemeralPublicKey, nonce, ciphertext, tag, senderFingerprint)
}

// EncodeToBase64 encodes bytes to base64 string
func EncodeToBase64(data []byte) string {
	return base64.StdEncoding.EncodeToString(data)
}

// DecodeFromBase64 decodes a base64 string to bytes
func DecodeFromBase64(s string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(s)
}