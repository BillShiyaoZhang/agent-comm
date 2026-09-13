package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/BillShiyaoZhang/agent-comm/crypto"
	pb "github.com/BillShiyaoZhang/agent-comm/proto"
	"github.com/BillShiyaoZhang/agent-comm/session"
	goproto "google.golang.org/protobuf/proto"
)

func durableTestKeys(t *testing.T) *crypto.IdentityKeys {
	t.Helper()
	ed, err := crypto.GenerateIdentityKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	sk, pk, err := crypto.GenerateX25519KeyPair()
	if err != nil {
		t.Fatal(err)
	}
	return &crypto.IdentityKeys{Ed25519: ed, X25519SK: sk, X25519PK: pk}
}

func TestDurableHandlerAcknowledgesOnlyPersistedAuthenticatedMessages(t *testing.T) {
	sender, receiver := durableTestKeys(t), durableTestKeys(t)
	env, err := session.NewManager(nil, sender).BuildEnvelopeForRecipient(receiver.Ed25519.URN(), receiver.X25519PK, "work", "durable-id")
	if err != nil {
		t.Fatal(err)
	}
	agent := &Agent{Keys: receiver, Session: session.NewManager(nil, receiver)}
	var acked []string
	ack := func(ids []string) error { acked = append(acked, ids...); return nil }
	storageFailure := errors.New("inbox disk full")
	err = agent.processEnvelopes(context.Background(), []*pb.EncryptedEnvelope{env}, func(_ *pb.EncryptedEnvelope, _ []byte) error { return storageFailure }, ack)
	if !errors.Is(err, storageFailure) || len(acked) != 0 {
		t.Fatalf("failed persistence acknowledged: %v %v", err, acked)
	}
	called := 0
	persist := func(envelope *pb.EncryptedEnvelope, payload []byte) error {
		called++
		if envelope.MessageId != "durable-id" || envelope.SenderUrn != sender.Ed25519.URN() {
			t.Fatalf("missing authenticated metadata: %v", envelope)
		}
		var msg pb.ChatMessage
		if err := goproto.Unmarshal(payload, &msg); err != nil {
			t.Fatal(err)
		}
		if msg.GetText().Text != "work" {
			t.Fatal("incorrect plaintext")
		}
		return nil
	}
	forged := goproto.Clone(env).(*pb.EncryptedEnvelope)
	forged.SenderUrn = receiver.Ed25519.URN()
	if err := agent.processEnvelopes(context.Background(), []*pb.EncryptedEnvelope{forged}, persist, ack); err == nil {
		t.Fatal("forged sender accepted")
	}
	if called != 0 || len(acked) != 0 {
		t.Fatal("forged sender reached persistence or ACK")
	}
	if err := agent.processEnvelopes(context.Background(), []*pb.EncryptedEnvelope{env}, persist, ack); err != nil {
		t.Fatal(err)
	}
	if called != 1 || len(acked) != 1 || acked[0] != "durable-id" {
		t.Fatalf("bad durable ACK: %d %v", called, acked)
	}
}

func TestDurableHandlerPropagatesAckFailureAndCancellation(t *testing.T) {
	sender, receiver := durableTestKeys(t), durableTestKeys(t)
	env, err := session.NewManager(nil, sender).BuildEnvelopeForRecipient(receiver.Ed25519.URN(), receiver.X25519PK, "work", "ack-failure")
	if err != nil {
		t.Fatal(err)
	}
	agent := &Agent{Keys: receiver, Session: session.NewManager(nil, receiver)}
	ackFailure := errors.New("ACK response lost")
	persist := func(_ *pb.EncryptedEnvelope, _ []byte) error { return nil }
	if err := agent.processEnvelopes(context.Background(), []*pb.EncryptedEnvelope{env}, persist, func([]string) error { return ackFailure }); !errors.Is(err, ackFailure) {
		t.Fatalf("ACK error lost: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = agent.processEnvelopes(ctx, []*pb.EncryptedEnvelope{env}, func(_ *pb.EncryptedEnvelope, _ []byte) error { t.Fatal("callback after cancellation"); return nil }, func([]string) error { t.Fatal("ACK after cancellation"); return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
}
