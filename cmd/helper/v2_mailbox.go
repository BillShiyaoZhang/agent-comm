package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/BillShiyaoZhang/agent-comm/v2"
)

type v2OutboxEntry struct {
	request    StoreRequest
	envelope   []byte
	cek        []byte
	policyHash string
	sessionID  string
	attempts   int
}

type v2HandshakeRecord struct {
	sessionID        string
	peerURN          string
	role             string
	initFrame        []byte
	acceptFrame      []byte
	ephemeralPrivate []byte
	status           string
}

func (m *mailbox) acceptV2(req StoreRequest) (string, error) {
	data, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	_, err = m.db.Exec(`INSERT INTO helper_v2_outbox(message_id,request,created_at) VALUES(?,?,?) ON CONFLICT(message_id) DO NOTHING`, req.MessageID, data, time.Now().UnixMilli())
	if err != nil {
		return "", err
	}
	var existing []byte
	var status string
	if err := m.db.QueryRow(`SELECT request,status FROM helper_v2_outbox WHERE message_id=?`, req.MessageID).Scan(&existing, &status); err != nil {
		return "", err
	}
	if !bytes.Equal(existing, data) {
		return "", errMessageConflict
	}
	return status, nil
}

func (m *mailbox) nextV2Outgoing() (*v2OutboxEntry, error) {
	entry := &v2OutboxEntry{}
	var request []byte
	err := m.db.QueryRow(`SELECT request,envelope,cek,policy_hash,session_id,attempts FROM helper_v2_outbox WHERE status='accepted' AND next_attempt<=? ORDER BY created_at,message_id LIMIT 1`, time.Now().UnixMilli()).Scan(&request, &entry.envelope, &entry.cek, &entry.policyHash, &entry.sessionID, &entry.attempts)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(request, &entry.request); err != nil {
		return nil, err
	}
	return entry, nil
}

func (m *mailbox) saveV2Envelope(messageID, peerURN string, expectedSequence uint64, rawEnvelope, cek []byte, policyHash, sessionID string) ([]byte, []byte, error) {
	tx, err := m.db.Begin()
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	var existing, existingCEK []byte
	if err := tx.QueryRow(`SELECT envelope,cek FROM helper_v2_outbox WHERE message_id=?`, messageID).Scan(&existing, &existingCEK); err != nil {
		return nil, nil, err
	}
	if len(existing) > 0 {
		return existing, existingCEK, tx.Commit()
	}
	var sessionRaw []byte
	if err := tx.QueryRow(`SELECT data FROM helper_v2_sessions WHERE peer_urn=?`, peerURN).Scan(&sessionRaw); err != nil {
		return nil, nil, err
	}
	var session v2.Session
	if err := json.Unmarshal(sessionRaw, &session); err != nil {
		return nil, nil, err
	}
	if !session.Ready() || session.ID != sessionID || session.PolicyHash != policyHash || session.SendSequence+1 != expectedSequence {
		return nil, nil, errors.New("v2 session sequence changed before envelope commit")
	}
	session.SendSequence = expectedSequence
	updated, err := json.Marshal(session)
	if err != nil {
		return nil, nil, err
	}
	if _, err := tx.Exec(`UPDATE helper_v2_sessions SET data=? WHERE peer_urn=?`, updated, peerURN); err != nil {
		return nil, nil, err
	}
	if _, err := tx.Exec(`UPDATE helper_v2_outbox SET envelope=?,cek=?,policy_hash=?,session_id=? WHERE message_id=? AND envelope IS NULL`, rawEnvelope, cek, policyHash, sessionID, messageID); err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	return rawEnvelope, cek, nil
}

func (m *mailbox) updateV2Outgoing(id, status, reason string, attempts int, receipt []byte) error {
	delay := time.Second << min(attempts, 8)
	_, err := m.db.Exec(`UPDATE helper_v2_outbox SET status=?,last_error=?,attempts=MAX(attempts,?),next_attempt=?,receipt=COALESCE(?,receipt) WHERE message_id=? AND status='accepted'`, status, reason, attempts, time.Now().Add(delay).UnixMilli(), receipt, id)
	return err
}

func (m *mailbox) v2OutgoingStatus(id string) (map[string]any, error) {
	var status, lastError, policyHash string
	var attempts int
	var receipt []byte
	err := m.db.QueryRow(`SELECT status,attempts,last_error,policy_hash,receipt FROM helper_v2_outbox WHERE message_id=?`, id).Scan(&status, &attempts, &lastError, &policyHash, &receipt)
	if err != nil {
		return nil, err
	}
	return map[string]any{"message_id": id, "status": status, "attempts": attempts, "last_error": lastError, "policy_hash": policyHash, "receipt_verified": len(receipt) > 0}, nil
}

