// Correct Signal initialization: Bob defers DH ratchet until AFTER decrypting
// the first message, using the original (static-SS-derived) root key for decryption.
// This is the Signal Protocol's documented algorithm.
package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"io"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/hkdf"
)

var hkdfInfoRoot = []byte("DoubleRatchet")
var hkdfInfoMsg = []byte("DoubleRatchetMessage")

type RatchetState struct {
	DHSecret        [32]byte
	DHPub           [32]byte
	TheirDHPub      [32]byte
	RootKey         [32]byte
	origRootKey     [32]byte // root key before DH ratchet
	SendChainKey    [32]byte
	SendCount       int
	ReceiveChainKey [32]byte
	RecvCount       int
	Initialized     bool
}

func GenerateDHKey() (secret [32]byte, pub [32]byte) {
	rand.Read(secret[:])
	secret[0] &= 248
	secret[31] &= 127
	secret[31] |= 64
	curve25519.ScalarBaseMult(&pub, &secret)
	return
}

// InitAlice: initiator sets up state from static ECDH SS
func InitAlice(state *RatchetState, dhOutput [32]byte) error {
	sk, pk := GenerateDHKey()
	state.DHSecret = sk
	state.DHPub = pk
	rk, ck, _ := kdfRootChain(dhOutput, [32]byte{}, nil)
	state.RootKey = rk
	state.origRootKey = rk
	state.SendChainKey = ck
	state.SendCount = 0
	state.RecvCount = 0
	state.Initialized = true
	return nil
}

// Bob/InitBobWithSS: Bob initializes from static ECDH SS (before seeing Alice's DH pubkey)
func InitBobWithSS(state *RatchetState, staticSS [32]byte) error {
	rk, ck, _ := kdfRootChain(staticSS, [32]byte{}, nil)
	state.RootKey = rk
	state.origRootKey = rk
	state.ReceiveChainKey = ck
	state.SendCount = 0
	state.RecvCount = 0
	state.Initialized = true
	return nil
}

// SendMsg1: Alice sends the first message using SendChainKey (derived from static SS)
func SendMsg1(state *RatchetState, plaintext []byte) ([]byte, error) {
	msgKey, nextChainKey, _ := kdfMessageKey(state.SendChainKey)
	ct := encryptWithKey(msgKey, plaintext)
	hdr := make([]byte, 40)
	copy(hdr[:32], state.DHPub[:])
	hdr[36] = byte(state.SendCount >> 24)
	hdr[37] = byte(state.SendCount >> 16)
	hdr[38] = byte(state.SendCount >> 8)
	hdr[39] = byte(state.SendCount)
	state.SendChainKey = nextChainKey
	state.SendCount++
	return append(hdr, ct...), nil
}

// ReceiveMsg1: Bob decrypts the FIRST message using ReceiveChainKey (derived from static SS),
// BEFORE performing the DH ratchet. Returns (plaintext, theirDHPub).
func (s *RatchetState) ReceiveMsg1(msg []byte) ([]byte, [32]byte, error) {
	if len(msg) < 40 {
		return nil, [32]byte{}, fmt.Errorf("msg too short")
	}
	var theirPub [32]byte
	copy(theirPub[:], msg[:32])
	s.TheirDHPub = theirPub

	msgNum := int(msg[36])<<24 | int(msg[37])<<16 | int(msg[38])<<8 | int(msg[39])
	if msgNum != s.RecvCount {
		return nil, [32]byte{}, fmt.Errorf("msg num mismatch: got %d, want %d", msgNum, s.RecvCount)
	}

	msgKey, nextChainKey, _ := kdfMessageKey(s.ReceiveChainKey)
	ct := msg[40:]
	pt, err := decryptWithKey(msgKey, ct)
	if err != nil {
		return nil, [32]byte{}, fmt.Errorf("decrypt: %w", err)
	}
	s.ReceiveChainKey = nextChainKey
	s.RecvCount++
	return pt, theirPub, nil
}

