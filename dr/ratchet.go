// Package dr implements the Double Ratchet (Signal Protocol) for forward-secret encrypted sessions.
// Each message uses a new ephemeral key derived from a ratcheting chain.
// Compromise of one key does not expose messages outside a bounded window.
package dr

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"fmt"
	"io"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/hkdf"
)

// HKDF params for Double Ratchet.
var hkdfInfoRoot = []byte("DoubleRatchet")
var hkdfInfoMsg = []byte("DoubleRatchetMessage")

// RatchetState holds the per-peer Double Ratchet state.
// It is stateful — a new instance is created for each peer session.
type RatchetState struct {
	// Our DH key pair (X25519, rotated on each DH ratchet step)
	DHSecret [32]byte
	DHPub    [32]byte

	// Their current DH ratchet public key
	TheirDHPub [32]byte

	// Root key (32 bytes) — HKDF input for chain key derivation
	RootKey [32]byte

	// Root key before the first DH ratchet (used to re-derive chain keys on first decrypt)
	origRootKey [32]byte

	// Sending chain key and count
	SendChainKey [32]byte
	SendCount    int

	// Receiving chain key and count
	ReceiveChainKey [32]byte
	RecvCount       int

	// Have we done our first DH ratchet step?
	Initialized bool

	// First DH ratchet flag (reset after first DH ratchet completes)
	firstDH bool
}

// DrMessage is a single Double Ratchet message.
type DrMessage struct {
	Header     DrHeader
	Ciphertext []byte // encrypted payload (using message key)
}

// DrHeader contains the DH pubkey and counters for ratchet state advancement.
type DrHeader struct {
	DHPub  [32]byte // sender's DH public key for this message
	PN     int      // previous chain length (for double ratchet)
	MsgNum int      // message number in this chain
}

// Public returns the public key component of the DH key pair.
func (s *RatchetState) Public() [32]byte { return s.DHPub }

// GenerateDHKey generates a new X25519 key pair.
func (s *RatchetState) GenerateDHKey() error {
	var scalar [32]byte
	if _, err := rand.Read(scalar[:]); err != nil {
		return fmt.Errorf("rand: %w", err)
	}
	scalar[0] &= 248
	scalar[31] &= 127
	scalar[31] |= 64

	var pub [32]byte
	curve25519.ScalarBaseMult(&pub, &scalar)

	copy(s.DHSecret[:], scalar[:])
	copy(s.DHPub[:], pub[:])
	return nil
}

// InitAlice is called by the initiator (Alice) to set up initial state.
// dhOutput is the shared secret from ECDH(my_X25519_SK, their_X25519_PK).
func (s *RatchetState) InitAlice(dhOutput [32]byte) error {
	if err := s.GenerateDHKey(); err != nil {
		return err
	}

	// Derive root key and initial send chain key from ECDH output
	var kdfErr error
	s.RootKey, s.SendChainKey, _, kdfErr = kdfRootChain(dhOutput, s.RootKey, nil)
	if kdfErr != nil {
		return fmt.Errorf("init alice kdf: %w", kdfErr)
	}
	s.origRootKey = s.RootKey
	s.firstDH = true

	s.SendCount = 0
	s.RecvCount = 0
	s.Initialized = true
	return nil
}

// InitBobWithSS initializes Bob's state from the static ECDH shared secret.
// Bob derives initial root key and receive chain key WITHOUT doing DH ratchet.
// The DH ratchet happens in FinishRatchet AFTER the first message decrypts.
func (s *RatchetState) InitBobWithSS(staticSS [32]byte) error {
	rk, ck, _, err := kdfRootChain(staticSS, [32]byte{}, nil)
	if err != nil {
		return fmt.Errorf("init bob root kdf: %w", err)
	}
	s.RootKey = rk
	s.origRootKey = rk
	s.ReceiveChainKey = ck
	s.firstDH = true
	s.SendCount = 0
	s.RecvCount = 0
	s.Initialized = true
	return nil
}

