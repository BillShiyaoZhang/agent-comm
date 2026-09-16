package mq

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/BillShiyaoZhang/agent-comm/crypto"
	pb "github.com/BillShiyaoZhang/agent-comm/proto"
	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	goproto "google.golang.org/protobuf/proto"
)

func TestSQLiteMailboxOwnershipAndStableRetry(t *testing.T) {
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "mq.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a, _ := crypto.GenerateIdentityKeyPair()
	b, _ := crypto.GenerateIdentityKeyPair()
	aCtx := WithAuthenticatedPublicKey(context.Background(), a.PublicKey)
	bCtx := WithAuthenticatedPublicKey(context.Background(), b.PublicKey)
	env := &pb.EncryptedEnvelope{
		RecipientUrn: b.URN(), MessageId: "stable-id", SenderStaticPubkey: make([]byte, 32),
		EphemeralPubkey: make([]byte, 32), Nonce: make([]byte, 12), Tag: make([]byte, 16), Ciphertext: []byte("opaque"),
	}
	if err := crypto.SignEnvelope(env, a); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StoreEnvelope(bCtx, b.URN(), env, 0); err == nil {
		t.Fatal("unauthorized sender accepted")
	}
	if _, err := s.StoreEnvelope(aCtx, b.URN(), env, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StoreEnvelope(aCtx, b.URN(), env, 0); err != nil {
		t.Fatalf("idempotent retry: %v", err)
	}
	if _, err := s.Retrieve(aCtx, b.URN()); err == nil {
		t.Fatal("unauthorized recipient retrieved mailbox")
	}
	if _, err := s.Ack(aCtx, b.URN(), []string{env.MessageId}); err == nil {
		t.Fatal("unauthorized ACK succeeded")
	}
	if n, err := s.Ack(aCtx, a.URN(), []string{env.MessageId}); err != nil || n != 0 {
		t.Fatalf("ACK crossed mailbox: %d %v", n, err)
	}
	conflict := goproto.Clone(env).(*pb.EncryptedEnvelope)
	conflict.Ciphertext = []byte("different")
	if err := crypto.SignEnvelope(conflict, a); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StoreEnvelope(aCtx, b.URN(), conflict, 0); err == nil {
		t.Fatal("conflicting ID replaced stored envelope")
	}
	pending, err := s.Retrieve(bCtx, b.URN())
	if err != nil || len(pending) != 1 || !bytes.Equal(pending[0].Ciphertext, env.Ciphertext) {
		t.Fatalf("stored envelope changed: %v", err)
	}
	if n, err := s.Ack(bCtx, b.URN(), []string{env.MessageId, "missing"}); err != nil || n != 1 {
		t.Fatalf("multi-ID ACK: %d %v", n, err)
	}
	if _, err := s.StoreEnvelope(aCtx, b.URN(), env, 0); err != nil {
		t.Fatal(err)
	}
	if pending, err := s.Retrieve(bCtx, b.URN()); err != nil || len(pending) != 0 {
		t.Fatalf("retry resurrected acknowledged envelope: %d %v", len(pending), err)
	}
}

func TestMQFrameAllocationIsBounded(t *testing.T) {
	for _, size := range []uint32{0, MaxFrameSize + 1, ^uint32(0)} {
		buf := make([]byte, 4)
		binary.BigEndian.PutUint32(buf, size)
		if _, err := readMQRequest(bytes.NewReader(buf)); err == nil {
			t.Fatalf("request accepted invalid size %d", size)
		}
		if _, err := readMQResponse(bytes.NewReader(buf)); err == nil {
			t.Fatalf("response accepted invalid size %d", size)
		}
	}
}

func TestSQLiteRejectsExpiredAndCapsUnboundedRetention(t *testing.T) {
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "mq.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	sender, _ := crypto.GenerateIdentityKeyPair()
	recipient, _ := crypto.GenerateIdentityKeyPair()
	ctx := WithAuthenticatedPublicKey(context.Background(), sender.PublicKey)
	for i, expiry := range []int64{-1, time.Now().Unix() - 1, math.MaxInt64, 0} {
		env := &pb.EncryptedEnvelope{
			RecipientUrn: recipient.URN(), MessageId: fmt.Sprintf("expiry-%d", i),
			SenderStaticPubkey: make([]byte, 32), EphemeralPubkey: make([]byte, 32),
			Nonce: make([]byte, 12), Tag: make([]byte, 16), Ciphertext: []byte("opaque"),
		}
		if err := crypto.SignEnvelope(env, sender); err != nil {
			t.Fatal(err)
		}
		_, err := s.StoreEnvelope(ctx, recipient.URN(), env, expiry)
		if i < 2 {
			if err == nil {
				t.Fatal("accepted negative or expired retention")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		var saved int64
		if err := s.db.QueryRow("SELECT expiry FROM messages WHERE id = ?", env.MessageId).Scan(&saved); err != nil {
			t.Fatal(err)
		}
		if saved <= time.Now().Unix() || saved > time.Now().Unix()+7*24*60*60 {
			t.Fatalf("uncapped retention: %d", saved)
		}
	}
}

func TestSQLiteRetrievePagesByBytesWithoutLosingMessages(t *testing.T) {
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "mq.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	sender, _ := crypto.GenerateIdentityKeyPair()
	recipient, _ := crypto.GenerateIdentityKeyPair()
	sendContext := WithAuthenticatedPublicKey(context.Background(), sender.PublicKey)
	receiveContext := WithAuthenticatedPublicKey(context.Background(), recipient.PublicKey)
	for i := 0; i < 8; i++ {
		env := &pb.EncryptedEnvelope{
			RecipientUrn: recipient.URN(), MessageId: fmt.Sprintf("large-%d", i),
			SenderStaticPubkey: make([]byte, 32), EphemeralPubkey: make([]byte, 32),
			Nonce: make([]byte, 12), Tag: make([]byte, 16), Ciphertext: make([]byte, 750000),
		}
		if err := crypto.SignEnvelope(env, sender); err != nil {
			t.Fatal(err)
		}
		if _, err := s.StoreEnvelope(sendContext, recipient.URN(), env, 0); err != nil {
			t.Fatal(err)
		}
	}
	seen := make(map[string]bool)
	for len(seen) < 8 {
		batch, err := s.Retrieve(receiveContext, recipient.URN())
		if err != nil || len(batch) == 0 || len(batch) >= 8 {
			t.Fatalf("invalid bounded batch: %d, %v", len(batch), err)
		}
		size := 0
		var ids []string
		for _, env := range batch {
			if seen[env.MessageId] {
				t.Fatalf("ACKed message returned: %s", env.MessageId)
			}
			seen[env.MessageId] = true
			ids = append(ids, env.MessageId)
			size += goproto.Size(env)
		}
		if size > maxRetrievePayload {
			t.Fatalf("retrieval exceeded byte budget: %d", size)
		}
		if n, err := s.Ack(receiveContext, recipient.URN(), ids); err != nil || n != len(ids) {
			t.Fatalf("ACK batch: %d, %v", n, err)
		}
	}
}

func TestMQContextCancellationInterruptsUnresponsiveRelay(t *testing.T) {
	relay, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	h, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	info := peer.AddrInfo{ID: relay.ID(), Addrs: relay.Addrs()}
	if err := h.Connect(context.Background(), info); err != nil {
		t.Fatal(err)
	}
	c := NewClient(h)
	for _, operation := range []string{"store", "retrieve", "ack"} {
		t.Run(operation, func(t *testing.T) {
			entered := make(chan struct{})
			relay.SetStreamHandler(ProtoID, func(s network.Stream) {
				defer s.Close()
				close(entered)
				_, _ = io.Copy(io.Discard, s) // accept request without sending a response
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				var err error
				switch operation {
				case "store":
					_, err = c.Store(ctx, info, "recipient", &pb.EncryptedEnvelope{}, 0)
				case "retrieve":
					_, err = c.Retrieve(ctx, info, "recipient")
				case "ack":
					_, err = c.Ack(ctx, info, []string{"message-id"})
				}
				done <- err
			}()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("stream did not open")
			}
			cancel()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("unresponsive request succeeded")
				}
			case <-time.After(time.Second):
				t.Fatal("canceled MQ operation remained blocked reading relay response")
			}
		})
	}
}
