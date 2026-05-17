//go:build ignore

package main

import (
	"crypto/rand"
	"fmt"
	"golang.org/x/crypto/curve25519"
)

func GenerateDHKey() (secret [32]byte, pub [32]byte) {
	rand.Read(secret[:])
	secret[0] &= 248
	secret[31] &= 127
	secret[31] |= 64
	curve25519.ScalarBaseMult(&pub, &secret)
	return
}

func main() {
	a1SK, a1PK := GenerateDHKey()
	fmt.Printf("A1_SK=%x\nA1_PK=%x\n", a1SK[:4], a1PK[:4])
	b1SK, b1PK := GenerateDHKey()
	fmt.Printf("B1_SK=%x\nB1_PK=%x\n", b1SK[:4], b1PK[:4])

	var dh [32]byte
	curve25519.ScalarMult(&dh, &b1SK, &a1PK)
	fmt.Printf("Bob dh (B1_SK * A1_PK)=%x\n", dh[:4])

	b2SK, b2PK := GenerateDHKey()
	fmt.Printf("B2_SK=%x\nB2_PK=%x\n", b2SK[:4], b2PK[:4])

	var dhSend [32]byte
	curve25519.ScalarMult(&dhSend, &b2SK, &a1PK)
	fmt.Printf("Bob dhSend (B2_SK * A1_PK)=%x\n", dhSend[:4])

	var dh2 [32]byte
	curve25519.ScalarMult(&dh2, &a1SK, &b2PK)
	fmt.Printf("Alice dh2 (A1_SK * B2_PK)=%x\n", dh2[:4])

	if dhSend == dh2 {
		fmt.Println("MATCH: dhSend == dh2")
	} else {
		fmt.Println("MISMATCH: dhSend != dh2")
	}

	var dhAB, dhBA [32]byte
	curve25519.ScalarMult(&dhAB, &a1SK, &b2PK)
	curve25519.ScalarMult(&dhBA, &b2SK, &a1PK)
	if dhAB == dhBA {
		fmt.Printf("Direct check PASS: %x\n", dhAB[:4])
	} else {
		fmt.Printf("Direct check FAIL: %x vs %x\n", dhAB[:4], dhBA[:4])
	}
}
