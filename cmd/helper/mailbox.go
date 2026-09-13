package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"time"

	pb "github.com/BillShiyaoZhang/agent-comm/proto"
	_ "modernc.org/sqlite"
)

const maxMessageBytes = 256 * 1024

var messageIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)
var recipientURNPattern = regexp.MustCompile(`^urn:[A-Za-z0-9][A-Za-z0-9._:-]*:[A-Za-z0-9]+$`)
var errMessageConflict = errors.New("message_id already belongs to different content")

// MessageFields travel inside the authenticated, encrypted text payload.
type MessageFields struct {
	Text           string `json:"text"`
	ConversationID string `json:"conversation_id,omitempty"`
	InReplyTo      string `json:"in_reply_to,omitempty"`
	TaskID         string `json:"task_id,omitempty"`
	Kind           string `json:"kind,omitempty"`
	Deadline       string `json:"deadline,omitempty"`
	HopLimit       *int   `json:"hop_limit,omitempty"`
}

type StoreRequest struct {
	MessageID    string `json:"message_id,omitempty"`
	RecipientURN string `json:"recipient_urn"`
	MessageFields
}

type InboxMessage struct {
	MessageID string `json:"message_id"`
	SenderURN string `json:"sender_urn"`
	MessageFields
}

type wireMessage struct {
	Version int `json:"agent_comm"`
	MessageFields
}

func (m *MessageFields) validate() error {
	if m.Text == "" || len(m.Text) > maxMessageBytes {
		return errors.New("text must contain 1 to 262144 bytes")
	}
	for _, value := range []string{m.ConversationID, m.InReplyTo, m.TaskID, m.Kind} {
		if len(value) > 256 {
			return errors.New("message metadata exceeds 256 bytes")
		}
	}
	if m.Kind == "" {
		m.Kind = "message"
	}
	if m.HopLimit == nil {
		n := 8
		m.HopLimit = &n
	}
	if *m.HopLimit < 0 || *m.HopLimit > 64 {
		return errors.New("hop_limit must be between 0 and 64")
	}
	if m.Deadline != "" {
		if _, err := time.Parse(time.RFC3339, m.Deadline); err != nil {
			return errors.New("deadline must be RFC3339")
		}
	}
	return nil
}

func (m MessageFields) expired(now time.Time) bool {
	if m.Deadline == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339, m.Deadline)
	return err == nil && !t.After(now)
}

type mailbox struct{ db *sql.DB }

func openMailbox(path string) (*mailbox, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`PRAGMA busy_timeout=5000; PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL;
CREATE TABLE IF NOT EXISTS helper_inbox (
 message_id TEXT PRIMARY KEY, payload BLOB NOT NULL, received_at INTEGER NOT NULL,
 consumed_at INTEGER);
CREATE TABLE IF NOT EXISTS helper_outbox (
 message_id TEXT PRIMARY KEY, request BLOB NOT NULL, envelope BLOB,
 status TEXT NOT NULL DEFAULT 'accepted', attempts INTEGER NOT NULL DEFAULT 0,
 next_attempt INTEGER NOT NULL DEFAULT 0, last_error TEXT NOT NULL DEFAULT '',
 created_at INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS helper_inbox_pending ON helper_inbox(consumed_at,received_at);
CREATE INDEX IF NOT EXISTS helper_outbox_pending ON helper_outbox(status,next_attempt);`)
	if err != nil {
		db.Close()
		return nil, err
	}
	if err := os.Chmod(path, 0600); err != nil {
		db.Close()
		return nil, err
	}
	return &mailbox{db: db}, nil
}

func (m *mailbox) accept(req StoreRequest) (string, error) {
	data, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	_, err = m.db.Exec(`INSERT INTO helper_outbox(message_id,request,created_at) VALUES(?,?,?) ON CONFLICT(message_id) DO NOTHING`, req.MessageID, data, time.Now().UnixMilli())
	if err != nil {
		return "", err
	}
	var existing []byte
	var status string
	if err := m.db.QueryRow(`SELECT request,status FROM helper_outbox WHERE message_id=?`, req.MessageID).Scan(&existing, &status); err != nil {
		return "", err
	}
	if !bytes.Equal(existing, data) {
		return "", errMessageConflict
	}
	return status, nil
}

