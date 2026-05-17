package main

import (
	"crypto/rand"
	"fmt"

	"golang.org/x/crypto/curve25519"

	"github.com/nousresearch/hermes-agent/agent-comm/dr"
)

// Simple Double Ratchet test without real crypto

func main() {
	fmt.Println("=== Double Ratchet Test ===")

	// Alice and Bob both have identity key pairs (not used in this simplified test)
	// For the test, we'll just use random DH outputs

	// Alice temp DH key
	aliceSK := [32]byte{}
	alicePK := [32]byte{}
	rand.Read(aliceSK[:])
	aliceSK[0] &= 248
	aliceSK[31] &= 127
	aliceSK[31] |= 64
	curve25519.ScalarBaseMult(&alicePK, &aliceSK)

	// Bob temp DH key (B1_SK for initial message)
	bobSK := [32]byte{}
	bobPK := [32]byte{}
	rand.Read(bobSK[:])
	bobSK[0] &= 248
	bobSK[31] &= 127
	bobSK[31] |= 64
	curve25519.ScalarBaseMult(&bobPK, &bobSK)

	// Simulate X3DH: ECDH(A1_SK, B1_PK)
	// In real system, this comes from X3DH protocol
	dhOut, _ := curve25519.X25519(aliceSK[:], bobPK[:])
	var dhOutput [32]byte
	copy(dhOutput[:], dhOut)

	fmt.Printf("\nAlice A1_PK: %x...\n", alicePK[:4])
	fmt.Printf("Bob   B1_PK: %x...\n", bobPK[:4])
	fmt.Printf("X3DH shared secret: %x...\n", dhOutput[:4])

	// Alice: init with shared secret (from X3DH)
	alice := &dr.RatchetState{}
	if err := alice.InitAlice(dhOutput); err != nil {
		fmt.Printf("[FAIL] Alice InitAlice: %v\n", err)
		return
	}
	fmt.Printf("[OK] Alice InitAlice: RootKey=%x SendChainKey=%x\n", alice.RootKey[:4], alice.SendChainKey[:4])

	// Bob: init with same shared secret (from X3DH)
	bob := &dr.RatchetState{}
	if err := bob.InitBobWithSS(dhOutput); err != nil {
		fmt.Printf("[FAIL] Bob InitBobWithSS: %v\n", err)
		return
	}
	fmt.Printf("[OK] Bob InitBobWithSS: RootKey=%x RecvChainKey=%x\n", bob.RootKey[:4], bob.ReceiveChainKey[:4])

	// Verify initial chain keys match
	if alice.SendChainKey == bob.ReceiveChainKey {
		fmt.Printf("[OK] Chain keys match (alice.SendChainKey == bob.ReceiveChainKey)\n")
	} else {
		fmt.Printf("[FAIL] Chain keys mismatch: alice=%x bob=%x\n", alice.SendChainKey[:4], bob.ReceiveChainKey[:4])
		return
	}

	// Test 1: Alice → Bob (first message, no DH ratchet)
	{
		msg1, err := alice.Send([]byte("Hello Bob! - message 1"))
		if err != nil {
			fmt.Printf("[FAIL] Alice sent msg1: %v\n", err)
			return
		}
		fmt.Printf("[OK] Alice sent msg1 (msgNum=%d, DHPub=%x...)\n", msg1.Header.MsgNum, msg1.Header.DHPub[:4])

		plaintext1, theirPub, err := bob.ReceiveMsg1(serializeDrMessage(msg1))
		if err != nil {
			fmt.Printf("[FAIL] Bob received msg1: %v\n", err)
			return
		}
		fmt.Printf("[OK] Bob received msg1: %q\n", plaintext1)

		// Bob finishes his DH ratchet using Alice's pubkey from message
		if err := bob.FinishRatchet(theirPub); err != nil {
			fmt.Printf("[FAIL] Bob FinishRatchet: %v\n", err)
			return
		}
		fmt.Printf("[OK] Bob FinishRatchet: RootKey=%x RecvChainKey=%x TheirDHPub=%x...\n",
			bob.RootKey[:4], bob.ReceiveChainKey[:4], bob.TheirDHPub[:4])
	}

	// Test 2: Bob → Alice (reply with DH pubkey)
	{
		reply1, err := bob.Send([]byte("Hi Alice! - reply to msg1"))
		if err != nil {
			fmt.Printf("[FAIL] Bob sent reply1: %v\n", err)
			return
		}
		fmt.Printf("[OK] Bob sent reply1 (msgNum=%d, DHPub=%x...)\n", reply1.Header.MsgNum, reply1.Header.DHPub[:4])

		plaintextR1, err := alice.Receive(&dr.DrMessage{
			Header:     reply1.Header,
			Ciphertext: reply1.Ciphertext,
		})
		if err != nil {
			fmt.Printf("[FAIL] Alice received reply1: %v\n", err)
			return
		}
		fmt.Printf("[OK] Alice received reply1: %q\n", plaintextR1)
	}

	// Test 3: Alice → Bob (msg2 after DH ratchet on both sides)
	{
		msg2, err := alice.Send([]byte("Hello Bob! - message 2"))
		if err != nil {
			fmt.Printf("[FAIL] Alice sent msg2: %v\n", err)
			return
		}
		fmt.Printf("[OK] Alice sent msg2 (msgNum=%d, DHPub=%x...)\n", msg2.Header.MsgNum, msg2.Header.DHPub[:4])

		plaintext2, err := bob.Receive(&dr.DrMessage{
			Header:     msg2.Header,
			Ciphertext: msg2.Ciphertext,
		})
		if err != nil {
			fmt.Printf("FAIL: bob receive msg2: %v\n", err)
			return
		}
		fmt.Printf("[OK] Bob received msg2: %q\n", plaintext2)
	}

	fmt.Println("\n=== All tests passed! ===")
}

// Helper to serialize a DrMessage for ReceiveMsg1
func serializeDrMessage(m *dr.DrMessage) []byte {
	header := dr.SerializeHeader(m.Header)
	return append(header, m.Ciphertext...)
}