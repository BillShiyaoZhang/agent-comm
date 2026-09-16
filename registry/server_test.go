package registry

import (
	"context"
	"crypto/ed25519"
	"errors"
	"io"
	"testing"
	"time"

	agentpb "github.com/BillShiyaoZhang/agent-comm/proto"
	libp2p "github.com/libp2p/go-libp2p"
	p2pcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	goproto "google.golang.org/protobuf/proto"
)

func registerRecord(s Store, r *agentpb.RegisterRequest) error {
	return s.RegisterWithSignature(r.Urn, r.PeerId, r.Addrs, r.RelayAddrs, r.X25519Pubkey, r.Ed25519Pubkey, r.Signature, r.StoresUserData, r.Timestamp)
}

func storedRecord(s Store, urn string) (*agentpb.RegisterRequest, bool) {
	pid, addrs, relays, xpk, epk, sig, policy, ts, found := s.ResolveExtended(urn)
	return &agentpb.RegisterRequest{Urn: urn, PeerId: pid, Addrs: addrs, RelayAddrs: relays, X25519Pubkey: xpk, Ed25519Pubkey: epk, Signature: sig, StoresUserData: policy, Timestamp: ts}, found
}

func assertStoredRecord(t *testing.T, s Store, expected *agentpb.RegisterRequest) {
	t.Helper()
	actual, found := storedRecord(s, expected.Urn)
	if !found || !goproto.Equal(actual, expected) {
		t.Fatalf("stored record changed: found=%v\nactual=%v\nexpected=%v", found, actual, expected)
	}
}

func TestInMemoryStoreRegistrationOwnership(t *testing.T) {
	owner, key := signedRequest(t, time.Now().Unix())
	for _, attack := range invalidRegistrations(t, owner, key) {
		t.Run(attack.name, func(t *testing.T) {
			bad := cloneRequest(owner)
			attack.edit(bad)
			for _, occupied := range []bool{false, true} {
				store := NewInMemoryStore()
				if occupied {
					if err := registerRecord(store, owner); err != nil {
						t.Fatal(err)
					}
				}
				if err := registerRecord(store, bad); err == nil {
					t.Fatalf("attack accepted (occupied=%v)", occupied)
				}
				if occupied {
					assertStoredRecord(t, store, owner)
				} else if len(store.ListURNs()) != 0 {
					t.Fatal("rejected record was persisted")
				}
			}
		})
	}
}

func TestInMemoryStoreSignedUpdateAndSliceIsolation(t *testing.T) {
	owner, key := signedRequest(t, time.Now().Unix())
	owner.Urn = "urn:example:agent:" + owner.Urn[len("urn:agent-comm:agent:"):]
	signRequest(owner, key)
	expected := cloneRequest(owner)
	store := NewInMemoryStore()
	if err := registerRecord(store, owner); err != nil {
		t.Fatal(err)
	}
	owner.X25519Pubkey[0] ^= 1
	owner.Ed25519Pubkey[0] ^= 1
	owner.Signature[0] ^= 1
	owner.Addrs[0] = "/ip4/127.0.0.1/tcp/20001"
	owner.RelayAddrs[0] = "/ip4/127.0.0.1/tcp/20002"
	assertStoredRecord(t, store, expected)
	result, _ := storedRecord(store, expected.Urn)
	result.X25519Pubkey[0] ^= 1
	result.Ed25519Pubkey[0] ^= 1
	result.Signature[0] ^= 1
	result.Addrs[0] = "/ip4/127.0.0.1/tcp/30001"
	result.RelayAddrs[0] = "/ip4/127.0.0.1/tcp/30002"
	_, _, xpk, _ := store.Resolve(expected.Urn)
	xpk[0] ^= 1
	assertStoredRecord(t, store, expected)
	update := cloneRequest(expected)
	update.X25519Pubkey[0] ^= 1
	update.StoresUserData = true
	update.Addrs[0] = "/ip4/127.0.0.1/tcp/40001"
	signRequest(update, key)
	if err := registerRecord(store, update); err != nil {
		t.Fatalf("owner update rejected: %v", err)
	}
	assertStoredRecord(t, store, update)
}

// An intentionally permissive custom store ensures the server itself validates.
type uncheckedStore struct {
	Store
	writes int
}

func (s *uncheckedStore) RegisterWithSignature(string, string, []string, []string, []byte, []byte, []byte, bool, int64) error {
	s.writes++
	return nil
}

func TestServerValidatesBeforeCustomStore(t *testing.T) {
	owner, key := signedRequest(t, time.Now().Unix())
	store := &uncheckedStore{}
	server := NewServer(nil, store)
	for _, attack := range invalidRegistrations(t, owner, key) {
		t.Run(attack.name, func(t *testing.T) {
			bad := cloneRequest(owner)
			attack.edit(bad)
			if ok, _ := server.handleRegister(bad); ok {
				t.Fatal("invalid registration reached custom store")
			}
		})
	}
	if ok, _ := server.handleRegister(nil); ok {
		t.Fatal("nil request accepted")
	}
	if store.writes != 0 {
		t.Fatalf("custom store received %d invalid writes", store.writes)
	}
	if ok, info := server.handleRegister(owner); !ok {
		t.Fatalf("valid registration rejected: %s", info)
	}
	if store.writes != 1 {
		t.Fatalf("custom store received %d writes, want 1", store.writes)
	}
}

