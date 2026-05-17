package main

import (
	"crypto/sha256"
	"fmt"
	"io"
	"golang.org/x/crypto/hkdf"
)

var hkdfInfoRoot = []byte("DoubleRatchet")

func kdfRootChain(dhOutput [32]byte, currentRootKey [32]byte) ([32]byte, [32]byte) {
	ikm := make([]byte, 0, 64)
	ikm = append(ikm, currentRootKey[:]...)
	ikm = append(ikm, dhOutput[:]...)
	prk := hkdf.New(sha256.New, ikm, nil, hkdfInfoRoot)
	out := make([]byte, 64)
	n, err := io.ReadFull(prk, out)
	if err != nil || n != 64 {
		return [32]byte{}, [32]byte{}
	}
	var rk, ck [32]byte
	copy(rk[:], out[:32])
	copy(ck[:], out[32:64])
	return rk, ck
}

func main() {
	dh := [32]byte{0x83, 0x4f, 0x26, 0xc6, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	rk := [32]byte{0xe5, 0x4d, 0xfd, 0x95, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}

	r1, ck1 := kdfRootChain(dh, rk)
	fmt.Printf("Call 1: rk=%x ck=%x\n", r1[:4], ck1[:4])

	r2, ck2 := kdfRootChain(dh, rk)
	fmt.Printf("Call 2: rk=%x ck=%x\n", r2[:4], ck2[:4])

	fmt.Printf("Same output: %v\n", ck1 == ck2)
}
