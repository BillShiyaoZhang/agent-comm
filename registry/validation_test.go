package registry

import (
	"crypto/ed25519"
	"crypto/rand"
	"math"
	"testing"
	"time"

	agentpb "github.com/BillShiyaoZhang/agent-comm/proto"
	p2pcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	goproto "google.golang.org/protobuf/proto"
)

func signedRequest(t *testing.T, timestamp int64) (*agentpb.RegisterRequest, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pk, err := p2pcrypto.UnmarshalEd25519PublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := peer.IDFromPublicKey(pk)
	if err != nil {
		t.Fatal(err)
	}
	xpk := make([]byte, 32)
	if _, err := rand.Read(xpk); err != nil {
		t.Fatal(err)
	}
	r := &agentpb.RegisterRequest{
		Urn: URNFromEd25519PK(pub), PeerId: pid.String(),
		Addrs: []string{"/ip4/127.0.0.1/tcp/10001"}, RelayAddrs: []string{"/ip4/127.0.0.1/tcp/10002"},
		X25519Pubkey: xpk, Ed25519Pubkey: pub, Timestamp: timestamp,
	}
	signRequest(r, priv)
	return r, priv
}

func signRequest(r *agentpb.RegisterRequest, priv ed25519.PrivateKey) {
	r.Signature = ed25519.Sign(priv, BuildSignedMsg(r.Urn, r.PeerId, r.X25519Pubkey, r.StoresUserData, r.Timestamp))
}

func cloneRequest(r *agentpb.RegisterRequest) *agentpb.RegisterRequest {
	return goproto.Clone(r).(*agentpb.RegisterRequest)
}

type invalidRegistration struct {
	name string
	edit func(*agentpb.RegisterRequest)
}

// These attacks are run against both empty stores and existing owner records.
func invalidRegistrations(t *testing.T, owner *agentpb.RegisterRequest, ownerKey ed25519.PrivateKey) []invalidRegistration {
	t.Helper()
	attacker, attackerKey := signedRequest(t, owner.Timestamp)
	return []invalidRegistration{
		{"attacker claims owner urn", func(r *agentpb.RegisterRequest) {
			r.PeerId, r.Ed25519Pubkey = attacker.PeerId, attacker.Ed25519Pubkey
			signRequest(r, attackerKey)
		}},
		{"missing urn", func(r *agentpb.RegisterRequest) { r.Urn = "" }},
		{"malformed urn", func(r *agentpb.RegisterRequest) { r.Urn = "invalid:" + r.Urn; signRequest(r, ownerKey) }},
		{"missing peer id", func(r *agentpb.RegisterRequest) { r.PeerId = "" }},
		{"malformed peer id", func(r *agentpb.RegisterRequest) { r.PeerId = "not-a-peer"; signRequest(r, ownerKey) }},
		{"noncanonical peer id", func(r *agentpb.RegisterRequest) {
			pid, _ := peer.Decode(r.PeerId)
			r.PeerId = peer.ToCid(pid).String()
			signRequest(r, ownerKey)
		}},
		{"owner signs unrelated peer", func(r *agentpb.RegisterRequest) { r.PeerId = attacker.PeerId; signRequest(r, ownerKey) }},
		{"missing x25519 key", func(r *agentpb.RegisterRequest) { r.X25519Pubkey = nil; signRequest(r, ownerKey) }},
		{"short x25519 key", func(r *agentpb.RegisterRequest) { r.X25519Pubkey = r.X25519Pubkey[:31]; signRequest(r, ownerKey) }},
		{"long x25519 key", func(r *agentpb.RegisterRequest) { r.X25519Pubkey = append(r.X25519Pubkey, 0); signRequest(r, ownerKey) }},
		{"missing identity key", func(r *agentpb.RegisterRequest) { r.Ed25519Pubkey = nil }},
		{"short identity key", func(r *agentpb.RegisterRequest) { r.Ed25519Pubkey = r.Ed25519Pubkey[:31] }},
		{"long identity key", func(r *agentpb.RegisterRequest) { r.Ed25519Pubkey = append(r.Ed25519Pubkey, 0) }},
		{"missing signature", func(r *agentpb.RegisterRequest) { r.Signature = nil }},
		{"short signature", func(r *agentpb.RegisterRequest) { r.Signature = r.Signature[:63] }},
		{"long signature", func(r *agentpb.RegisterRequest) { r.Signature = append(r.Signature, 0) }},
		{"corrupt signature", func(r *agentpb.RegisterRequest) { r.Signature[0] ^= 1 }},
		{"attacker signature", func(r *agentpb.RegisterRequest) { signRequest(r, attackerKey) }},
		{"tampered encryption key", func(r *agentpb.RegisterRequest) { r.X25519Pubkey[0] ^= 1 }},
		{"tampered storage policy", func(r *agentpb.RegisterRequest) { r.StoresUserData = !r.StoresUserData }},
		{"tampered timestamp", func(r *agentpb.RegisterRequest) { r.Timestamp++ }},
		{"zero timestamp", func(r *agentpb.RegisterRequest) { r.Timestamp = 0; signRequest(r, ownerKey) }},
		{"negative timestamp", func(r *agentpb.RegisterRequest) { r.Timestamp = -1; signRequest(r, ownerKey) }},
		{"stale timestamp", func(r *agentpb.RegisterRequest) { r.Timestamp -= 600; signRequest(r, ownerKey) }},
		{"future timestamp", func(r *agentpb.RegisterRequest) { r.Timestamp += 600; signRequest(r, ownerKey) }},
		{"minimum timestamp", func(r *agentpb.RegisterRequest) { r.Timestamp = math.MinInt64; signRequest(r, ownerKey) }},
		{"maximum timestamp", func(r *agentpb.RegisterRequest) { r.Timestamp = math.MaxInt64; signRequest(r, ownerKey) }},
	}
}

