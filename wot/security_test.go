package wot

import (
	"bytes"
	"encoding/binary"
	"path/filepath"
	"testing"
	"time"

	"github.com/BillShiyaoZhang/agent-comm/crypto"
	"github.com/BillShiyaoZhang/agent-comm/internal/wire"
	pb "github.com/BillShiyaoZhang/agent-comm/proto"
	p2pcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

type hostileStream struct {
	network.Stream
	*bytes.Reader
	reset bool
}

func (s *hostileStream) Read(p []byte) (int, error)  { return s.Reader.Read(p) }
func (s *hostileStream) Close() error                { return nil }
func (s *hostileStream) Reset() error                { s.reset = true; return nil }
func (s *hostileStream) SetDeadline(time.Time) error { return nil }

func TestWOTRejectsHugeHeaderBeforeReadingPayload(t *testing.T) {
	for _, size := range []uint32{wire.MaxMessageSize + 1, ^uint32(0)} {
		var header [4]byte
		binary.BigEndian.PutUint32(header[:], size)
		stream := &hostileStream{Reader: bytes.NewReader(header[:])}
		HandleWOTStream(stream, nil)
		if !stream.reset {
			t.Fatal("oversized remote frame was not reset")
		}
	}
}

func TestAddClaimDoesNotDeadlockOnNetworkInput(t *testing.T) {
	keys, err := crypto.LoadOrCreateIdentity(filepath.Join(t.TempDir(), "keys"))
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(filepath.Join(t.TempDir(), "wot.db"), keys)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	publicKey, err := p2pcrypto.UnmarshalEd25519PublicKey(keys.Ed25519.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := peer.IDFromPublicKey(publicKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddKnownPeer(keys.Ed25519.URN(), pid.String(), keys.X25519PK, keys.Ed25519.PublicKey); err != nil {
		t.Fatal(err)
	}
	claim, err := NewTrustClaim(keys, keys.Ed25519.URN(), pid.String(), keys.X25519PK, pb.TrustLevel_TRUSTED)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- store.AddClaim(claim) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("adding a claim deadlocked on a nested read lock")
	}
	for _, invalid := range []*TrustClaim{nil, {}} {
		if err := store.AddClaim(invalid); err == nil {
			t.Fatal("accepted missing claim")
		}
	}
}