func TestUnsignedRegistrationAPIsReject(t *testing.T) {
	r, _ := signedRequest(t, time.Now().Unix())
	pid, _ := peer.Decode(r.PeerId)
	store := NewInMemoryStore()
	if ok, info := store.Register(r.Urn, r.PeerId, r.Addrs, r.X25519Pubkey); ok || info == "" {
		t.Fatal("unsigned store Register accepted")
	}
	if err := store.HandleRegister(r.Urn, pid, nil, r.X25519Pubkey); !errors.Is(err, ErrUnsignedRegistration) {
		t.Fatalf("unsigned store HandleRegister: %v", err)
	}
	if len(store.ListURNs()) != 0 {
		t.Fatal("unsigned APIs mutated store")
	}
	if err := NewServer(nil, &uncheckedStore{}).HandleRegister(r.Urn, pid, nil, r.X25519Pubkey); !errors.Is(err, ErrUnsignedRegistration) {
		t.Fatalf("unsigned server HandleRegister: %v", err)
	}
	if err := NewClient(nil).Register(peer.AddrInfo{}, r.Urn, nil, r.X25519Pubkey); !errors.Is(err, ErrUnsignedRegistration) {
		t.Fatalf("unsigned client Register: %v", err)
	}
}

func registryTestHost(t *testing.T, key ed25519.PrivateKey) host.Host {
	t.Helper()
	options := []libp2p.Option{libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0")}
	if key != nil {
		priv, err := p2pcrypto.UnmarshalEd25519PrivateKey(key)
		if err != nil {
			t.Fatal(err)
		}
		options = append(options, libp2p.Identity(priv))
	}
	h, err := libp2p.New(options...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	return h
}

func sendRegistration(t *testing.T, publisher, server host.Host, r *agentpb.RegisterRequest) *agentpb.RegisterResponse {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := publisher.Connect(ctx, peer.AddrInfo{ID: server.ID(), Addrs: server.Addrs()}); err != nil {
		t.Fatal(err)
	}
	stream, err := publisher.NewStream(ctx, server.ID(), protocol.ID(ProtoID))
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if err := stream.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	data, err := goproto.Marshal(&agentpb.URNRegistryRequest{Op: &agentpb.URNRegistryRequest_Register{Register: r}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := stream.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	data, err = io.ReadAll(stream)
	if err != nil {
		t.Fatal(err)
	}
	var response agentpb.URNRegistryResponse
	if err := goproto.Unmarshal(data, &response); err != nil {
		t.Fatal(err)
	}
	result := response.GetRegister()
	if result == nil {
		t.Fatalf("unexpected response: %v", &response)
	}
	return result
}

func TestP2PRegistrationRejectsForgeryAndAllowsOwnerPublication(t *testing.T) {
	owner, key := signedRequest(t, time.Now().Unix())
	serverHost := registryTestHost(t, nil)
	publisher := registryTestHost(t, nil)
	ownerHost := registryTestHost(t, key)
	store := NewInMemoryStore()
	NewServer(serverHost, store).Register()
	if publisher.ID().String() == owner.PeerId {
		t.Fatal("test requires a separate publisher")
	}
	if response := sendRegistration(t, ownerHost, serverHost, owner); !response.Ok {
		t.Fatalf("owner-signed publication rejected: %s", response.Info)
	}
	assertStoredRecord(t, store, owner)
	// The record signature omits addresses; a copied signature must not let
	// another peer replace its owner's routing hints.
	replayed := cloneRequest(owner)
	replayed.Addrs = []string{"/ip4/127.0.0.1/tcp/9999"}
	replayed.RelayAddrs = []string{"/ip4/127.0.0.1/tcp/9998"}
	if response := sendRegistration(t, publisher, serverHost, replayed); response.Ok {
		t.Fatal("third-party replay replaced unsigned routing addresses")
	}
	assertStoredRecord(t, store, owner)
	for _, attack := range invalidRegistrations(t, owner, key) {
		t.Run(attack.name, func(t *testing.T) {
			bad := cloneRequest(owner)
			attack.edit(bad)
			if response := sendRegistration(t, publisher, serverHost, bad); response.Ok || response.Info == "" {
				t.Fatal("forged P2P registration accepted")
			}
			assertStoredRecord(t, store, owner)
		})
	}
	update := cloneRequest(owner)
	update.StoresUserData = true
	update.X25519Pubkey[0] ^= 1
	signRequest(update, key)
	if response := sendRegistration(t, ownerHost, serverHost, update); !response.Ok {
		t.Fatalf("owner update rejected: %s", response.Info)
	}
	assertStoredRecord(t, store, update)
	resolved, err := NewClient(publisher).Resolve(peer.AddrInfo{ID: serverHost.ID()}, owner.Urn)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyResolveResult(owner.Urn, &resolved); err != nil {
		t.Fatalf("stored signature did not survive P2P resolution: %v", err)
	}
}

func TestInMemoryStoreRejectsRegistrationRollback(t *testing.T) {
	current, key := signedRequest(t, time.Now().Unix())
	store := NewInMemoryStore()
	if err := registerRecord(store, current); err != nil {
		t.Fatal(err)
	}
	stale := cloneRequest(current)
	stale.Timestamp--
	stale.X25519Pubkey[0] ^= 1
	signRequest(stale, key)
	if err := registerRecord(store, stale); err == nil {
		t.Fatal("valid old signature rolled back current encryption key")
	}
	assertStoredRecord(t, store, current)
}