// FinishRatchet is called AFTER the first ReceiveMsg1 succeeds.
// It performs Bob's DH ratchet: computes recv chain from ECDH(B1_SK, A1_PK),
// generates B2 keypair, and computes send chain from the SAME DH output (symmetric ratchet).
func (s *RatchetState) FinishRatchet(theirPub [32]byte) error {
	// Save B1_SK before GenerateDHKey overwrites it
	var b1SK [32]byte
	copy(b1SK[:], s.DHSecret[:])

	// Compute ECDH(B1_SK, A1_PK) for recv chain
	dhOut, err := curve25519.X25519(s.DHSecret[:], theirPub[:])
	if err != nil {
		return fmt.Errorf("dh ecdh: %w", err)
	}
	if isZero(dhOut) {
		return fmt.Errorf("low-order point")
	}
	var dh [32]byte
	copy(dh[:], dhOut)

	// Generate B2 keypair
	if err := s.GenerateDHKey(); err != nil {
		return fmt.Errorf("bob dh key: %w", err)
	}

	// Symmetric ratchet: BOTH chains use SAME dh output and origRootKey base
	// Step 1: recv chain = kdfRootChain(ECDH(B1_SK, A1_PK), origRootKey)
	rk, ck, _, err := kdfRootChain(dh, s.origRootKey, nil)
	if err != nil {
		return fmt.Errorf("finish ratchet kdf: %w", err)
	}
	s.RootKey = rk
	s.ReceiveChainKey = ck

	// Step 2: send chain = kdfRootChain(ECDH(B2_SK, A1_PK), origRootKey)
	// Due to commutativity: ECDH(B2_SK, A1_PK) = ECDH(A1_SK, B2_PK)
	var dhSend [32]byte
	curve25519.ScalarMult(&dhSend, &s.DHSecret, &theirPub)
	rkSend, ckSend, _, err := kdfRootChain(dhSend, s.origRootKey, nil)
	if err != nil {
		return fmt.Errorf("send chain kdf: %w", err)
	}
	s.RootKey = rkSend
	s.SendChainKey = ckSend

	s.TheirDHPub = theirPub
	// Do NOT set firstDH = false here — FinishRatchet is NOT a full dhRatchet.
	// Bob's first DH ratchet via dhRatchet() should still use origRootKey as base.
	s.RecvCount = 0
	s.SendCount = 0
	return nil
}

// ReceiveMsg1 decrypts the FIRST message from Alice using the original receive chain key
// (derived from static ECDH SS), BEFORE performing any DH ratchet.
// Returns the plaintext and the sender's DH public key.
func (s *RatchetState) ReceiveMsg1(msg []byte) ([]byte, [32]byte, error) {
	if !s.Initialized {
		return nil, [32]byte{}, fmt.Errorf("ratchet not initialized")
	}
	if len(msg) < 40 {
		return nil, [32]byte{}, fmt.Errorf("message too short")
	}

	var theirPub [32]byte
	copy(theirPub[:], msg[:32])
	s.TheirDHPub = theirPub

	msgNum := int(msg[36])<<24 | int(msg[37])<<16 | int(msg[38])<<8 | int(msg[39])
	if msgNum != s.RecvCount {
		return nil, [32]byte{}, fmt.Errorf("message number mismatch: got %d, want %d", msgNum, s.RecvCount)
	}

	msgKey, nextChainKey, err := kdfMessageKey(s.ReceiveChainKey)
	if err != nil {
		return nil, [32]byte{}, fmt.Errorf("message key: %w", err)
	}

	ct := msg[40:]
	plaintext, err := decryptWithKey(msgKey, ct)
	if err != nil {
		return nil, [32]byte{}, fmt.Errorf("decrypt: %w", err)
	}

	s.ReceiveChainKey = nextChainKey
	s.RecvCount++
	return plaintext, theirPub, nil
}

// Send encrypts a plaintext and returns a DrMessage, advancing the send chain.
func (s *RatchetState) Send(plaintext []byte) (*DrMessage, error) {
	if !s.Initialized {
		return nil, fmt.Errorf("ratchet not initialized")
	}

	msgKey, nextChainKey, err := kdfMessageKey(s.SendChainKey)
	if err != nil {
		return nil, fmt.Errorf("message key: %w", err)
	}

	header := DrHeader{
		DHPub:  s.DHPub,
		PN:     0,
		MsgNum: s.SendCount,
	}

	ct, err := encryptWithKey(msgKey, plaintext)
	if err != nil {
		return nil, fmt.Errorf("encrypt: %w", err)
	}

	s.SendChainKey = nextChainKey
	s.SendCount++

	return &DrMessage{Header: header, Ciphertext: ct}, nil
}

// Receive decrypts a DrMessage and advances the receive chain.
// If the header's DH pubkey differs from our stored TheirDHPub, it performs
// a DH ratchet step first.
func (s *RatchetState) Receive(m *DrMessage) ([]byte, error) {
	if !constantTimeEq(m.Header.DHPub[:], s.TheirDHPub[:]) {
		if err := s.dhRatchet(m.Header.DHPub); err != nil {
			return nil, fmt.Errorf("dh ratchet: %w", err)
		}
	}

	if s.RecvCount != m.Header.MsgNum {
		return nil, fmt.Errorf("message number mismatch: got %d, want %d", m.Header.MsgNum, s.RecvCount)
	}

	msgKey, nextChainKey, err := kdfMessageKey(s.ReceiveChainKey)
	if err != nil {
		return nil, fmt.Errorf("message key: %w", err)
	}

	plaintext, err := decryptWithKey(msgKey, m.Ciphertext)
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w", err)
	}

	s.ReceiveChainKey = nextChainKey
	s.RecvCount++

	return plaintext, nil
}

