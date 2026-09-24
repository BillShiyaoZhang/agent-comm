package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/BillShiyaoZhang/agent-comm/v2"
	"github.com/google/uuid"
)

type v2Engine struct {
	ds          *DaemonServer
	client      *v2.HTTPClient
	root        ed25519.PublicKey
	keysDir     string
	mu          sync.RWMutex
	policy      *v2.Policy
	lastRefresh time.Time
}

func (e *v2Engine) currentPolicy() *v2.Policy {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.policy == nil || time.Now().Unix() < e.policy.NotBefore || time.Now().Unix() >= e.policy.ExpiresAt {
		return nil
	}
	return e.policy
}

func (e *v2Engine) refreshPolicy(ctx context.Context) error {
	highest, err := e.ds.mailbox.highestV2Epoch()
	if err != nil {
		return err
	}
	policy, err := e.client.FetchPolicy(ctx, e.root, highest)
	if err != nil {
		return err
	}
	if _, err := e.ds.mailbox.saveV2Policy(policy); err != nil {
		return err
	}
	e.mu.Lock()
	e.policy = policy
	e.lastRefresh = time.Now()
	e.mu.Unlock()
	return nil
}

func (e *v2Engine) run(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		tickCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		if err := e.tick(tickCtx); err != nil && ctx.Err() == nil {
			log.Printf("V2 worker: %v", err)
		}
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (e *v2Engine) tick(ctx context.Context) error {
	e.mu.RLock()
	needRefresh := e.policy == nil || time.Since(e.lastRefresh) > 10*time.Second
	e.mu.RUnlock()
	if needRefresh {
		if err := e.refreshPolicy(ctx); err != nil && e.currentPolicy() == nil {
			return err
		}
	}
	policy := e.currentPolicy()
	if policy == nil {
		return errors.New("no valid signed v2 policy")
	}
	if policy.Mode == v2.ModeCompliance && !v2.ComplianceAllowed(e.keysDir, policy) {
		return errors.New("compliance disclosure is not locally authorized")
	}
	var failures []error
	if err := e.processFrames(ctx, policy); err != nil {
		failures = append(failures, err)
	}
	if err := e.resendPending(ctx, policy); err != nil {
		failures = append(failures, err)
	}
	if err := e.processMessages(ctx, policy); err != nil {
		failures = append(failures, err)
	}
	if err := e.deliverNext(ctx, policy); err != nil {
		failures = append(failures, err)
	}
	return errors.Join(failures...)
}

func (ds *DaemonServer) handleV2Store(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	disclosure, err := ds.disclosureStatus()
	if err != nil {
		writePolicyError(w, http.StatusServiceUnavailable, "policy_unavailable", disclosure)
		return
	}
	if !disclosure.V2SendReady {
		code, httpStatus := "upgrade_required", http.StatusConflict
		if disclosure.State == "legacy_unconfigured" {
			code, httpStatus = "policy_root_required", http.StatusPreconditionRequired
		} else if disclosure.State == "consent_required" {
			code, httpStatus = "consent_required", http.StatusForbidden
		} else if disclosure.State == "policy_unavailable" {
			code, httpStatus = "policy_unavailable", http.StatusServiceUnavailable
		}
		writePolicyError(w, httpStatus, code, disclosure)
		return
	}
	var req StoreRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	if !recipientURNPattern.MatchString(req.RecipientURN) || len(req.RecipientURN) > 256 {
		http.Error(w, "Invalid recipient_urn", http.StatusBadRequest)
		return
	}
	if req.MessageID == "" {
		req.MessageID = "msg_" + uuid.NewString()
	}
	if !messageIDPattern.MatchString(req.MessageID) {
		http.Error(w, "Invalid message_id", http.StatusBadRequest)
		return
	}
	if err := req.MessageFields.validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if _, err := v2.LoadPeerPin(ds.agent.Keys.KeysDir, req.RecipientURN); err != nil {
		http.Error(w, "Recipient full identity key must be independently pinned: "+err.Error(), http.StatusForbidden)
		return
	}
	status, err := ds.mailbox.acceptV2(req)
	if err != nil {
		code := http.StatusInternalServerError
		if errors.Is(err, errMessageConflict) {
			code = http.StatusConflict
		}
		http.Error(w, err.Error(), code)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]any{"success": true, "message_id": req.MessageID, "status": status, "protocol": "v2", "mode": disclosure.Mode, "policy_epoch": disclosure.PolicyEpoch, "platform_can_decrypt": disclosure.PlatformCanDecrypt})
}