func (m *mailbox) receive(msg InboxMessage) error {
	if !messageIDPattern.MatchString(msg.MessageID) || msg.SenderURN == "" {
		return errors.New("invalid authenticated message identity")
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	_, err = m.db.Exec(`INSERT INTO helper_inbox(message_id,payload,received_at) VALUES(?,?,?) ON CONFLICT(message_id) DO NOTHING`, msg.MessageID, data, time.Now().UnixMilli())
	if err != nil {
		return err
	}
	var existing []byte
	if err := m.db.QueryRow(`SELECT payload FROM helper_inbox WHERE message_id=?`, msg.MessageID).Scan(&existing); err != nil {
		return err
	}
	if !bytes.Equal(existing, data) {
		return errMessageConflict
	}
	return nil
}

func (m *mailbox) pending() ([]InboxMessage, error) {
	rows, err := m.db.Query(`SELECT payload FROM helper_inbox WHERE consumed_at IS NULL ORDER BY received_at,message_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	messages := []InboxMessage{}
	for rows.Next() {
		var data []byte
		var msg InboxMessage
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(data, &msg); err != nil {
			return nil, err
		}
		messages = append(messages, msg)
	}
	return messages, rows.Err()
}

func (m *mailbox) ack(ids []string) (int64, error) {
	tx, err := m.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var count int64
	for _, id := range ids {
		result, err := tx.Exec(`UPDATE helper_inbox SET consumed_at=? WHERE message_id=? AND consumed_at IS NULL`, time.Now().UnixMilli(), id)
		if err != nil {
			return 0, err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return 0, err
		}
		count += n
	}
	return count, tx.Commit()
}

type outboxEntry struct {
	request  StoreRequest
	envelope []byte
	attempts int
}

func (m *mailbox) nextOutgoing() (*outboxEntry, error) {
	var data []byte
	entry := &outboxEntry{}
	err := m.db.QueryRow(`SELECT request,envelope,attempts FROM helper_outbox WHERE status='accepted' AND next_attempt<=? ORDER BY created_at,message_id LIMIT 1`, time.Now().UnixMilli()).Scan(&data, &entry.envelope, &entry.attempts)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &entry.request); err != nil {
		return nil, err
	}
	return entry, nil
}

func (m *mailbox) saveEnvelope(id string, data []byte) ([]byte, error) {
	// Another helper using this mailbox may have prepared the same ID while
	// discovery was in progress. Every worker must send the committed winner.
	_, err := m.db.Exec(`UPDATE helper_outbox SET envelope=? WHERE message_id=? AND envelope IS NULL`, data, id)
	if err != nil {
		return nil, err
	}
	var committed []byte
	err = m.db.QueryRow(`SELECT envelope FROM helper_outbox WHERE message_id=?`, id).Scan(&committed)
	return committed, err
}

func (m *mailbox) updateOutgoing(id, status, reason string, attempts int) error {
	delay := time.Second << min(attempts, 8)
	// A late failed duplicate attempt must not demote a successful delivery.
	_, err := m.db.Exec(`UPDATE helper_outbox SET status=?,last_error=?,attempts=MAX(attempts,?),next_attempt=? WHERE message_id=? AND status='accepted'`, status, reason, attempts, time.Now().Add(delay).UnixMilli(), id)
	return err
}

func (m *mailbox) outgoingStatus(id string) (map[string]any, error) {
	var status, lastError string
	var attempts int
	err := m.db.QueryRow(`SELECT status,attempts,last_error FROM helper_outbox WHERE message_id=?`, id).Scan(&status, &attempts, &lastError)
	if err != nil {
		return nil, err
	}
	return map[string]any{"message_id": id, "status": status, "attempts": attempts, "last_error": lastError}, nil
}

// durableTransport makes persistence ordering testable without a public network.
type durableTransport interface {
	PrepareMessage(context.Context, string, string, string) (*pb.EncryptedEnvelope, error)
	DeliverEnvelope(context.Context, *pb.EncryptedEnvelope) error
}

func (m *mailbox) String() string { return fmt.Sprintf("helper mailbox %p", m.db) }