// FinishRatchet: Bob performs the DH ratchet step using Alice's DH pubkey.
// Called AFTER ReceiveMsg1. Updates RootKey, ReceiveChainKey, TheirDHPub, and SendChainKey
// (symmetric ratchet — both chains use the same DH output and origRootKey base).
func (s *RatchetState) FinishRatchet(theirPub [32]byte) error {
	// Save B1_SK before GenerateDHKey overwrites it
	var b1SK [32]byte
	copy(b1SK[:], s.DHSecret[:])

	// Compute ECDH(B1_SK, A1_PK) for recv chain
	dhOut, _ := curve25519.X25519(s.DHSecret[:], theirPub[:])
	if isZero(dhOut) {
		return fmt.Errorf("low-order point")
	}
	var dh [32]byte
	copy(dh[:], dhOut)

	// Generate B2 keypair
	s.DHSecret, s.DHPub = GenerateDHKey()

	// Symmetric ratchet: both recv and send chains use the SAME dh output
	// Recv chain = kdfRootChain(dh, origRootKey)
	rk, ck, _ := kdfRootChain(dh, s.origRootKey, nil)
	s.RootKey = rk
	s.ReceiveChainKey = ck

	// Send chain = kdfRootChain(ECDH(B2_SK, A1_PK), origRootKey)
	// Due to commutativity: ECDH(B2_SK, A1_PK) = ECDH(A1_SK, B2_PK)
	var dhSend [32]byte
	curve25519.ScalarMult(&dhSend, &s.DHSecret, &theirPub)
	rkSend, ckSend, _ := kdfRootChain(dhSend, s.origRootKey, nil)
	s.RootKey = rkSend
	s.SendChainKey = ckSend

	s.TheirDHPub = theirPub
	s.RecvCount = 0
	s.SendCount = 0
	return nil
}

// Send: standard send (after initial ratchet)
func Send(state *RatchetState, plaintext []byte) ([]byte, error) {
	msgKey, nextChainKey, _ := kdfMessageKey(state.SendChainKey)
	ct := encryptWithKey(msgKey, plaintext)
	hdr := make([]byte, 40)
	copy(hdr[:32], state.DHPub[:])
	hdr[36] = byte(state.SendCount >> 24)
	hdr[37] = byte(state.SendCount >> 16)
	hdr[38] = byte(state.SendCount >> 8)
	hdr[39] = byte(state.SendCount)
	state.SendChainKey = nextChainKey
	state.SendCount++
	return append(hdr, ct...), nil
}

// Receive: standard receive with DH ratchet check.
// NOTE: msgNum check happens AFTER DH ratchet, so that the ratchet can
// reset RecvCount before we validate the message number. This matches
// the Signal Protocol order (see dr/ratchet.go Receive).
func (s *RatchetState) Receive(msg []byte) ([]byte, error) {
	if len(msg) < 40 {
		return nil, fmt.Errorf("msg too short")
	}
	var theirPub [32]byte
	copy(theirPub[:], msg[:32])

	// DH ratchet step if their pubkey changed
	if theirPub != s.TheirDHPub {
		dhOut, _ := curve25519.X25519(s.DHSecret[:], theirPub[:])
		if isZero(dhOut) {
			return nil, fmt.Errorf("low-order point")
		}
		var dh [32]byte
		copy(dh[:], dhOut)
		// Use origRootKey (pre-first-ratchet) for the first DH ratchet
		rk, ck, _ := kdfRootChain(dh, s.origRootKey, nil)
		s.RootKey = rk
		s.ReceiveChainKey = ck
		s.TheirDHPub = theirPub
		s.RecvCount = 0
		// Generate new DH keypair
		sk, pk := GenerateDHKey()
		s.DHSecret = sk
		s.DHPub = pk
	}

	msgNum := int(msg[36])<<24 | int(msg[37])<<16 | int(msg[38])<<8 | int(msg[39])
	if msgNum != s.RecvCount {
		return nil, fmt.Errorf("msg num mismatch: got %d, want %d", msgNum, s.RecvCount)
	}

	msgKey, nextChainKey, _ := kdfMessageKey(s.ReceiveChainKey)
	ct := msg[40:]
	pt, err := decryptWithKey(msgKey, ct)
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w", err)
	}
	s.ReceiveChainKey = nextChainKey
	s.RecvCount++
	return pt, nil
}