func (ds *DaemonServer) handleV2Status(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	status, err := ds.mailbox.v2OutgoingStatus(r.URL.Query().Get("message_id"))
	if err != nil {
		code := http.StatusInternalServerError
		if errors.Is(err, sql.ErrNoRows) {
			code = http.StatusNotFound
		}
		http.Error(w, err.Error(), code)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

func (e *v2Engine) pinnedPeer(urn string) (ed25519.PublicKey, error) {
	pin, err := v2.LoadPeerPin(e.keysDir, urn)
	if err != nil {
		return nil, err
	}
	return ed25519.PublicKey(pin.IdentityPublicKey), nil
}

func (e *v2Engine) resolveRecipient(ctx context.Context, urn string) ([]byte, error) {
	pinned, err := e.pinnedPeer(urn)
	if err != nil {
		return nil, err
	}
	resolved, err := e.ds.agent.ResolveVerifiedRecipient(ctx, urn)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(resolved.Ed25519PubKey, pinned) || len(resolved.X25519PubKey) != 32 {
		return nil, errors.New("registry identity differs from independently pinned peer")
	}
	return resolved.X25519PubKey, nil
}

func (e *v2Engine) startHandshake(ctx context.Context, policy *v2.Policy, peerURN string) error {
	if _, err := e.ds.mailbox.loadV2HandshakeByPeer(peerURN); err == nil {
		return nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	peerX, err := e.resolveRecipient(ctx, peerURN)
	if err != nil {
		return err
	}
	expiry := time.Now().Add(time.Hour).Unix()
	if expiry > policy.ExpiresAt {
		expiry = policy.ExpiresAt
	}
	init, private, err := v2.NewInit(policy, e.client.URN, peerURN, v2.KeyID(peerX), uuid.NewString(), expiry, e.client.IdentityPrivate)
	if err != nil {
		return err
	}
	raw, err := v2.Canonical(init)
	if err != nil {
		return err
	}
	record := v2HandshakeRecord{sessionID: init.SessionID, peerURN: peerURN, role: "initiator", initFrame: raw, ephemeralPrivate: private, status: "init-sent"}
	if err := e.ds.mailbox.saveV2Handshake(record); err != nil {
		return err
	}
	_, err = e.client.StoreFrame(ctx, init)
	return err
}

func (e *v2Engine) processFrames(ctx context.Context, policy *v2.Policy) error {
	items, err := e.client.RetrieveFrames(ctx, 100)
	if err != nil {
		return err
	}
	var failures []error
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return err
		}
		frame, err := v2.ParseFrame(item.Frame)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		frameID := v2.FrameHash(frame)
		if item.FrameID != "" && item.FrameID != frameID {
			failures = append(failures, errors.New("frame ID mismatch"))
			continue
		}
		seen, err := e.ds.mailbox.seenV2Frame(frameID)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if !seen {
			if frame.RecipientURN != e.client.URN {
				failures = append(failures, errors.New("handshake recipient mismatch"))
				continue
			}
			peer, err := e.pinnedPeer(frame.SenderURN)
			if err != nil {
				failures = append(failures, err)
				continue
			}
			if err := v2.VerifyFrame(frame, peer); err != nil {
				failures = append(failures, err)
				continue
			}
			if err := v2.ValidateFrameForRelay(policy, frame, time.Now()); err != nil {
				failures = append(failures, err)
				continue
			}
			switch frame.Type {
			case v2.FrameInit:
				err = e.handleInit(ctx, policy, frame, peer)
			case v2.FrameAccept:
				err = e.handleAccept(ctx, policy, frame, peer)
			case v2.FrameFinished:
				err = e.handleFinished(frame, peer)
			}
			if err != nil {
				failures = append(failures, err)
				continue
			}
			if err := e.ds.mailbox.markV2FrameSeen(frameID); err != nil {
				failures = append(failures, err)
				continue
			}
		}
		if err := e.client.AckFrames(ctx, []string{frameID}); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (e *v2Engine) handleInit(ctx context.Context, policy *v2.Policy, init *v2.HandshakeFrame, peer ed25519.PublicKey) error {
	peerURN := init.SenderURN
	if existing, err := e.ds.mailbox.loadV2HandshakeByPeer(peerURN); err == nil && existing.sessionID != init.SessionID {
		// Resolve simultaneous initiation deterministically.
		if e.client.URN < peerURN {
			return nil
		}
		if err := e.ds.mailbox.deleteV2Handshake(existing.sessionID); err != nil {
			return err
		}
	} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var accept *v2.HandshakeFrame
	var private []byte
	record, err := e.ds.mailbox.loadV2HandshakeByID(init.SessionID)
	if err == nil {
		if record.role != "responder" || !bytes.Equal(record.initFrame, mustCanonicalFrame(init)) {
			return errors.New("handshake ID reused with different init")
		}
		accept, err = v2.ParseFrame(record.acceptFrame)
		if err != nil {
			return err
		}
		private = record.ephemeralPrivate
	} else if errors.Is(err, sql.ErrNoRows) {
		accept, private, err = v2.NewAccept(policy, init, peer, e.client.IdentityPrivate, v2.KeyID(e.ds.agent.Keys.X25519PK), time.Now())
		if err != nil {
			return err
		}
		initRaw, _ := v2.Canonical(init)
		acceptRaw, _ := v2.Canonical(accept)
		record = &v2HandshakeRecord{sessionID: init.SessionID, peerURN: peerURN, role: "responder", initFrame: initRaw, acceptFrame: acceptRaw, ephemeralPrivate: private, status: "accept-sent"}
		if err := e.ds.mailbox.saveV2Handshake(*record); err != nil {
			return err
		}
	} else {
		return err
	}
	if _, err := e.client.StoreFrame(ctx, accept); err != nil {
		return err
	}
	session, err := v2.CompleteResponder(policy, init, accept, private, peer, e.client.IdentityPublic, time.Now())
	if err != nil {
		return err
	}
	peerX, err := e.resolveRecipient(ctx, peerURN)
	if err != nil {
		return err
	}
	session.PeerKeyID = v2.KeyID(peerX)
	finished, err := v2.MakeFinished(session, e.client.URN, e.client.IdentityPrivate)
	if err != nil {
		return err
	}
	if err := e.ds.mailbox.saveV2Session(peerURN, session); err != nil {
		return err
	}
	_, err = e.client.StoreFrame(ctx, finished)
	return err
}

func mustCanonicalFrame(frame *v2.HandshakeFrame) []byte { raw, _ := v2.Canonical(frame); return raw }

func (e *v2Engine) handleAccept(ctx context.Context, policy *v2.Policy, accept *v2.HandshakeFrame, peer ed25519.PublicKey) error {
	record, err := e.ds.mailbox.loadV2HandshakeByID(accept.SessionID)
	if err != nil {
		return err
	}
	if record.role != "initiator" || record.peerURN != accept.SenderURN {
		return errors.New("unexpected accept frame")
	}
	init, err := v2.ParseFrame(record.initFrame)
	if err != nil {
		return err
	}
	session, err := v2.CompleteInitiator(policy, init, accept, record.ephemeralPrivate, e.client.IdentityPublic, peer, time.Now())
	if err != nil {
		return err
	}
	peerX, err := e.resolveRecipient(ctx, accept.SenderURN)
	if err != nil {
		return err
	}
	var initPayload v2.InitPayload
	if err := json.Unmarshal(init.Payload, &initPayload); err != nil || initPayload.RecipientKeyID != v2.KeyID(peerX) {
		return errors.New("recipient key changed during handshake")
	}
	session.PeerKeyID = v2.KeyID(peerX)
	finished, err := v2.MakeFinished(session, e.client.URN, e.client.IdentityPrivate)
	if err != nil {
		return err
	}
	if err := e.ds.mailbox.saveV2Session(accept.SenderURN, session); err != nil {
		return err
	}
	_, err = e.client.StoreFrame(ctx, finished)
	return err
}

func (e *v2Engine) handleFinished(frame *v2.HandshakeFrame, peer ed25519.PublicKey) error {
	session, err := e.ds.mailbox.loadV2Session(frame.SenderURN)
	if err != nil {
		return err
	}
	if err := v2.VerifyFinished(session, frame, peer, e.client.URN); err != nil {
		return err
	}
	if err := e.ds.mailbox.saveV2Session(frame.SenderURN, session); err != nil {
		return err
	}
	if session.Ready() {
		_ = e.ds.mailbox.deleteV2Handshake(session.ID)
	}
	return nil
}

func (e *v2Engine) resendPending(ctx context.Context, policy *v2.Policy) error {
	rows, err := e.ds.mailbox.db.Query(`SELECT session_id,peer_urn,role,init_frame,accept_frame,ephemeral_private,status FROM helper_v2_handshakes`)
	if err != nil {
		return err
	}
	var records []v2HandshakeRecord
	for rows.Next() {
		var r v2HandshakeRecord
		if err := rows.Scan(&r.sessionID, &r.peerURN, &r.role, &r.initFrame, &r.acceptFrame, &r.ephemeralPrivate, &r.status); err != nil {
			rows.Close()
			return err
		}
		records = append(records, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	var failures []error
	for _, record := range records {
		session, sessionErr := e.ds.mailbox.loadV2Session(record.peerURN)
		if sessionErr == nil && session.Ready() {
			_ = e.ds.mailbox.deleteV2Handshake(record.sessionID)
			continue
		}
		if sessionErr == nil && session.ID == record.sessionID && session.OwnFinishedSent {
			finished, err := v2.MakeFinished(session, e.client.URN, e.client.IdentityPrivate)
			if err == nil {
				_, err = e.client.StoreFrame(ctx, finished)
			}
			if err != nil {
				failures = append(failures, err)
			}
			continue
		}
		var frame *v2.HandshakeFrame
		if record.role == "initiator" {
			frame, err = v2.ParseFrame(record.initFrame)
		} else {
			frame, err = v2.ParseFrame(record.acceptFrame)
		}
		if err == nil {
			_, err = e.client.StoreFrame(ctx, frame)
		}
		if err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (e *v2Engine) deliverNext(ctx context.Context, policy *v2.Policy) error {
	if policy.Mode == v2.ModeCompliance && !v2.ComplianceAllowed(e.keysDir, policy) {
		return errors.New("compliance disclosure is not locally authorized")
	}
	entry, err := e.ds.mailbox.nextV2Outgoing()
	if err != nil || entry == nil {
		return err
	}
	req := entry.request
	if req.MessageFields.expired(time.Now()) {
		return e.ds.mailbox.updateV2Outgoing(req.MessageID, "expired", "deadline elapsed", entry.attempts, nil)
	}
	if len(entry.envelope) > 0 && entry.policyHash != v2.PolicyHash(policy) {
		return e.ds.mailbox.updateV2Outgoing(req.MessageID, "quarantined", "policy changed; original ciphertext retained", entry.attempts, nil)
	}
	if len(entry.envelope) == 0 {
		session, err := e.ds.mailbox.loadV2Session(req.RecipientURN)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && (!session.Ready() || session.PolicyHash != v2.PolicyHash(policy) || session.Mode != policy.Mode)) {
			if err := e.startHandshake(ctx, policy, req.RecipientURN); err != nil {
				return e.retryV2(req.MessageID, entry.attempts, err)
			}
			return nil
		}
		if err != nil {
			return e.retryV2(req.MessageID, entry.attempts, err)
		}
		recipientX, err := e.resolveRecipient(ctx, req.RecipientURN)
		if err != nil {
			return e.retryV2(req.MessageID, entry.attempts, err)
		}
		if session.PeerKeyID == "" || session.PeerKeyID != v2.KeyID(recipientX) {
			if err := e.ds.mailbox.deleteV2Session(req.RecipientURN); err != nil {
				return e.retryV2(req.MessageID, entry.attempts, err)
			}
			if err := e.startHandshake(ctx, policy, req.RecipientURN); err != nil {
				return e.retryV2(req.MessageID, entry.attempts, err)
			}
			return nil
		}
		sequence := session.SendSequence + 1
		direction := "a_to_b"
		if e.client.URN == session.ResponderURN {
			direction = "b_to_a"
		}
		deadline := time.Now().Add(7 * 24 * time.Hour).Unix()
		if deadline > policy.ExpiresAt {
			deadline = policy.ExpiresAt
		}
		h := v2.Header{Version: v2.Version, PlatformID: policy.PlatformID, PolicyEpoch: policy.Epoch, PolicyHash: v2.PolicyHash(policy), Mode: policy.Mode, Suite: policy.Suite, SenderURN: e.client.URN, RecipientURN: req.RecipientURN, SessionID: session.ID, Direction: direction, Sequence: sequence, MessageID: req.MessageID, Expiry: deadline, ContentType: "application/agent-comm+json", RecipientKeyID: v2.KeyID(recipientX)}
		plaintext, err := json.Marshal(wireMessage{Version: 2, MessageFields: req.MessageFields})
		if err != nil {
			return err
		}
		var env *v2.Envelope
		var cek []byte
		if policy.Mode == v2.ModePrivate {
			key, keyErr := session.PrivateMessageKey(direction, sequence)
			if keyErr != nil {
				return e.retryV2(req.MessageID, entry.attempts, keyErr)
			}
			env, err = v2.SealPrivate(policy, h, plaintext, key, e.client.IdentityPrivate)
		} else {
			env, cek, err = v2.SealCompliance(policy, h, plaintext, recipientX, e.client.IdentityPrivate)
		}
		if err != nil {
			return e.retryV2(req.MessageID, entry.attempts, err)
		}
		raw, err := v2.Canonical(env)
		if err != nil {
			return err
		}
		entry.envelope, entry.cek, err = e.ds.mailbox.saveV2Envelope(req.MessageID, req.RecipientURN, sequence, raw, cek, v2.PolicyHash(policy), session.ID)
		if err != nil {
			return e.retryV2(req.MessageID, entry.attempts, err)
		}
	}
	if _, err := v2.VerifyEnvelope(policy, e.client.IdentityPublic, entry.envelope, time.Now()); err != nil {
		return e.ds.mailbox.updateV2Outgoing(req.MessageID, "quarantined", err.Error(), entry.attempts, nil)
	}
	if policy.Mode == v2.ModeCompliance && !v2.ComplianceAllowed(e.keysDir, policy) {
		return errors.New("compliance disclosure was revoked before platform store")
	}
	receipt, err := e.client.StoreEnvelope(ctx, policy, entry.envelope, entry.cek)
	if err != nil {
		return e.retryV2(req.MessageID, entry.attempts, err)
	}
	rawReceipt, err := v2.Canonical(receipt)
	if err != nil {
		return err
	}
	return e.ds.mailbox.updateV2Outgoing(req.MessageID, "platform_queued", "", entry.attempts+1, rawReceipt)
}

func (e *v2Engine) retryV2(id string, attempts int, cause error) error {
	if updateErr := e.ds.mailbox.updateV2Outgoing(id, "accepted", cause.Error(), attempts+1, nil); updateErr != nil {
		return updateErr
	}
	return cause
}

func (e *v2Engine) processMessages(ctx context.Context, policy *v2.Policy) error {
	items, err := e.client.RetrieveMessages(ctx)
	if err != nil {
		return err
	}
	var failures []error
	for _, item := range items {
		if err := e.processOneMessage(policy, item); err != nil {
			failures = append(failures, fmt.Errorf("v2 message %s: %w", item.MessageID, err))
			continue
		}
		if err := e.client.AckMessages(ctx, []string{item.MessageID}); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (e *v2Engine) processOneMessage(policy *v2.Policy, item v2.MessageItem) error {
	if policy.Mode == v2.ModeCompliance && !v2.ComplianceAllowed(e.keysDir, policy) {
		return errors.New("compliance disclosure is not locally authorized")
	}
	env, err := v2.ParseEnvelope(item.Envelope)
	if err != nil {
		return err
	}
	if item.MessageID != env.Header.MessageID || env.Header.RecipientURN != e.client.URN {
		return errors.New("v2 queue identity mismatch")
	}
	if env.Header.RecipientKeyID != v2.KeyID(e.ds.agent.Keys.X25519PK) {
		return errors.New("recipient key changed since envelope creation")
	}
	peer, err := e.pinnedPeer(env.Header.SenderURN)
	if err != nil {
		return err
	}
	env, err = v2.VerifyEnvelope(policy, peer, item.Envelope, time.Now())
	if err != nil {
		return err
	}
	session, err := e.ds.mailbox.loadV2Session(env.Header.SenderURN)
	if err != nil {
		return err
	}
	if !session.Ready() || session.ID != env.Header.SessionID || session.PolicyHash != env.Header.PolicyHash || session.Mode != env.Header.Mode {
		return errors.New("v2 session not verified for received envelope")
	}
	direction := "a_to_b"
	if e.client.URN == session.InitiatorURN {
		direction = "b_to_a"
	}
	if env.Header.Direction != direction {
		return errors.New("v2 direction mismatch")
	}
	var cek, plaintext []byte
	if policy.Mode == v2.ModePrivate {
		key, keyErr := session.PrivateMessageKey(direction, env.Header.Sequence)
		if keyErr != nil {
			return keyErr
		}
		plaintext, err = v2.OpenPrivate(policy, env, key)
	} else {
		cek, plaintext, err = v2.RecipientOpenCompliance(policy, env, e.ds.agent.Keys.X25519SK)
	}
	if err != nil {
		return err
	}
	receipt, err := v2.ParseReceipt(item.Receipt)
	if err != nil {
		return err
	}
	if err := v2.VerifyReceipt(policy, receipt, item.Envelope, cek, time.Now()); err != nil {
		return err
	}
	if err := v2.ValidateBody(plaintext); err != nil {
		return err
	}
	var wire wireMessage
	if err := json.Unmarshal(plaintext, &wire); err != nil || wire.Version != 2 {
		return errors.New("v2 body is not an Agent Comm message")
	}
	if err := wire.MessageFields.validate(); err != nil {
		return err
	}
	if policy.Mode == v2.ModeCompliance && !v2.ComplianceAllowed(e.keysDir, policy) {
		return errors.New("compliance disclosure was revoked before inbox commit")
	}
	msg := InboxMessage{MessageID: env.Header.MessageID, SenderURN: env.Header.SenderURN, MessageFields: wire.MessageFields, Mode: env.Header.Mode, PolicyEpoch: env.Header.PolicyEpoch, GatewayKeyID: env.Header.GatewayKeyID, EnvelopeHash: v2.EnvelopeHash(item.Envelope)}
	if err := e.ds.mailbox.receiveV2(msg, env); err != nil {
		return err
	}
	e.ds.broadcast()
	return nil
}
