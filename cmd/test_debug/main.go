// Standalone debug test mirroring test_dr step by step.
// This version adds detailed tracing to find the key mismatch.
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
	origRootKey     [32]byte
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

func main() {
	// Use a FIXED shared secret so we can reproduce exactly
	var staticSS [32]byte
	ssBytes, _ := hexToBytes("acf3c06696fd1ef0")
	copy(staticSS[:], ssBytes[:])

	alice := RatchetState{}
	bob := RatchetState{}

	// Alice InitAlice
	sk, pk := GenerateDHKey()
	alice.DHSecret = sk
	alice.DHPub = pk
	rk, ck, _, _ := kdfRootChain(staticSS, [32]byte{}, nil)
	alice.RootKey = rk
	alice.origRootKey = rk
	alice.SendChainKey = ck
	alice.SendCount = 0
	alice.RecvCount = 0
	alice.Initialized = true

	// Bob InitBobWithSS
	bobSK, bobPK := GenerateDHKey()
	bob.DHSecret = bobSK
	bob.DHPub = bobPK
	bRK, bCK, _, _ := kdfRootChain(staticSS, [32]byte{}, nil)
	bob.RootKey = bRK
	bob.origRootKey = bRK
	bob.ReceiveChainKey = bCK
	bob.SendCount = 0
	bob.RecvCount = 0
	bob.Initialized = true

	fmt.Printf("Alice: DHSecret[0:4]=%x DHPub[0:4]=%x RootKey[0:4]=%x origRootKey[0:4]=%x SendChainKey[0:4]=%x TheirDHPub=%x\n",
		alice.DHSecret[:4], alice.DHPub[:4], alice.RootKey[:4], alice.origRootKey[:4], alice.SendChainKey[:4], alice.TheirDHPub[:4])
	fmt.Printf("Bob:   DHSecret[0:4]=%x DHPub[0:4]=%x RootKey[0:4]=%x origRootKey[0:4]=%x RecvChainKey[0:4]=%x TheirDHPub=%x\n",
		bob.DHSecret[:4], bob.DHPub[:4], bob.RootKey[:4], bob.origRootKey[:4], bob.ReceiveChainKey[:4], bob.TheirDHPub[:4])
	fmt.Printf("Chain keys match: %v\n\n", alice.SendChainKey == bob.ReceiveChainKey)

	// Alice sends msg1
	fmt.Println("=== Alice sends msg1 ===")
	msgKey1, nextCK1, _ := kdfMessageKey(alice.SendChainKey)
	ct1 := encryptWithKey(msgKey1, []byte("Hello Bob!"))
	alice.SendChainKey = nextCK1
	alice.SendCount++
	fmt.Printf("  Alice msg1 key: %x\n", msgKey1[:4])
	fmt.Printf("  Alice DHPub: %x\n", alice.DHPub[:4])

	// Bob receives msg1 (ReceiveMsg1)
	fmt.Println("=== Bob receives msg1 (ReceiveMsg1) ===")
	bob.TheirDHPub = alice.DHPub
	msgKey1b, nextCK1b, _ := kdfMessageKey(bob.ReceiveChainKey)
	fmt.Printf("  Bob recvChainKey: %x -> msgKey: %x\n", bob.ReceiveChainKey[:4], msgKey1b[:4])
	pt1, err := decryptWithKey(msgKey1b, ct1)
	fmt.Printf("  Decrypt: %v, plaintext: %q\n", err, pt1)
	bob.ReceiveChainKey = nextCK1b
	bob.RecvCount++
	theirDHPub1 := alice.DHPub

	// Bob FinishRatchet(alice.DHPub)
	fmt.Println("=== Bob FinishRatchet ===")
	bobSK2, bobPK2 := GenerateDHKey()
	bob.DHSecret = bobSK2
	bob.DHPub = bobPK2
	dh1, _ := curve25519.X25519(bob.DHSecret[:], theirDHPub1[:])
	var dh1b [32]byte
	copy(dh1b[:], dh1)
	rk2, ck2, _, _ := kdfRootChain(dh1b, bob.origRootKey, nil)
	bob.RootKey = rk2
	bob.ReceiveChainKey = ck2
	bob.TheirDHPub = theirDHPub1
	bob.RecvCount = 0
	fmt.Printf("  Bob new DHPub: %x\n", bob.DHPub[:4])
	fmt.Printf("  Bob new RootKey: %x, RecvChainKey: %x\n", bob.RootKey[:4], bob.ReceiveChainKey[:4])

	// Bob sends reply1
	fmt.Println("=== Bob sends reply1 ===")
	// Step 1-6 of bob's send: DH output from current secret
	dhSend1, _ := curve25519.X25519(bob.DHSecret[:], bob.TheirDHPub[:])
	var dhSend1b [32]byte
	copy(dhSend1b[:], dhSend1)
	// Bob step 7: new RootKey = bob.RootKey (from FinishRatchet), SendChainKey = kdfRootChain(dhSend1, newRK)
	fmt.Printf("  Bob step7: dhSend1=%x newRK=%x (bob.RootKey)\n", dhSend1[:4], bob.RootKey[:4])
	// CRITICAL CHECK: dhSend1 should equal dh1 (from FinishRatchet step 2)
	fmt.Printf("  CRITICAL: dhSend1=%x dh1=%x equal=%v\n", dhSend1[:4], dh1[:4], string(dhSend1) == string(dh1))
	// What Bob's send chain key will be
	fmt.Printf("  Bob will compute: kdfRootChain(dhSend1=%x, RK=%x)\n", dhSend1[:4], bob.RootKey[:4])
	rk3, ckSend1, _, _ := kdfRootChain(dhSend1b, bob.RootKey, nil)
	fmt.Printf("  Bob send chain result: newRK=%x sendCK=%x\n", rk3[:4], ckSend1[:4])
	bob.RootKey = rk3
	bob.SendChainKey = ckSend1
	bob.SendCount = 0
	fmt.Printf("  Bob send DH: %x\n", dhSend1[:4])
	fmt.Printf("  Bob new SendChainKey: %x, RootKey: %x\n", bob.SendChainKey[:4], bob.RootKey[:4])

	msgKeyR1, nextCKR1, _ := kdfMessageKey(bob.SendChainKey)
	ctR1 := encryptWithKey(msgKeyR1, []byte("Hi Alice!"))
	bob.SendChainKey = nextCKR1
	bob.SendCount++
	fmt.Printf("  Bob reply1 msgKey: %x, DHPub: %x\n", msgKeyR1[:4], bob.DHPub[:4])

	// Alice receives reply1: dhRatchet(A1) then decrypt
	fmt.Println("=== Alice receives reply1 (dhRatchet then decrypt) ===")
	fmt.Printf("  Alice.TheirDHPub before ratchet: %x\n", alice.TheirDHPub[:4])
	fmt.Printf("  Alice.DHSecret[0:4] (A1): %x\n", alice.DHSecret[:4])

	// Step 5: ECDH(A1_SK, B2_PK) — use pre-ratchet secret BEFORE GenerateDHKey
	dhR1, _ := curve25519.X25519(alice.DHSecret[:], bob.DHPub[:]) // A1_SK * B2_PK
	var dhR1b [32]byte
	copy(dhR1b[:], dhR1)
	fmt.Printf("  DH ratchet: ECDH(A1, B2) = %x\n", dhR1[:4])

	// Save dhOutput from step 1 — needed for send chain AFTER GenerateDHKey
	var savedDHOutput [32]byte
	copy(savedDHOutput[:], dhR1[:])

	rootKeyBase2 := alice.RootKey
	if alice.origRootKey != [32]byte{} && alice.RootKey == alice.origRootKey {
		rootKeyBase2 = alice.origRootKey
		fmt.Printf("  Using origRootKey as base (first ratchet)\n")
	} else {
		fmt.Printf("  Using RootKey as base (subsequent ratchet)\n")
		fmt.Printf("  origRootKey=%x RootKey=%x equal=%v\n", alice.origRootKey[:4], alice.RootKey[:4], alice.RootKey == alice.origRootKey)
	}
	rkR1, ckR1, _, _ := kdfRootChain(dhR1b, rootKeyBase2, nil)
	alice.RootKey = rkR1
	alice.ReceiveChainKey = ckR1
	alice.TheirDHPub = bob.DHPub
	alice.RecvCount = 0

	// Generate new DH keypair for alice (A2) — this updates alice.DHSecret to A2
	skA2, pkA2 := GenerateDHKey()
	alice.DHSecret = skA2
	alice.DHPub = pkA2

	// Step 7: Send chain = HKDF(savedDHOutput, new RootKey)
	// Use savedDHOutput (A1_SK * B2_PK) to match Bob's send chain DH derivation.
	// Bob's send chain uses ECDH(B2_SK, A1_PK) = ECDH(A1_PK, B2_SK) = savedDHOutput.
	fmt.Printf("  Alice step7: savedDHOutput=%x RK=%x (alice.RootKey after recv chain update)\n", savedDHOutput[:4], alice.RootKey[:4])
	fmt.Printf("  Alice will compute: kdfRootChain(savedDHOutput=%x, RK=%x)\n", savedDHOutput[:4], alice.RootKey[:4])
	rkR2, ckR2, _, _ := kdfRootChain(savedDHOutput, alice.RootKey, nil)
	fmt.Printf("  Alice recv chain result: newRK=%x recvCK=%x\n", rkR2[:4], ckR2[:4])
	alice.RootKey = rkR2
	alice.SendChainKey = ckR2
	alice.SendCount = 0
	alice.Initialized = true
	fmt.Printf("  Alice AFTER ratchet: RootKey=%x, RecvChainKey=%x, SendChainKey=%x, DHPub=%x\n",
		alice.RootKey[:4], alice.ReceiveChainKey[:4], alice.SendChainKey[:4], alice.DHPub[:4])

	// Now decrypt with recvChainKey=ckR1
	msgKeyR1a, _, _ := kdfMessageKey(alice.ReceiveChainKey)
	fmt.Printf("  Alice recvChainKey: %x -> msgKey: %x\n", alice.ReceiveChainKey[:4], msgKeyR1a[:4])
	fmt.Printf("  Bob reply1 msgKey:   %x\n", msgKeyR1[:4])
	fmt.Printf("  Keys match: %v\n", msgKeyR1a == msgKeyR1)
	ptR1, err := decryptWithKey(msgKeyR1a, ctR1)
	fmt.Printf("  Decrypt: %v, plaintext: %q\n", err, ptR1)
}

func hexToBytes(s string) ([]byte, error) {
	result := make([]byte, len(s)/2)
	for i := 0; i < len(s)/2; i++ {
		var v byte
		hi := s[i*2]
		lo := s[i*2+1]
		for j := 0; j < 16; j++ {
			if "0123456789abcdef"[j] == hi || "0123456789ABCDEF"[j] == hi {
				v = byte(j << 4)
				break
			}
		}
		for j := 0; j < 16; j++ {
			if "0123456789abcdef"[j] == lo || "0123456789ABCDEF"[j] == lo {
				v |= byte(j)
				break
			}
		}
		result[i] = v
	}
	return result, nil
}