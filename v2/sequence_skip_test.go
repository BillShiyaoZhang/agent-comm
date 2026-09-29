package v2

import (
	"bytes"
	"testing"
)

func TestPrivateSequenceSkipKeyIsDomainSeparated(t *testing.T) {
	session := &Session{ID: "session", Mode: ModePrivate, PRK: bytes.Repeat([]byte{7}, 32),
		OwnFinishedSent: true, PeerFinishedVerified: true}
	messageKey, err := session.PrivateMessageKey("a_to_b", 2)
	if err != nil {
		t.Fatal(err)
	}
	skipKey, err := session.PrivateSequenceSkipKey("a_to_b", 2)
	if err != nil {
		t.Fatal(err)
	}
	otherSequence, err := session.PrivateSequenceSkipKey("a_to_b", 3)
	if err != nil {
		t.Fatal(err)
	}
	otherDirection, err := session.PrivateSequenceSkipKey("b_to_a", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(skipKey) != 32 || bytes.Equal(skipKey, messageKey) ||
		bytes.Equal(skipKey, otherSequence) || bytes.Equal(skipKey, otherDirection) {
		t.Fatal("sequence repair key did not separate purpose, sequence, and direction")
	}
	session.PeerFinishedVerified = false
	if _, err := session.PrivateSequenceSkipKey("a_to_b", 2); err == nil {
		t.Fatal("unverified session derived repair key")
	}
}
