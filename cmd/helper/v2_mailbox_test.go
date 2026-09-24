package main

import (
	"bytes"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/BillShiyaoZhang/agent-comm/v2"
)

func TestV2OutboxEnvelopeAndSequenceAtomic(t *testing.T) {
	m, err := openMailbox(filepath.Join(t.TempDir(), "mailbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer m.db.Close()
	req := StoreRequest{MessageID: "v2-1", RecipientURN: "peer", MessageFields: MessageFields{Text: "hello"}}
	if _, err := m.acceptV2(req); err != nil {
		t.Fatal(err)
	}
	session := &v2.Session{ID: "session", InitiatorURN: "self", ResponderURN: "peer", Mode: v2.ModeCompliance, PolicyHash: "policy", PRK: bytes.Repeat([]byte{1}, 32), TranscriptHash: bytes.Repeat([]byte{2}, 32), OwnFinishedSent: true, PeerFinishedVerified: true}
	if err := m.saveV2Session("peer", session); err != nil {
		t.Fatal(err)
	}
	first, _, err := m.saveV2Envelope("v2-1", "peer", 1, []byte("first"), []byte("cek"), "policy", "session")
	if err != nil || string(first) != "first" {
		t.Fatalf("first envelope: %v", err)
	}
	second, _, err := m.saveV2Envelope("v2-1", "peer", 2, []byte("different"), nil, "policy", "session")
	if err != nil || string(second) != "first" {
		t.Fatalf("retry changed envelope: %q %v", second, err)
	}
	stored, err := m.loadV2Session("peer")
	if err != nil || stored.SendSequence != 1 {
		t.Fatalf("sequence advanced on retry: %+v %v", stored, err)
	}
	if _, _, err := m.saveV2Envelope("missing", "peer", 2, []byte("new"), nil, "policy", "session"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing outbox did not rollback: %v", err)
	}
	stored, _ = m.loadV2Session("peer")
	if stored.SendSequence != 1 {
		t.Fatal("missing outbox consumed sequence")
	}
}

func TestV2ReceiveCommitBeforeAckAndPolicyCutover(t *testing.T) {
	m, err := openMailbox(filepath.Join(t.TempDir(), "mailbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer m.db.Close()
	session := &v2.Session{ID: "s", InitiatorURN: "peer", ResponderURN: "self", Mode: v2.ModePrivate, PolicyHash: "old", PRK: bytes.Repeat([]byte{1}, 32), TranscriptHash: bytes.Repeat([]byte{2}, 32), OwnFinishedSent: true, PeerFinishedVerified: true}
	if err := m.saveV2Session("peer", session); err != nil {
		t.Fatal(err)
	}
	env := &v2.Envelope{Header: v2.Header{MessageID: "in-1", SenderURN: "peer", SessionID: "s", PolicyHash: "old", Mode: v2.ModePrivate, Sequence: 1}}
	msg := InboxMessage{MessageID: "in-1", SenderURN: "peer", MessageFields: MessageFields{Text: "verified"}, Mode: v2.ModePrivate, EnvelopeHash: "hash"}
	wrong := *env
	wrong.Header.Sequence = 2
	if err := m.receiveV2(msg, &wrong); err == nil {
		t.Fatal("out-of-order message accepted")
	}
	var count int
	if err := m.db.QueryRow(`SELECT COUNT(*) FROM helper_inbox`).Scan(&count); err != nil || count != 0 {
		t.Fatal("invalid message persisted")
	}
	if err := m.receiveV2(msg, env); err != nil {
		t.Fatal(err)
	}
	if err := m.receiveV2(msg, env); err != nil {
		t.Fatalf("identical retry rejected: %v", err)
	}
	stored, _ := m.loadV2Session("peer")
	if stored.ReceiveSequence != 1 {
		t.Fatal("inbox commit did not advance exactly one sequence")
	}
	if err := m.db.QueryRow(`SELECT COUNT(*) FROM helper_inbox`).Scan(&count); err != nil || count != 1 {
		t.Fatal("inbox dedup failed")
	}
	p1 := &v2.Policy{Version: 2, PlatformID: "p", Epoch: 1, NotBefore: time.Now().Add(-time.Hour).Unix(), ExpiresAt: time.Now().Add(time.Hour).Unix(), Mode: v2.ModePrivate, Suite: v2.Suite}
	changed, err := m.saveV2Policy(p1)
	if err != nil || !changed {
		t.Fatalf("first policy not saved: %v %v", changed, err)
	}
	stored.PolicyHash = v2.PolicyHash(p1)
	if err := m.saveV2Session("peer", stored); err != nil {
		t.Fatal(err)
	}
	req := StoreRequest{MessageID: "old-out", RecipientURN: "peer", MessageFields: MessageFields{Text: "pending"}}
	if _, err := m.acceptV2(req); err != nil {
		t.Fatal(err)
	}
	unsent := StoreRequest{MessageID: "old-unsent", RecipientURN: "peer", MessageFields: MessageFields{Text: "not encrypted yet"}}
	if _, err := m.acceptV2(unsent); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.saveV2Envelope("old-out", "peer", 1, []byte("immutable"), nil, v2.PolicyHash(p1), "s"); err != nil {
		t.Fatal(err)
	}
	p2 := *p1
	p2.Epoch = 2
	p2.Mode = v2.ModeCompliance
	changed, err = m.saveV2Policy(&p2)
	if err != nil || !changed {
		t.Fatalf("cutover policy not saved: %v %v", changed, err)
	}
	status, err := m.v2OutgoingStatus("old-out")
	if err != nil || status["status"] != "quarantined" {
		t.Fatalf("old envelope not quarantined: %+v %v", status, err)
	}
	unsentStatus, err := m.v2OutgoingStatus("old-unsent")
	if err != nil || unsentStatus["status"] != "quarantined" {
		t.Fatalf("pre-cutover plaintext was silently upgraded: %+v %v", unsentStatus, err)
	}
	if _, err := m.loadV2Session("peer"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("old session survived cutover: %v", err)
	}
}