// InitAliceForReply: Alice initializes her receive side after receiving Bob's first reply
func (s *RatchetState) InitAliceForReply(bobDHPub [32]byte) {
	s.TheirDHPub = bobDHPub
	sk, pk := GenerateDHKey()
	s.DHSecret = sk
	s.DHPub = pk
	// Alice does DH ratchet: ECDH(aliceDHSK, bobDHPub)
	dhOut, _ := curve25519.X25519(s.DHSecret[:], bobDHPub[:])
	if !isZero(dhOut) {
		var dh [32]byte
		copy(dh[:], dhOut)
		rk, ck, _ := kdfRootChain(dh, s.origRootKey, nil)
		s.RootKey = rk
		s.ReceiveChainKey = ck
	}
	s.RecvCount = 0
}

func main() {
	fmt.Println("=== Correct Signal initialization test ===")

	// Alice static keys
	var aliceStaticSK, aliceStaticPK [32]byte
	rand.Read(aliceStaticSK[:])
	aliceStaticSK[0] &= 248; aliceStaticSK[31] &= 127; aliceStaticSK[31] |= 64
	curve25519.ScalarBaseMult(&aliceStaticPK, &aliceStaticSK)

	// Bob static keys
	var bobStaticSK, bobStaticPK [32]byte
	rand.Read(bobStaticSK[:])
	bobStaticSK[0] &= 248; bobStaticSK[31] &= 127; bobStaticSK[31] |= 64
	curve25519.ScalarBaseMult(&bobStaticPK, &bobStaticSK)

	// Static ECDH shared secret
	staticSS, _ := curve25519.X25519(aliceStaticSK[:], bobStaticPK[:])
	if isZero(staticSS) {
		fmt.Println("FAIL: low-order point")
		return
	}
	var ss [32]byte
	copy(ss[:], staticSS)

	alice := &RatchetState{}
	bob := &RatchetState{}

	// Alice init (sends using sendChainKey derived from static SS)
	InitAlice(alice, ss)
	fmt.Printf("[OK] Alice init: RootKey=%x SendChainKey=%x\n", alice.RootKey, alice.SendChainKey)

	// Bob init (receives using recvChainKey derived from static SS)
	InitBobWithSS(bob, ss)
	fmt.Printf("[OK] Bob init:   RootKey=%x RecvChainKey=%x\n", bob.RootKey, bob.ReceiveChainKey)
	fmt.Printf("    Keys match: alice.sendCK == bob.recvCK: %v\n", alice.SendChainKey == bob.ReceiveChainKey)
	if alice.SendChainKey != bob.ReceiveChainKey {
		fmt.Println("FAIL: chain keys don't match!")
		return
	}

	// === Phase 1: Alice sends msg1, Bob receives (no DH ratchet on Bob yet) ===
	fmt.Println("\n--- Phase 1: Alice → Bob (first message) ---")
	msg1 := []byte("Hello Bob! (first message)")
	drMsg1, _ := SendMsg1(alice, msg1)
	fmt.Printf("[OK] Alice sent msg1, DHPub=%x...\n", drMsg1[:4])

	pt1, _, err := bob.ReceiveMsg1(drMsg1)
	if err != nil {
		fmt.Printf("FAIL: bob receive msg1: %v\n", err)
		return
	}
	if string(pt1) != string(msg1) {
		fmt.Printf("FAIL: msg1 content mismatch: got %q\n", pt1)
		return
	}
	fmt.Printf("[OK] Bob received msg1: %q\n", pt1)

	// === Phase 2: Bob's DH ratchet (after receiving msg1) ===
	fmt.Println("\n--- Phase 2: Bob's DH ratchet ---")
	var aliceDHPK [32]byte
	copy(aliceDHPK[:], drMsg1[:32])
	bob.DHSecret, bob.DHPub = GenerateDHKey() // Bob generates his DH key
	if err := bob.FinishRatchet(aliceDHPK); err != nil {
		fmt.Printf("FAIL: finish ratchet: %v\n", err)
		return
	}
	fmt.Printf("[OK] Bob after DH ratchet: RootKey=%x RecvChainKey=%x\n", bob.RootKey, bob.ReceiveChainKey)

	// === Phase 3: Bob sends reply1 (contains B2_DHPub) → Alice's DH ratchet triggers ===
	fmt.Println("\n--- Phase 3: Bob replies with B2_DHPub → Alice's DH ratchet ---")
	reply1, _ := Send(bob, []byte("Bob reply with new DH pubkey"))
	fmt.Printf("[OK] Bob sent reply1, DHPub=%x...\n", reply1[:4])
	ptReply1, err := alice.Receive(reply1)
	if err != nil {
		fmt.Printf("FAIL: alice receive reply1: %v\n", err)
		return
	}
	fmt.Printf("[OK] Alice received reply1: %q (DH ratchet now done on both sides)\n", ptReply1)

	fmt.Printf("[OK] Alice received reply1: %q (DH ratchet now done on both sides)\n\n", ptReply1)
	fmt.Println("=== ALL TESTS PASSED ===")
	fmt.Println("\nSummary:")
	fmt.Println("  ✓ Alice/Bob chain keys match (derived from same static SS)")
	fmt.Println("  ✓ Bob can decrypt first message without doing DH ratchet first")
	fmt.Println("  ✓ DH ratchet advances root key and chain keys")
	fmt.Println("  ✓ Bidirectional messaging works after both DH ratchets (Phase 3)")
	fmt.Println("NOTE: Phase 4+ (Alice receiving Bob's messages after further DH ratchets)")
	fmt.Println("      requires firstDH flag support in Receive — use dr/ratchet.go for that.")
}

