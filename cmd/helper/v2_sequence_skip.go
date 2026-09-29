package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/BillShiyaoZhang/agent-comm/v2"
)

const (
	v2SequenceSkipPrefix = "v2skip_"
	v2SequenceSkipKind   = "agent_comm.sequence_skip.v1"
	v2SequenceSkipText   = "sequence canceled after permanent message ID conflict"
	v2ConflictErrorText  = "v2 platform /api/v2/mq/store: HTTP 409: v2 message ID conflict"
)

type v2GapCandidate struct {
	messageID, policyHash, sessionID string
	request, envelope, receipt       []byte
	createdAt                        int64
}

var errNoLaterV2Receipt = errors.New("no later admitted v2 sequence")

func v2SequenceSkipID(originalEnvelope []byte) string {
	hash := sha256.New()
	hash.Write([]byte("agent-comm-v2/sequence-skip-id\x00"))
	hash.Write(originalEnvelope)
	return v2SequenceSkipPrefix + hex.EncodeToString(hash.Sum(nil))
}

func isV2ConflictError(value string) bool {
	return strings.TrimRight(value, "\r\n") == v2ConflictErrorText
}

func (m *mailbox) nextV2GapCandidate(afterCreatedAt int64, afterID string) (*v2GapCandidate, error) {
	var c v2GapCandidate
	err := m.db.QueryRow(`SELECT o.message_id,o.request,o.envelope,o.receipt,o.policy_hash,o.session_id,o.created_at
		FROM helper_v2_outbox o LEFT JOIN helper_v2_sequence_repairs r ON r.original_message_id=o.message_id
		WHERE o.status='conflict' AND o.receipt IS NULL AND o.envelope IS NOT NULL
		AND o.last_error IN (?,?,?) AND r.original_message_id IS NULL
		AND (o.created_at>? OR (o.created_at=? AND o.message_id>?))
		ORDER BY o.created_at,o.message_id LIMIT 1`,
		v2ConflictErrorText, v2ConflictErrorText+"\n", v2ConflictErrorText+"\r\n",
		afterCreatedAt, afterCreatedAt, afterID).Scan(
		&c.messageID, &c.request, &c.envelope, &c.receipt, &c.policyHash, &c.sessionID, &c.createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// A later Platform receipt proves this session already published a higher
// sequence. The original conflict has no receipt and can never fill its gap.
func (e *v2Engine) hasLaterAdmittedV2Sequence(policy *v2.Policy, peerURN, sessionID string, gap uint64) (bool, error) {
	rows, err := e.ds.mailbox.db.Query(`SELECT envelope,cek,receipt FROM helper_v2_outbox
		WHERE status='platform_queued' AND policy_hash=? AND session_id=? AND receipt IS NOT NULL`,
		v2.PolicyHash(policy), sessionID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw, cek, receiptRaw []byte
		if err := rows.Scan(&raw, &cek, &receiptRaw); err != nil {
			return false, err
		}
		parsed, err := v2.ParseEnvelope(raw)
		if err != nil {
			return false, err
		}
		if parsed.Header.RecipientURN != peerURN || parsed.Header.SessionID != sessionID || parsed.Header.Sequence <= gap {
			continue
		}
		_, err = v2.VerifyEnvelope(policy, e.client.IdentityPublic, raw, time.Now())
		if err != nil {
			return false, err
		}
		receipt, err := v2.ParseReceipt(receiptRaw)
		if err != nil {
			return false, err
		}
		if err := v2.VerifyReceipt(policy, receipt, raw, cek, time.Now()); err != nil {
			return false, err
		}
		return true, nil
	}
	return false, rows.Err()
}

// scheduleV2SequenceSkip emits no user content. It requires a durable, exact
// admission conflict and a verified receipt for a later sequence in the same
// session, then saves one immutable control envelope for normal outbox retry.
func (e *v2Engine) scheduleV2SequenceSkip(ctx context.Context, policy *v2.Policy) error {
	var failures []error
	var afterCreatedAt int64
	var afterID string
	for {
		c, err := e.ds.mailbox.nextV2GapCandidate(afterCreatedAt, afterID)
		if err != nil || c == nil {
			return errors.Join(append(failures, err)...)
		}
		afterCreatedAt, afterID = c.createdAt, c.messageID
		err = e.scheduleOneV2SequenceSkip(ctx, policy, c)
		if errors.Is(err, errNoLaterV2Receipt) {
			continue
		}
		if err != nil {
			failures = append(failures, fmt.Errorf("v2 gap %s: %w", c.messageID, err))
			continue
		}
		return errors.Join(failures...)
	}
}

func (e *v2Engine) scheduleOneV2SequenceSkip(ctx context.Context, policy *v2.Policy, c *v2GapCandidate) error {
	var original StoreRequest
	if err := json.Unmarshal(c.request, &original); err != nil {
		return err
	}
	env, err := v2.VerifyEnvelopeSignature(e.client.IdentityPublic, c.envelope)
	if err != nil {
		return err
	}
	h := env.Header
	if original.MessageID != c.messageID || original.RecipientURN != h.RecipientURN ||
		h.MessageID != c.messageID || h.SenderURN != e.client.URN || h.SessionID != c.sessionID ||
		h.PolicyHash != c.policyHash || h.PolicyHash != v2.PolicyHash(policy) ||
		h.PlatformID != policy.PlatformID || h.PolicyEpoch != policy.Epoch || h.Mode != policy.Mode ||
		h.Suite != policy.Suite || h.ContentType != v2.ContentTypeAgentJSON || h.Sequence == 0 {
		return errors.New("conflicted v2 envelope does not match current session and policy")
	}
	session, err := e.ds.mailbox.loadV2Session(h.RecipientURN)
	if err != nil {
		return err
	}
	if !session.Ready() || session.ID != c.sessionID || session.PolicyHash != c.policyHash ||
		session.Mode != policy.Mode || session.PeerKeyID != h.RecipientKeyID || session.SendSequence <= h.Sequence {
		return errors.New("conflicted v2 sequence lacks a ready session with later sends")
	}
	direction := "a_to_b"
	if e.client.URN == session.ResponderURN {
		direction = "b_to_a"
	}
	if h.Direction != direction {
		return errors.New("conflicted v2 envelope direction mismatch")
	}
	later, err := e.hasLaterAdmittedV2Sequence(policy, h.RecipientURN, c.sessionID, h.Sequence)
	if err != nil {
		return err
	}
	if !later {
		return errNoLaterV2Receipt
	}
	recipientX, err := e.resolveRecipient(ctx, h.RecipientURN)
	if err != nil {
		return err
	}
	if v2.KeyID(recipientX) != h.RecipientKeyID {
		return errors.New("recipient key changed before sequence repair")
	}
	skipID := v2SequenceSkipID(c.envelope)
	zero := 0
	req := StoreRequest{MessageID: skipID, RecipientURN: h.RecipientURN, MessageFields: MessageFields{
		Text: v2SequenceSkipText, InReplyTo: c.messageID, TaskID: v2.EnvelopeHash(c.envelope),
		Kind: v2SequenceSkipKind, HopLimit: &zero,
	}}
	deadline := time.Now().Add(7 * 24 * time.Hour).Unix()
	if deadline > policy.ExpiresAt {
		deadline = policy.ExpiresAt
	}
	h.MessageID, h.Expiry = skipID, deadline
	plaintext, err := json.Marshal(wireMessage{Version: 2, MessageFields: req.MessageFields})
	if err != nil {
		return err
	}
	var repair *v2.Envelope
	var cek []byte
	if policy.Mode == v2.ModePrivate {
		key, err := session.PrivateSequenceSkipKey(direction, h.Sequence)
		if err != nil {
			return err
		}
		repair, err = v2.SealPrivate(policy, h, plaintext, key, e.client.IdentityPrivate)
	} else {
		repair, cek, err = v2.SealCompliance(policy, h, plaintext, recipientX, e.client.IdentityPrivate)
	}
	if err != nil {
		return err
	}
	raw, err := v2.Canonical(repair)
	if err != nil {
		return err
	}
	created, err := e.ds.mailbox.saveV2SequenceSkip(*c, req, raw, cek, h.Sequence)
	if err != nil {
		return err
	}
	if created {
		// The original conflict and later business envelope remain byte-for-byte.
		// A later tick will retry this same repair envelope if Platform is offline.
		return nil
	}
	return nil
}

func (m *mailbox) saveV2SequenceSkip(c v2GapCandidate, req StoreRequest, raw, cek []byte, sequence uint64) (bool, error) {
	if sequence == 0 || sequence > math.MaxInt64 || len(raw) == 0 || !strings.HasPrefix(req.MessageID, v2SequenceSkipPrefix) {
		return false, errors.New("invalid v2 sequence repair")
	}
	request, err := json.Marshal(req)
	if err != nil {
		return false, err
	}
	tx, err := m.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var mappedID, mappedHash, mappedSession string
	var mappedSequence int64
	err = tx.QueryRow(`SELECT repair_message_id,original_envelope_hash,session_id,sequence
		FROM helper_v2_sequence_repairs WHERE original_message_id=?`, c.messageID).Scan(
		&mappedID, &mappedHash, &mappedSession, &mappedSequence)
	if err == nil {
		if mappedID != req.MessageID || mappedHash != v2.EnvelopeHash(c.envelope) ||
			mappedSession != c.sessionID || mappedSequence != int64(sequence) {
			return false, errors.New("existing v2 sequence repair differs")
		}
		var persisted []byte
		if err := tx.QueryRow(`SELECT envelope FROM helper_v2_outbox WHERE message_id=?`, mappedID).Scan(&persisted); err != nil || len(persisted) == 0 {
			return false, errors.New("existing v2 sequence repair has no durable envelope")
		}
		return false, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	var status, lastError, policyHash, sessionID string
	var originalRaw, receipt []byte
	if err := tx.QueryRow(`SELECT status,last_error,envelope,receipt,policy_hash,session_id
		FROM helper_v2_outbox WHERE message_id=?`, c.messageID).Scan(
		&status, &lastError, &originalRaw, &receipt, &policyHash, &sessionID); err != nil {
		return false, err
	}
	if status != "conflict" || !isV2ConflictError(lastError) || len(receipt) != 0 ||
		!bytes.Equal(originalRaw, c.envelope) || policyHash != c.policyHash || sessionID != c.sessionID {
		return false, errors.New("v2 conflict changed before sequence repair commit")
	}
	var sessionRaw []byte
	if err := tx.QueryRow(`SELECT data FROM helper_v2_sessions WHERE peer_urn=?`, req.RecipientURN).Scan(&sessionRaw); err != nil {
		return false, err
	}
	var session v2.Session
	if err := json.Unmarshal(sessionRaw, &session); err != nil {
		return false, err
	}
	if !session.Ready() || session.ID != c.sessionID || session.PolicyHash != c.policyHash ||
		session.SendSequence <= sequence {
		return false, errors.New("v2 session changed before sequence repair commit")
	}
	if _, err := tx.Exec(`INSERT INTO helper_v2_outbox
		(message_id,request,envelope,cek,policy_hash,session_id,status,created_at)
		VALUES(?,?,?,?,?,?,'accepted',?)`, req.MessageID, request, raw, cek, c.policyHash, c.sessionID, c.createdAt); err != nil {
		return false, err
	}
	if _, err := tx.Exec(`INSERT INTO helper_v2_sequence_repairs
		(original_message_id,repair_message_id,original_envelope_hash,session_id,sequence,created_at)
		VALUES(?,?,?,?,?,?)`, c.messageID, req.MessageID, v2.EnvelopeHash(c.envelope), c.sessionID,
		int64(sequence), time.Now().UnixMilli()); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

func validV2SequenceSkip(fields MessageFields) bool {
	return fields.Kind == v2SequenceSkipKind && fields.Text == v2SequenceSkipText &&
		messageIDPattern.MatchString(fields.InReplyTo) && len(fields.TaskID) == 64 &&
		fields.ConversationID == "" && fields.Deadline == "" &&
		fields.HopLimit != nil && *fields.HopLimit == 0 &&
		func() bool {
			decoded, err := hex.DecodeString(fields.TaskID)
			return err == nil && len(decoded) == 32 && fields.TaskID == hex.EncodeToString(decoded)
		}()
}

// A verified sequence skip is hidden from the user inbox. Its tombstone and
// receive counter commit together, so loss of the MQ ACK is safely retryable.
func (m *mailbox) receiveV2SequenceSkip(env *v2.Envelope, originalID, originalHash, envelopeHash string) error {
	if env == nil || !strings.HasPrefix(env.Header.MessageID, v2SequenceSkipPrefix) ||
		originalID == "" || originalHash == "" || envelopeHash == "" {
		return errors.New("invalid v2 sequence skip")
	}
	tx, err := m.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var storedHash, storedOriginalID, storedOriginalHash string
	err = tx.QueryRow(`SELECT envelope_hash,original_message_id,original_envelope_hash
		FROM helper_v2_skipped_inbox WHERE message_id=?`, env.Header.MessageID).Scan(
		&storedHash, &storedOriginalID, &storedOriginalHash)
	if err == nil {
		if storedHash != envelopeHash || storedOriginalID != originalID || storedOriginalHash != originalHash {
			return errMessageConflict
		}
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var existingID string
	err = tx.QueryRow(`SELECT message_id FROM helper_inbox WHERE message_id=?`, env.Header.MessageID).Scan(&existingID)
	if err == nil {
		return errMessageConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var sessionRaw []byte
	if err := tx.QueryRow(`SELECT data FROM helper_v2_sessions WHERE peer_urn=?`, env.Header.SenderURN).Scan(&sessionRaw); err != nil {
		return err
	}
	var session v2.Session
	if err := json.Unmarshal(sessionRaw, &session); err != nil {
		return err
	}
	if !session.Ready() || session.ID != env.Header.SessionID || session.PolicyHash != env.Header.PolicyHash ||
		session.Mode != env.Header.Mode || env.Header.Sequence != session.ReceiveSequence+1 ||
		env.Header.Sequence > math.MaxInt64 {
		return fmt.Errorf("v2 receive sequence or session mismatch: got %d after %d", env.Header.Sequence, session.ReceiveSequence)
	}
	session.ReceiveSequence = env.Header.Sequence
	updated, err := json.Marshal(session)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE helper_v2_sessions SET data=? WHERE peer_urn=?`, updated, env.Header.SenderURN); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO helper_v2_skipped_inbox
		(message_id,sender_urn,session_id,sequence,original_message_id,original_envelope_hash,envelope_hash,received_at)
		VALUES(?,?,?,?,?,?,?,?)`, env.Header.MessageID, env.Header.SenderURN, env.Header.SessionID,
		int64(env.Header.Sequence), originalID, originalHash, envelopeHash, time.Now().UnixMilli()); err != nil {
		return err
	}
	return tx.Commit()
}