// dhRatchet performs the DH ratchet step when receiving a new DH pubkey from peer.
// Symmetric + DH ratchet from Signal spec.
// For the first DH ratchet, uses origRootKey as base for both chains (symmetric ratchet).
func (s *RatchetState) dhRatchet(theirNewDHPub [32]byte) error {
	// Step 1: Save current DHSecret (pre-ratchet secret) for recv chain DH
	var skPre [32]byte
	copy(skPre[:], s.DHSecret[:])

	// Step 2: ECDH(current_DH_SK, theirNewDHPub) for recv chain
	var dhOutput [32]byte
	curve25519.ScalarMult(&dhOutput, &s.DHSecret, &theirNewDHPub)
	// For first DH ratchet, use origRootKey as base (Signal spec symmetric ratchet)
	rootKeyBase := s.RootKey
	if s.firstDH && s.origRootKey != [32]byte{} {
		rootKeyBase = s.origRootKey
	}
	// Step 3: recv chain = kdfRootChain(dhOutput, rootKeyBase)
	newRootKey, newReceiveChainKey, _, err := kdfRootChain(dhOutput, rootKeyBase, nil)
	if err != nil {
		return err
	}
	// Step 4: Update root key and receive chain key
	s.RootKey = newRootKey
	s.ReceiveChainKey = newReceiveChainKey
	s.RecvCount = 0

	// Step 5: Save dhOutput for send chain (before GenerateDHKey overwrites DHSecret)
	var savedDHOutput [32]byte
	copy(savedDHOutput[:], dhOutput[:])

	// Step 6: Generate new DH keypair (this overwrites DHSecret)
	if err := s.GenerateDHKey(); err != nil {
		return err
	}
	// Step 7: send chain = kdfRootChain(ECDH(new_DH_SK, theirNewDHPub), rootKeyBase)
	// Use the NEW DHSecret (A2_SK) after GenerateDHKey, with theirNewDHPub (B2_PK).
	// For msg2: alice uses ECDH(A2_SK, B2_PK), bob uses ECDH(B2_SK, A2_PK).
	// Due to commutativity: ECDH(A2_SK, B2_PK) = ECDH(B2_SK, A2_PK) ✓
	var dhSend [32]byte
	curve25519.ScalarMult(&dhSend, &s.DHSecret, &theirNewDHPub)
		newRootKey2, newSendChainKey, _, err := kdfRootChain(dhSend, rootKeyBase, nil)
	if err != nil {
		return err
	}
	s.RootKey = newRootKey2
	s.SendChainKey = newSendChainKey
	s.SendCount = 0

	s.firstDH = false
	s.Initialized = true
		return nil
}

// ---- Key Derivation Functions ----

// kdfRootChain derives a new root key and chain key from a DH output.
// Returns (newRootKey, newChainKey, _, error).
// ikm = currentRootKey || dhOutput (64 bytes total).
func kdfRootChain(dhOutput [32]byte, currentRootKey [32]byte, info []byte) ([32]byte, [32]byte, [32]byte, error) {
	ikm := make([]byte, 0, 64)
	ikm = append(ikm, currentRootKey[:]...)
	ikm = append(ikm, dhOutput[:]...)

	info2 := make([]byte, 0, len(hkdfInfoRoot)+len(info))
	info2 = append(info2, hkdfInfoRoot...)
	info2 = append(info2, info...)

	prk := hkdf.New(sha256.New, ikm, nil, info2)
	out := make([]byte, 64)
	n, err := io.ReadFull(prk, out)
	if err != nil || n != 64 {
		return [32]byte{}, [32]byte{}, [32]byte{}, fmt.Errorf("hkdf: %w", err)
	}
	var rk, ck [32]byte
	copy(rk[:], out[:32])
	copy(ck[:], out[32:64])
	return rk, ck, [32]byte{}, nil
}

// kdfMessageKey derives a message key and next chain key from a chain key.
func kdfMessageKey(chainKey [32]byte) (msgKey [32]byte, nextChainKey [32]byte, err error) {
	prk := hkdf.New(sha256.New, chainKey[:], nil, hkdfInfoMsg)
	if _, err := io.ReadFull(prk, msgKey[:]); err != nil {
		return [32]byte{}, [32]byte{}, fmt.Errorf("msg key hkdf: %w", err)
	}

	// Next chain key: HMAC-SHA256(chainKey, 0x01) — Signal spec
	h := hmac.New(sha256.New, chainKey[:])
	h.Write([]byte{0x01})
	copy(nextChainKey[:], h.Sum(nil))

	return msgKey, nextChainKey, nil
}