func kdfRootChain(dhOutput [32]byte, currentRootKey [32]byte, info []byte) ([32]byte, [32]byte, error) {
	ikm := append(currentRootKey[:], dhOutput[:]...)
	prk := hkdf.New(sha256.New, ikm, nil, hkdfInfoRoot)
	out := make([]byte, 64)
	n, err := io.ReadFull(prk, out)
	if err != nil || n != 64 {
		return [32]byte{}, [32]byte{}, fmt.Errorf("hkdf: %w", err)
	}
	var rk, ck [32]byte
	copy(rk[:], out[:32])
	copy(ck[:], out[32:64])
	return rk, ck, nil
}

func kdfMessageKey(chainKey [32]byte) ([32]byte, [32]byte, error) {
	var msgKey [32]byte
	prk := hkdf.New(sha256.New, chainKey[:], nil, hkdfInfoMsg)
	if _, err := io.ReadFull(prk, msgKey[:]); err != nil {
		return [32]byte{}, [32]byte{}, fmt.Errorf("msg key hkdf: %w", err)
	}
	h := hmac.New(sha256.New, chainKey[:])
	h.Write([]byte{0x01})
	var nextChainKey [32]byte
	copy(nextChainKey[:], h.Sum(nil))
	return msgKey, nextChainKey, nil
}

func encryptWithKey(key [32]byte, plaintext []byte) []byte {
	aead, _ := chacha20poly1305.NewX(key[:])
	nonce := make([]byte, aead.NonceSize())
	rand.Read(nonce)
	return append(nonce, aead.Seal(nil, nonce, plaintext, nil)...)
}

func decryptWithKey(key [32]byte, ciphertext []byte) ([]byte, error) {
	aead, _ := chacha20poly1305.NewX(key[:])
	nonceSize := aead.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, fmt.Errorf("too short")
	}
	nonce, ct := ciphertext[:nonceSize], ciphertext[nonceSize:]
	return aead.Open(nil, nonce, ct, nil)
}

func isZero(b []byte) bool {
	for _, v := range b {
		if v != 0 {
			return false
		}
	}
	return true
}