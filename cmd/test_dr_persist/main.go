// Test DR persistence: serialize/deserialize round-trip via SQLite store.
package main

import (
	"crypto/rand"
	"fmt"
	"os"

	"github.com/nousresearch/hermes-agent/agent-comm/dr"
)

func main() {
	tmp := os.TempDir() + "/dr_persist_test.db"
	os.Remove(tmp)
	defer os.Remove(tmp)

	// Create SQLite-backed store
	store, err := dr.NewDRStore(tmp)
	if err != nil {
		fmt.Println("FAIL: NewDRStore:", err)
		os.Exit(1)
	}
	defer store.Close()

	// Simulate Alice init
	var aliceSS [32]byte
	rand.Read(aliceSS[:])
	alice := &dr.RatchetState{}
	if err := alice.InitAlice(aliceSS); err != nil {
		fmt.Println("FAIL: InitAlice:", err)
		os.Exit(1)
	}

	const peerURN = "urn:dr:test-peer"
	const peerID = "test-peer-id"

	// Save Alice's state
	if err := store.SaveSession(peerURN, peerID, alice); err != nil {
		fmt.Println("FAIL: SaveSession:", err)
		os.Exit(1)
	}
	fmt.Println("OK: SaveSession")

	// Load into new instance
	loaded, ok, err := store.LoadSession(peerURN)
	if err != nil {
		fmt.Println("FAIL: LoadSession:", err)
		os.Exit(1)
	}
	if !ok {
		fmt.Println("FAIL: LoadSession returned not found")
		os.Exit(1)
	}
	fmt.Println("OK: LoadSession")

	// Verify serialized bytes match
	orig := alice.Serialize()
	restored := loaded.Serialize()
	if string(orig) != string(restored) {
		fmt.Println("FAIL: state mismatch after restore")
		os.Exit(1)
	}
	fmt.Println("OK: Serialize round-trip matches")

	// Simulate a send to advance state
	_, err = alice.Send([]byte("hello world"))
	if err != nil {
		fmt.Println("FAIL: alice.Send:", err)
		os.Exit(1)
	}

	// Save updated state
	if err := store.SaveSession(peerURN, peerID, alice); err != nil {
		fmt.Println("FAIL: SaveSession (update):", err)
		os.Exit(1)
	}
	fmt.Println("OK: SaveSession (updated)")

	// Load again and verify send count advanced
	loaded2, ok, err := store.LoadSession(peerURN)
	if err != nil || !ok {
		fmt.Println("FAIL: LoadSession (update):", err)
		os.Exit(1)
	}
	if loaded2.SendCount != 1 {
		fmt.Println("FAIL: expected SendCount=1, got", loaded2.SendCount)
		os.Exit(1)
	}
	fmt.Println("OK: SendCount persisted correctly")

	// Delete and verify gone
	if err := store.DeleteSession(peerURN); err != nil {
		fmt.Println("FAIL: DeleteSession:", err)
		os.Exit(1)
	}
	_, ok, err = store.LoadSession(peerURN)
	if err != nil {
		fmt.Println("FAIL: LoadSession after delete:", err)
		os.Exit(1)
	}
	if ok {
		fmt.Println("FAIL: session still found after delete")
		os.Exit(1)
	}
	fmt.Println("OK: DeleteSession")

	fmt.Println("\n=== ALL TESTS PASSED ===")
}