// encryptWithKey encrypts plaintext using ChaCha20-Poly1305 with the given 32-byte key.
func encryptWithKey(key [32]byte, plaintext []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(key[:])
	if err != nil {
		return nil, fmt.Errorf("chacha20poly1305: %w", err)
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	ct := aead.Seal(nil, nonce, plaintext, nil)
	return append(nonce, ct...), nil
}

// decryptWithKey decrypts ciphertext using ChaCha20-Poly1305 with the given 32-byte key.
func decryptWithKey(key [32]byte, ciphertext []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(key[:])
	if err != nil {
		return nil, fmt.Errorf("chacha20poly1305: %w", err)
	}
	nonceSize := aead.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, fmt.Errorf("ciphertext too short")
	}
	nonce, ct := ciphertext[:nonceSize], ciphertext[nonceSize:]
	return aead.Open(nil, nonce, ct, nil)
}

// constantTimeEq compares two byte slices in constant time.
func constantTimeEq(a, b []byte) bool {
	return subtle.ConstantTimeCompare(a, b) == 1
}

// isZero checks if a byte slice is all zeros.
func isZero(b []byte) bool {
	for _, v := range b {
		if v != 0 {
			return false
		}
	}
	return true
}

// Serialize serializes the full RatchetState to a byte slice for persistence.
// It includes all fields (including unexported firstDH and origRootKey).
func (s *RatchetState) Serialize() []byte {
	// Layout:
	// [32] DHSecret
	// [32] DHPub
	// [32] TheirDHPub
	// [32] RootKey
	// [32] origRootKey
	// [32] SendChainKey
	// [32] ReceiveChainKey
	// [4]  SendCount (big-endian)
	// [4]  RecvCount (big-endian)
	// [1]  Initialized (0 or 1)
	// [1]  firstDH (0 or 1)
	// = 234 bytes
	buf := make([]byte, 234)
	copy(buf[0:32], s.DHSecret[:])
	copy(buf[32:64], s.DHPub[:])
	copy(buf[64:96], s.TheirDHPub[:])
	copy(buf[96:128], s.RootKey[:])
	copy(buf[128:160], s.origRootKey[:])
	copy(buf[160:192], s.SendChainKey[:])
	copy(buf[192:224], s.ReceiveChainKey[:])
	binary.BigEndian.PutUint32(buf[224:228], uint32(s.SendCount))
	binary.BigEndian.PutUint32(buf[228:232], uint32(s.RecvCount))
	if s.Initialized {
		buf[232] = 1
	}
	if s.firstDH {
		buf[233] = 1
	}
	return buf
}

// DeserializeRatchetState restores a RatchetState from a byte slice produced by Serialize.
func DeserializeRatchetState(data []byte) (RatchetState, error) {
	if len(data) < 234 {
		return RatchetState{}, fmt.Errorf("ratchet state data too short: need 234, got %d", len(data))
	}
	var s RatchetState
	copy(s.DHSecret[:], data[0:32])
	copy(s.DHPub[:], data[32:64])
	copy(s.TheirDHPub[:], data[64:96])
	copy(s.RootKey[:], data[96:128])
	copy(s.origRootKey[:], data[128:160])
	copy(s.SendChainKey[:], data[160:192])
	copy(s.ReceiveChainKey[:], data[192:224])
	s.SendCount = int(binary.BigEndian.Uint32(data[224:228]))
	s.RecvCount = int(binary.BigEndian.Uint32(data[228:232]))
	s.Initialized = data[232] != 0
	s.firstDH = data[233] != 0
	return s, nil
}

// SerializeHeader serializes DrHeader into a byte slice.
func SerializeHeader(h DrHeader) []byte {
	buf := make([]byte, 32+4+4)
	copy(buf[:32], h.DHPub[:])
	binary.BigEndian.PutUint32(buf[32:36], uint32(h.PN))
	binary.BigEndian.PutUint32(buf[36:40], uint32(h.MsgNum))
	return buf
}

// DeserializeHeader deserializes a DrHeader from a byte slice.
func DeserializeHeader(b []byte) (DrHeader, error) {
	if len(b) < 40 {
		return DrHeader{}, fmt.Errorf("header too short: need 40, got %d", len(b))
	}
	var h DrHeader
	copy(h.DHPub[:], b[:32])
	h.PN = int(binary.BigEndian.Uint32(b[32:36]))
	h.MsgNum = int(binary.BigEndian.Uint32(b[36:40]))
	return h, nil
}

var _ = rand.Reader // used by encryptWithKey