func TestRegistrationTimestampWindow(t *testing.T) {
	const now int64 = 1800000000
	r, key := signedRequest(t, now)
	for _, tc := range []struct {
		name      string
		timestamp int64
		valid     bool
	}{
		{"past boundary", now - 300, true}, {"too old", now - 301, false},
		{"present", now, true}, {"future boundary", now + 60, true}, {"too new", now + 61, false},
		{"zero", 0, false}, {"negative", -1, false}, {"min int", math.MinInt64, false}, {"max int", math.MaxInt64, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := cloneRequest(r)
			r.Timestamp = tc.timestamp
			signRequest(r, key)
			err := validateRegistrationAt(r.Urn, r.PeerId, r.X25519Pubkey, r.Ed25519Pubkey, r.Signature, r.StoresUserData, r.Timestamp, now)
			if (err == nil) != tc.valid {
				t.Fatalf("valid = %v, error = %v", tc.valid, err)
			}
		})
	}
}

func TestVerifyResolveResultAuthenticatesPersistedRecords(t *testing.T) {
	r, key := signedRequest(t, time.Now().Unix()-86400)
	r.Urn = "urn:example:custom:" + r.Urn[len("urn:agent-comm:agent:"):]
	signRequest(r, key)
	pid, err := peer.Decode(r.PeerId)
	if err != nil {
		t.Fatal(err)
	}
	res := &ResolveResult{AddrInfo: peer.AddrInfo{ID: pid}, X25519PubKey: r.X25519Pubkey, Ed25519PubKey: r.Ed25519Pubkey, Signature: r.Signature, Timestamp: r.Timestamp}
	if err := VerifyResolveResult(r.Urn, res); err != nil {
		t.Fatalf("authentic persisted custom-namespace record rejected: %v", err)
	}
	if err := ValidateRegistration(r.Urn, r.PeerId, r.X25519Pubkey, r.Ed25519Pubkey, r.Signature, false, r.Timestamp); err == nil {
		t.Fatal("stale record was accepted as a new registration")
	}
	res.Signature = nil
	if err := VerifyResolveResult(r.Urn, res); err == nil {
		t.Fatal("unsigned resolve result accepted")
	}
	if err := VerifyResolveResult(r.Urn, nil); err == nil {
		t.Fatal("nil resolve result accepted")
	}
}