func (m *mailbox) highestV2Epoch() (uint64, error) {
	var value sql.NullInt64
	if err := m.db.QueryRow(`SELECT MAX(epoch) FROM helper_v2_policy_state`).Scan(&value); err != nil {
		return 0, err
	}
	if !value.Valid {
		return 0, nil
	}
	return uint64(value.Int64), nil
}

func (m *mailbox) policyForAuthorization(expectedHash string) (*v2.Policy, error) {
	if len(expectedHash) != 64 {
		return nil, errors.New("full current policy hash required")
	}
	var raw []byte
	if err := m.db.QueryRow(`SELECT policy FROM helper_v2_policy_state WHERE policy_hash=?`, expectedHash).Scan(&raw); err != nil {
		return nil, err
	}
	policy, err := v2.ParsePolicy(raw)
	if err != nil {
		return nil, err
	}
	highest, err := m.highestV2Epoch()
	if err != nil {
		return nil, err
	}
	if policy.Epoch != highest || v2.PolicyHash(policy) != expectedHash {
		return nil, errors.New("policy hash is not the current verified epoch")
	}
	return policy, nil
}

func (m *mailbox) saveV2Policy(policy *v2.Policy) (bool, error) {
	if policy == nil {
		return false, errors.New("missing v2 policy")
	}
	raw, err := v2.Canonical(policy)
	if err != nil {
		return false, err
	}
	tx, err := m.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var oldEpoch int64
	var oldHash string
	err = tx.QueryRow(`SELECT epoch,policy_hash FROM helper_v2_policy_state WHERE platform_id=?`, policy.PlatformID).Scan(&oldEpoch, &oldHash)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if err == nil && uint64(oldEpoch) > policy.Epoch {
		return false, errors.New("v2 policy epoch rollback")
	}
	newHash := v2.PolicyHash(policy)
	if err == nil && uint64(oldEpoch) == policy.Epoch {
		if oldHash != newHash {
			return false, errors.New("v2 policy equivocation at same epoch")
		}
		return false, nil
	}
	if _, err := tx.Exec(`INSERT INTO helper_v2_policy_state(platform_id,epoch,policy_hash,policy) VALUES(?,?,?,?) ON CONFLICT(platform_id) DO UPDATE SET epoch=excluded.epoch,policy_hash=excluded.policy_hash,policy=excluded.policy`, policy.PlatformID, policy.Epoch, newHash, raw); err != nil {
		return false, err
	}
	if policy.Mode == v2.ModeCompliance {
		if _, err := tx.Exec(`UPDATE helper_outbox SET status='quarantined',last_error='v1 message existed before compliance policy; original ciphertext retained' WHERE status='accepted' AND created_at<=?`, time.Now().UnixMilli()); err != nil {
			return false, err
		}
	}
	// All locally accepted work predating a new policy requires a fresh owner
	// decision, including plaintext that had not yet been encrypted. Existing
	// signed ciphertext remains immutable and available for audit.
	if err == nil {
		if _, err := tx.Exec(`UPDATE helper_v2_outbox SET status='quarantined',last_error='policy changed; pending message requires a new send decision' WHERE status='accepted'`); err != nil {
			return false, err
		}
		if _, err := tx.Exec(`DELETE FROM helper_v2_sessions`); err != nil {
			return false, err
		}
		if _, err := tx.Exec(`DELETE FROM helper_v2_handshakes`); err != nil {
			return false, err
		}
	}
	return true, tx.Commit()
}

func (m *mailbox) saveV2Session(peerURN string, session *v2.Session) error {
	if session == nil {
		return errors.New("nil session")
	}
	raw, err := json.Marshal(session)
	if err != nil {
		return err
	}
	_, err = m.db.Exec(`INSERT INTO helper_v2_sessions(peer_urn,data) VALUES(?,?) ON CONFLICT(peer_urn) DO UPDATE SET data=excluded.data`, peerURN, raw)
	return err
}

