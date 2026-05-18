// Test: WoT (Web of Trust) - trust claims + transitive trust path resolution.
// Scenario: Alice trusts Bob directly, Carol trusts Alice, so Carol can transitively trust Bob.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/nousresearch/hermes-agent/agent-comm/crypto"
	"github.com/nousresearch/hermes-agent/agent-comm/libp2p"
	"github.com/nousresearch/hermes-agent/agent-comm/proto"
	"github.com/nousresearch/hermes-agent/agent-comm/wot"
)

func main() {
	fmt.Println("=== WoT: Trust Claims + Trust Path Resolution ===\n")

	ctx := context.Background()

	// Clean up
	os.RemoveAll("/tmp/wot_alice_db")
	os.RemoveAll("/tmp/wot_bob_db")
	os.RemoveAll("/tmp/wot_carol_db")
	os.RemoveAll("/tmp/wot_dave_db")

	// --- Create 4 identities ---
	aliceKeys, _ := crypto.LoadOrCreateIdentity("/tmp/wot_alice_keys")
	bobKeys, _ := crypto.LoadOrCreateIdentity("/tmp/wot_bob_keys")
	carolKeys, _ := crypto.LoadOrCreateIdentity("/tmp/wot_carol_keys")
	daveKeys, _ := crypto.LoadOrCreateIdentity("/tmp/wot_dave_keys")

	fmt.Printf("Alice: %s\n", aliceKeys.Ed25519.URN())
	fmt.Printf("Bob:   %s\n", bobKeys.Ed25519.URN())
	fmt.Printf("Carol: %s\n", carolKeys.Ed25519.URN())
	fmt.Printf("Dave:  %s\n\n", daveKeys.Ed25519.URN())

	// --- Set up hosts (needed for resolver) ---
	aliceCfg := libp2p.Config{
		ListenAddrs:  []string{"/ip4/127.0.0.1/tcp/0"},
		EnableRelay:  true,
		PrivKeyBytes: aliceKeys.Ed25519.PrivateKey,
	}
	aliceHost, _ := libp2p.NewHost(aliceCfg)
	defer aliceHost.Close()

	bobCfg := libp2p.Config{
		ListenAddrs:  []string{"/ip4/127.0.0.1/tcp/0"},
		EnableRelay:  true,
		PrivKeyBytes: bobKeys.Ed25519.PrivateKey,
	}
	bobHost, _ := libp2p.NewHost(bobCfg)
	defer bobHost.Close()

	// --- Alice creates Bob's trust claim (direct trust) ---
	fmt.Println("--- Alice creates TRUSTED claim about Bob ---")
	aliceStore, _ := wot.NewStore("/tmp/wot_alice_db", aliceKeys)
	defer aliceStore.Close()

	// Alice knows Bob's identity info from out-of-band verification
	bobX25519PK := bobKeys.X25519PK
	bobPeerID := bobHost.ID().String()

	aliceClaim, err := wot.NewDirectTrustClaim(aliceKeys, bobKeys.Ed25519.URN(), bobPeerID, bobX25519PK)
	if err != nil {
		fmt.Printf("FAIL: create claim: %v\n", err)
		return
	}
	fmt.Printf("Alice's claim: %s -> %s (level=TRUSTED)\n", aliceClaim.IssuerUrn, aliceClaim.SubjectUrn)
	fmt.Printf("  Signature size: %d bytes\n", len(aliceClaim.IssuerSignature))

	// Add Bob to Alice's known peers so she can verify signatures
	aliceStore.AddKnownPeer(bobKeys.Ed25519.URN(), bobPeerID, bobX25519PK, bobKeys.Ed25519.PublicKey)

	// Store Alice's own claim
	if err := aliceStore.AddMyClaim(aliceClaim); err != nil {
		fmt.Printf("FAIL: store claim: %v\n", err)
		return
	}
	fmt.Println("  Alice stored her own claim (self-signed)\n")

	// --- Carol creates Alice's trust claim ---
	fmt.Println("--- Carol creates TRUSTED claim about Alice ---")
	carolStore, _ := wot.NewStore("/tmp/wot_carol_db", carolKeys)
	defer carolStore.Close()

	aliceX25519PK := aliceKeys.X25519PK
	carolClaim, err := wot.NewDirectTrustClaim(carolKeys, aliceKeys.Ed25519.URN(), aliceHost.ID().String(), aliceX25519PK)
	if err != nil {
		fmt.Printf("FAIL: create Carol's claim: %v\n", err)
		return
	}

	// Carol knows Alice's key (out-of-band)
	carolStore.AddKnownPeer(aliceKeys.Ed25519.URN(), aliceHost.ID().String(), aliceX25519PK, aliceKeys.Ed25519.PublicKey)
	carolStore.AddMyClaim(carolClaim)
	fmt.Printf("Carol's claim: %s -> %s (level=TRUSTED)\n\n", carolClaim.IssuerUrn, carolClaim.SubjectUrn)

	// --- Now Carol wants to trust Bob via transitive trust: Carol -> Alice -> Bob ---
	// But Carol only has Alice in her store. Bob is not there.
	// In real system, Carol would fetch via network. Here we simulate by adding Bob's info.
	fmt.Println("--- Simulating transitive trust: Carol -> Alice -> Bob ---")

	// Carol's store needs Alice's Ed25519 PK to verify Alice's claim about Bob
	// But Alice's claim about Bob is in Alice's store, not Carol's.
	// For testing, we add Alice as known peer to Carol's store
	carolStore.AddKnownPeer(aliceKeys.Ed25519.URN(), aliceHost.ID().String(), aliceX25519PK, aliceKeys.Ed25519.PublicKey)

	// Also add Bob's info so Carol can build the path
	carolStore.AddKnownPeer(bobKeys.Ed25519.URN(), bobPeerID, bobX25519PK, bobKeys.Ed25519.PublicKey)

	// Now Carol creates her own claim about Alice (she already did above)
	// But what about Alice's claim about Bob? Carol doesn't have it locally.
	// Let's add it manually to simulate the network fetch
	carolStore.AddKnownPeer(bobKeys.Ed25519.URN(), bobPeerID, bobX25519PK, bobKeys.Ed25519.PublicKey)

	// --- Test 1: Alice verifies her own claim (self-trust) ---
	fmt.Println("--- Test 1: Self-trust verification ---")
	selfTrustPath, err := aliceStore.ListTrustedPeers()
	if err != nil {
		fmt.Printf("FAIL: list trusted: %v\n", err)
		return
	}
	fmt.Printf("Alice trusts: %v\n", selfTrustPath)

	// --- Test 2: Alice verifies Bob's claim (direct trust) ---
	fmt.Println("\n--- Test 2: Direct trust - Alice trusts Bob ---")
	claimsAboutBob, err := aliceStore.GetClaimsAbout(bobKeys.Ed25519.URN())
	if err != nil {
		fmt.Printf("FAIL: get claims about Bob: %v\n", err)
		return
	}
	fmt.Printf("Claims about Bob: %d\n", len(claimsAboutBob))
	for _, c := range claimsAboutBob {
		fmt.Printf("  From %s: level=%s\n", c.IssuerUrn, c.Level.String())
	}

	trustedPK := claimsAboutBob[0].TrustedPubkey()
	if trustedPK == nil {
		fmt.Println("FAIL: Bob's claim not TRUSTED")
		return
	}
	fmt.Printf("  Bob's trusted X25519 PK: %x\n", trustedPK[:8])
	fmt.Println("  [OK] Alice directly trusts Bob\n")

	// --- Test 3: Verify claim signature (Ed25519) ---
	fmt.Println("--- Test 3: Signature verification ---")
	aliceEd25519PK := aliceKeys.Ed25519.PublicKey
	err = aliceClaim.Verify(aliceEd25519PK)
	if err != nil {
		fmt.Printf("FAIL: signature verify: %v\n", err)
		return
	}
	fmt.Println("  [OK] Alice's signature on Bob's claim is valid\n")

	// --- Test 4: Trust path resolver (BFS) ---
	fmt.Println("--- Test 4: Trust path resolver ---")
	resolver := wot.NewResolver(aliceHost, aliceStore)

	// Self-trust
	path, err := resolver.FindTrustPathSimple(ctx, aliceKeys.Ed25519.URN())
	if err != nil || path == nil {
		fmt.Printf("FAIL: self trust path: %v\n", err)
		return
	}
	fmt.Printf("  Self-trust path to Alice: depth=%d\n", path.Depth)

	// Direct trust: Alice -> Bob
	path, err = resolver.FindTrustPathSimple(ctx, bobKeys.Ed25519.URN())
	if err != nil {
		fmt.Printf("FAIL: Alice->Bob trust path: %v\n", err)
		return
	}
	fmt.Printf("  Alice -> Bob: depth=%d, trusted PK[0:8]=%x\n", path.Depth, path.TrustedPK[:8])
	if path.Depth != 1 {
		fmt.Printf("FAIL: expected depth=1, got %d\n", path.Depth)
		return
	}
	if len(path.Claims) != 1 {
		fmt.Printf("FAIL: expected 1 claim, got %d\n", len(path.Claims))
		return
	}
	if path.Claims[0].SubjectUrn != bobKeys.Ed25519.URN() {
		fmt.Printf("FAIL: claim subject mismatch\n")
		return
	}
	fmt.Println("  [OK] Direct trust path Alice->Bob verified\n")

	// --- Test 5: Transitive trust Carol -> Alice -> Bob ---
	fmt.Println("--- Test 5: Transitive trust Carol -> Alice -> Bob ---")
	// Carol's resolver needs a host for self-trust check; use aliceHost (not used for trust path)
	carolResolver := wot.NewResolver(aliceHost, carolStore)

	// Carol's direct trust in Alice
	path, err = carolResolver.FindTrustPathSimple(ctx, aliceKeys.Ed25519.URN())
	if err != nil {
		fmt.Printf("FAIL: Carol->Alice trust path: %v\n", err)
		return
	}
	fmt.Printf("  Carol -> Alice: depth=%d\n", path.Depth)

	// Carol's transitive trust in Bob (via Alice)
	// This won't work with local store only since Alice's claim about Bob is not in Carol's store
	// Simulate by adding Alice's claim about Bob to Carol's store (network fetch simulation)
	// Carol needs Alice's Ed25519 PK in her store to verify Alice's claim about Bob
	carolStore.AddKnownPeer(aliceKeys.Ed25519.URN(), aliceHost.ID().String(), aliceX25519PK, aliceKeys.Ed25519.PublicKey)
	carolStore.AddKnownPeer(carolKeys.Ed25519.URN(), aliceHost.ID().String(), carolKeys.X25519PK, carolKeys.Ed25519.PublicKey)
	carolStore.AddMyClaim(aliceClaim) // Carol fetches and stores Alice's claim
	path, err = carolResolver.FindTrustPathSimple(ctx, bobKeys.Ed25519.URN())
	if err != nil {
		fmt.Printf("FAIL: Carol->Bob (transitive) trust path: %v\n", err)
		return
	}
	fmt.Printf("  Carol -> Alice -> Bob: depth=%d\n", path.Depth)
	if path.Depth != 2 {
		fmt.Printf("FAIL: expected depth=2, got %d\n", path.Depth)
		return
	}
	fmt.Println("  [OK] Transitive trust path verified\n")

	// --- Test 6: Untrusted path (Dave doesn't trust anyone) ---
	fmt.Println("--- Test 6: Untrusted path (Dave has no trust relationships) ---")
	daveStore, _ := wot.NewStore("/tmp/wot_dave_db", daveKeys)
	defer daveStore.Close()
	daveResolver := wot.NewResolver(aliceHost, daveStore)

	_, err = daveResolver.FindTrustPathSimple(ctx, aliceKeys.Ed25519.URN())
	if err == nil {
		fmt.Println("FAIL: Dave should not trust Alice")
		return
	}
	fmt.Printf("  Dave cannot trust Alice (no trust path): %v\n", err)
	fmt.Println("  [OK] Correctly rejected untrusted path\n")

	// --- Test 7: Verify signature on transitive path ---
	fmt.Println("--- Test 7: Verify signatures on full trust path ---")
	// Carol -> Alice -> Bob path
	carolResolver2 := wot.NewResolver(aliceHost, carolStore)
	path, err = carolResolver2.FindTrustPathSimple(ctx, bobKeys.Ed25519.URN())
	if err != nil {
		fmt.Printf("FAIL: find path: %v\n", err)
		return
	}

	getPK := func(urn string) ([]byte, error) {
		_, _, ed25519PK, err := carolStore.GetKnownPeer(urn)
		return ed25519PK, err
	}
	err = wot.VerifyTrustPath(path.Claims, getPK)
	if err != nil {
		fmt.Printf("FAIL: verify path: %v\n", err)
		return
	}
	fmt.Printf("  Verified %d claim(s) in path\n", len(path.Claims))
	fmt.Println("  [OK] Full trust path signature verified\n")

	// --- Test 8: Claim expiration ---
	fmt.Println("--- Test 8: Claim expiration check ---")
	// Create an expired claim
	oldClaim := &wot.TrustClaim{
		TrustClaim: &proto.TrustClaim{
			IssuerUrn:       carolKeys.Ed25519.URN(),
			SubjectUrn:      aliceKeys.Ed25519.URN(),
			SubjectPeerId:   aliceHost.ID().String(),
			SubjectX25519Pk: aliceX25519PK,
			Level:           proto.TrustLevel_TRUSTED,
			IssuedAtUnix:    1609459200, // 2021-01-01 - definitely old
			IssuerSignature: carolClaim.IssuerSignature, // reuse sig for test
		},
	}
	if !oldClaim.IsExpired(365 * 24 * time.Hour) {
		fmt.Println("FAIL: claim should be expired")
		return
	}
	fmt.Printf("  Old claim (2021) correctly identified as expired\n")
	if oldClaim.IsExpired(10 * 365 * 24 * time.Hour) {
		fmt.Println("FAIL: 10-year old claim should not be expired in this test")
		return
	}
	fmt.Println("  [OK] Claim expiration works correctly\n")

	fmt.Println("=== ALL TESTS PASSED ===")
}