func (m *mailbox) loadV2Session(peerURN string) (*v2.Session, error) {
	var raw []byte
	if err := m.db.QueryRow(`SELECT data FROM helper_v2_sessions WHERE peer_urn=?`, peerURN).Scan(&raw); err != nil {
		return nil, err
	}
	var s v2.Session
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func (m *mailbox) deleteV2Session(peerURN string) error {
	_, err := m.db.Exec(`DELETE FROM helper_v2_sessions WHERE peer_urn=?`, peerURN)
	return err
}

func (m *mailbox) saveV2Handshake(record v2HandshakeRecord) error {
	_, err := m.db.Exec(`INSERT INTO helper_v2_handshakes(session_id,peer_urn,role,init_frame,accept_frame,ephemeral_private,status,updated_at) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(session_id) DO UPDATE SET accept_frame=excluded.accept_frame,status=excluded.status,updated_at=excluded.updated_at`, record.sessionID, record.peerURN, record.role, record.initFrame, record.acceptFrame, record.ephemeralPrivate, record.status, time.Now().Unix())
	return err
}

func (m *mailbox) loadV2HandshakeByPeer(peerURN string) (*v2HandshakeRecord, error) {
	return m.scanV2Handshake(m.db.QueryRow(`SELECT session_id,peer_urn,role,init_frame,accept_frame,ephemeral_private,status FROM helper_v2_handshakes WHERE peer_urn=? ORDER BY updated_at DESC LIMIT 1`, peerURN))
}

func (m *mailbox) loadV2HandshakeByID(sessionID string) (*v2HandshakeRecord, error) {
	return m.scanV2Handshake(m.db.QueryRow(`SELECT session_id,peer_urn,role,init_frame,accept_frame,ephemeral_private,status FROM helper_v2_handshakes WHERE session_id=?`, sessionID))
}

func (m *mailbox) scanV2Handshake(row *sql.Row) (*v2HandshakeRecord, error) {
	r := &v2HandshakeRecord{}
	if err := row.Scan(&r.sessionID, &r.peerURN, &r.role, &r.initFrame, &r.acceptFrame, &r.ephemeralPrivate, &r.status); err != nil {
		return nil, err
	}
	return r, nil
}

func (m *mailbox) deleteV2Handshake(sessionID string) error {
	_, err := m.db.Exec(`DELETE FROM helper_v2_handshakes WHERE session_id=?`, sessionID)
	return err
}

func (m *mailbox) seenV2Frame(frameID string) (bool, error) {
	var value string
	err := m.db.QueryRow(`SELECT frame_id FROM helper_v2_seen_frames WHERE frame_id=?`, frameID).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (m *mailbox) markV2FrameSeen(frameID string) error {
	_, err := m.db.Exec(`INSERT INTO helper_v2_seen_frames(frame_id,seen_at) VALUES(?,?) ON CONFLICT(frame_id) DO NOTHING`, frameID, time.Now().Unix())
	return err
}

// receiveV2 atomically persists the verified inbox item and consumed sequence.
// Duplicate retrieval of the exact committed message is safe to ACK again.
func (m *mailbox) receiveV2(msg InboxMessage, env *v2.Envelope) error {
	if env == nil || msg.MessageID != env.Header.MessageID || msg.SenderURN != env.Header.SenderURN {
		return errors.New("v2 inbox identity mismatch")
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	tx, err := m.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var existing []byte
	err = tx.QueryRow(`SELECT payload FROM helper_inbox WHERE message_id=?`, msg.MessageID).Scan(&existing)
	if err == nil {
		if !bytes.Equal(existing, data) {
			return errMessageConflict
		}
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var sessionRaw []byte
	if err := tx.QueryRow(`SELECT data FROM helper_v2_sessions WHERE peer_urn=?`, msg.SenderURN).Scan(&sessionRaw); err != nil {
		return err
	}
	var s v2.Session
	if err := json.Unmarshal(sessionRaw, &s); err != nil {
		return err
	}
	if !s.Ready() || s.ID != env.Header.SessionID || s.PolicyHash != env.Header.PolicyHash || s.Mode != env.Header.Mode || env.Header.Sequence != s.ReceiveSequence+1 {
		return fmt.Errorf("v2 receive sequence or session mismatch: got %d after %d", env.Header.Sequence, s.ReceiveSequence)
	}
	s.ReceiveSequence = env.Header.Sequence
	updated, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE helper_v2_sessions SET data=? WHERE peer_urn=?`, updated, msg.SenderURN); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO helper_inbox(message_id,payload,received_at) VALUES(?,?,?)`, msg.MessageID, data, time.Now().UnixMilli()); err != nil {
		return err
	}
	return tx.Commit()
}
