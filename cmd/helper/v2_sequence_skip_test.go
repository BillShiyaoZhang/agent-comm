package main

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BillShiyaoZhang/agent-comm/agent"
	"github.com/BillShiyaoZhang/agent-comm/crypto"
	"github.com/BillShiyaoZhang/agent-comm/mq"
	"github.com/BillShiyaoZhang/agent-comm/registry"
	"github.com/BillShiyaoZhang/agent-comm/v2"
)

type v2SequencePlatform struct {
	mu                    sync.Mutex
	items                 []v2.MessageItem
	acked                 map[string]bool
	policy                *v2.Policy
	receipt               ed25519.PrivateKey
	gateway               []byte
	receiver              *crypto.IdentityKeys
	dropFirstSkipResponse bool
}

func (p *v2SequencePlatform) admission(t *testing.T, raw []byte) []byte {
	t.Helper()
	env, err := v2.ParseEnvelope(raw)
	if err != nil {
		t.Fatal(err)
	}
	var cek []byte
	result := v2.ResultAcceptedUninspected
	if p.policy.Mode == v2.ModeCompliance {
		cek, _, err = v2.GatewayOpen(p.policy, env, p.gateway)
		if err != nil {
			t.Fatal(err)
		}
		result = v2.ResultDecryptedAdmitted
	}
	receipt, err := v2.MakeReceipt(p.policy, raw, cek, p.receipt, time.Now().Unix(), result)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := v2.Canonical(receipt)
	if err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, item := range p.items {
		if item.MessageID == env.Header.MessageID {
			if !bytes.Equal(item.Envelope, raw) {
				t.Fatal("Platform received different bytes under an existing message ID")
			}
			return item.Receipt
		}
	}
	p.items = append(p.items, v2.MessageItem{MessageID: env.Header.MessageID, Envelope: raw, Receipt: encoded})
	return encoded
}

func (p *v2SequencePlatform) item(id string) v2.MessageItem {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, item := range p.items {
		if item.MessageID == id {
			return item
		}
	}
	return v2.MessageItem{}
}

func (p *v2SequencePlatform) isAcked(id string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.acked[id]
}

func (p *v2SequencePlatform) count(id string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	var count int
	for _, item := range p.items {
		if item.MessageID == id {
			count++
		}
	}
	return count
}

func newV2SequencePair(t *testing.T, mode string) (*v2Engine, *v2Engine, *v2SequencePlatform) {
	t.Helper()
	senderDir, receiverDir := t.TempDir(), t.TempDir()
	senderKeys, err := crypto.LoadOrCreateIdentity(senderDir)
	if err != nil {
		t.Fatal(err)
	}
	receiverKeys, err := crypto.LoadOrCreateIdentity(receiverDir)
	if err != nil {
		t.Fatal(err)
	}
	senderMail, err := openMailbox(filepath.Join(senderDir, "mailbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { senderMail.db.Close() })
	receiverMail, err := openMailbox(filepath.Join(receiverDir, "mailbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { receiverMail.db.Close() })
	rootPub, rootPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	receiptPub, receiptPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	gateway, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	policy := &v2.Policy{Version: v2.Version, PlatformID: "skip-test-platform", Epoch: 1,
		NotBefore: now - 60, ExpiresAt: now + 3600, Mode: mode, Suite: v2.Suite,
		GatewayKeyID: "gateway", GatewayPublicKey: gateway.PublicKey().Bytes(),
		ReceiptKeyID: "receipt", ReceiptPublicKey: receiptPub}
	if err := v2.SignPolicy(policy, rootPriv); err != nil {
		t.Fatal(err)
	}
	for _, mail := range []*mailbox{senderMail, receiverMail} {
		if _, err := mail.saveV2Policy(policy); err != nil {
			t.Fatal(err)
		}
	}
	if mode == v2.ModeCompliance {
		for _, dir := range []string{senderDir, receiverDir} {
			if err := v2.PinPolicyRoot(dir, rootPub, policy.PlatformID, "synthetic test root"); err != nil {
				t.Fatal(err)
			}
			if err := v2.AllowCompliance(dir, policy, v2.PolicyHash(policy), "synthetic test authorization"); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := v2.PinRegistryPeer(receiverDir, senderKeys.Ed25519.URN(), senderKeys.Ed25519.PublicKey); err != nil {
		t.Fatal(err)
	}
	peerID, err := agent.PeerIDFromEd25519PK(receiverKeys.Ed25519.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	signature := ed25519.Sign(receiverKeys.Ed25519.PrivateKey, registry.BuildSignedMsg(
		receiverKeys.Ed25519.URN(), peerID.String(), receiverKeys.X25519PK, false, now))
	platform := &v2SequencePlatform{acked: make(map[string]bool), policy: policy,
		receipt: receiptPriv, gateway: gateway.Bytes(), receiver: receiverKeys}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/registry/resolve":
			if r.URL.Query().Get("urn") != receiverKeys.Ed25519.URN() {
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"found": true,
				"urn": receiverKeys.Ed25519.URN(), "peer_id": peerID.String(),
				"x25519_pubkey":  receiverKeys.X25519PK,
				"ed25519_pubkey": receiverKeys.Ed25519.PublicKey,
				"signature":      signature, "timestamp": now, "stores_user_data": false})
		case "/api/v2/mq/store":
			var request struct {
				Envelope []byte `json:"envelope"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			env, err := v2.ParseEnvelope(request.Envelope)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			receipt := platform.admission(t, request.Envelope)
			platform.mu.Lock()
			drop := strings.HasPrefix(env.Header.MessageID, v2SequenceSkipPrefix) && platform.dropFirstSkipResponse
			if drop {
				platform.dropFirstSkipResponse = false
			}
			platform.mu.Unlock()
			if drop {
				http.Error(w, "simulated lost admission response", http.StatusServiceUnavailable)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true,
				"message_id": env.Header.MessageID, "receipt": receipt})
		case "/api/v2/mq/retrieve":
			platform.mu.Lock()
			items := make([]v2.MessageItem, 0, len(platform.items))
			for _, item := range platform.items {
				if !platform.acked[item.MessageID] {
					items = append(items, item)
				}
			}
			platform.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"messages": items})
		case "/api/v2/mq/ack":
			var request struct {
				MessageIDs []string `json:"message_ids"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			platform.mu.Lock()
			for _, id := range request.MessageIDs {
				platform.acked[id] = true
			}
			platform.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	senderClient, err := v2.NewHTTPClient(server.URL, senderKeys.Ed25519.URN(), senderKeys.Ed25519.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	receiverClient, err := v2.NewHTTPClient(server.URL, receiverKeys.Ed25519.URN(), receiverKeys.Ed25519.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	senderClient.ExpectedPlatformID, receiverClient.ExpectedPlatformID = policy.PlatformID, policy.PlatformID
	senderDS := &DaemonServer{agent: &agent.Agent{Keys: senderKeys, MQHTTPClient: &mq.HTTPClient{BaseURL: server.URL}}, mailbox: senderMail}
	receiverDS := &DaemonServer{agent: &agent.Agent{Keys: receiverKeys}, mailbox: receiverMail}
	sender := &v2Engine{ds: senderDS, client: senderClient, root: rootPub, keysDir: senderDir, policy: policy, lastRefresh: time.Now()}
	receiver := &v2Engine{ds: receiverDS, client: receiverClient, root: rootPub, keysDir: receiverDir, policy: policy, lastRefresh: time.Now()}
	senderDS.v2, receiverDS.v2 = sender, receiver
	prk := bytes.Repeat([]byte{3}, 32)
	transcript := bytes.Repeat([]byte{4}, 32)
	senderSession := &v2.Session{ID: "ready-session", InitiatorURN: senderClient.URN,
		ResponderURN: receiverClient.URN, Mode: mode, PolicyHash: v2.PolicyHash(policy),
		PeerKeyID: v2.KeyID(receiverKeys.X25519PK), PRK: prk, TranscriptHash: transcript,
		OwnFinishedSent: true, PeerFinishedVerified: true, SendSequence: 1}
	receiverSession := *senderSession
	receiverSession.PeerKeyID = v2.KeyID(senderKeys.X25519PK)
	receiverSession.SendSequence, receiverSession.ReceiveSequence = 0, 1
	if err := senderMail.saveV2Session(receiverClient.URN, senderSession); err != nil {
		t.Fatal(err)
	}
	if err := receiverMail.saveV2Session(senderClient.URN, &receiverSession); err != nil {
		t.Fatal(err)
	}
	return sender, receiver, platform
}

func sealSequenceBusiness(t *testing.T, sender *v2Engine, receiver *v2Engine, sequence uint64, id, text string) ([]byte, []byte) {
	return sealSequenceFields(t, sender, receiver, sequence, id, MessageFields{Text: text})
}

func sealSequenceFields(t *testing.T, sender *v2Engine, receiver *v2Engine, sequence uint64, id string, fields MessageFields) ([]byte, []byte) {
	t.Helper()
	policy := sender.policy
	session, err := sender.ds.mailbox.loadV2Session(receiver.client.URN)
	if err != nil {
		t.Fatal(err)
	}
	header := v2.Header{Version: v2.Version, PlatformID: policy.PlatformID, PolicyEpoch: policy.Epoch,
		PolicyHash: v2.PolicyHash(policy), Mode: policy.Mode, Suite: policy.Suite,
		SenderURN: sender.client.URN, RecipientURN: receiver.client.URN,
		SessionID: session.ID, Direction: "a_to_b", Sequence: sequence, MessageID: id,
		Expiry: time.Now().Add(30 * time.Minute).Unix(), ContentType: v2.ContentTypeAgentJSON,
		RecipientKeyID: v2.KeyID(receiver.ds.agent.Keys.X25519PK)}
	body, err := json.Marshal(wireMessage{Version: 2, MessageFields: fields})
	if err != nil {
		t.Fatal(err)
	}
	var env *v2.Envelope
	var cek []byte
	if policy.Mode == v2.ModePrivate {
		key, err := session.PrivateMessageKey("a_to_b", sequence)
		if err != nil {
			t.Fatal(err)
		}
		env, err = v2.SealPrivate(policy, header, body, key, sender.client.IdentityPrivate)
	} else {
		env, cek, err = v2.SealCompliance(policy, header, body,
			receiver.ds.agent.Keys.X25519PK, sender.client.IdentityPrivate)
	}
	if err != nil {
		t.Fatal(err)
	}
	raw, err := v2.Canonical(env)
	if err != nil {
		t.Fatal(err)
	}
	return raw, cek
}

func TestV2LegacyReservedPrefixBusinessMessageIsStillReceived(t *testing.T) {
	for _, mode := range []string{v2.ModePrivate, v2.ModeCompliance} {
		t.Run(mode, func(t *testing.T) {
			sender, receiver, platform := newV2SequencePair(t, mode)
			const id = "v2skip_legacy-business-id"
			raw, _ := sealSequenceBusiness(t, sender, receiver, 2, id, "older release business content")
			platform.admission(t, raw)
			if err := receiver.processMessages(context.Background(), receiver.policy); err != nil {
				t.Fatalf("older ordinary message was rejected after upgrade: %v", err)
			}
			if !platform.isAcked(id) {
				t.Fatal("older ordinary message was not ACKed after durable receipt")
			}
			var inboxCount, skippedCount int
			if err := receiver.ds.mailbox.db.QueryRow(`SELECT COUNT(*) FROM helper_inbox WHERE message_id=?`, id).Scan(&inboxCount); err != nil || inboxCount != 1 {
				t.Fatalf("older business message missing from inbox: count=%d err=%v", inboxCount, err)
			}
			if err := receiver.ds.mailbox.db.QueryRow(`SELECT COUNT(*) FROM helper_v2_skipped_inbox`).Scan(&skippedCount); err != nil || skippedCount != 0 {
				t.Fatalf("older business message became a hidden control: count=%d err=%v", skippedCount, err)
			}
			session, err := receiver.ds.mailbox.loadV2Session(sender.client.URN)
			if err != nil || session.ReceiveSequence != 2 {
				t.Fatalf("older message did not advance one sequence: %+v err=%v", session, err)
			}
		})
	}
}

func TestV2PrivateSequenceControlNeedsDedicatedKey(t *testing.T) {
	sender, receiver, platform := newV2SequencePair(t, v2.ModePrivate)
	zero := 0
	fields := MessageFields{Text: v2SequenceSkipText, Kind: v2SequenceSkipKind,
		InReplyTo: "old-message", TaskID: strings.Repeat("a", 64), HopLimit: &zero}
	raw, _ := sealSequenceFields(t, sender, receiver, 2, "v2skip_old-normal-key", fields)
	platform.admission(t, raw)
	if err := receiver.processMessages(context.Background(), receiver.policy); err == nil {
		t.Fatal("ordinary-key envelope was accepted as a sequence control")
	}
	if platform.isAcked("v2skip_old-normal-key") {
		t.Fatal("invalid control was ACKed")
	}
	var skippedCount int
	if err := receiver.ds.mailbox.db.QueryRow(`SELECT COUNT(*) FROM helper_v2_skipped_inbox`).Scan(&skippedCount); err != nil || skippedCount != 0 {
		t.Fatal("invalid control advanced the hidden sequence ledger")
	}
}

func TestV2SequenceSkipRepairsAdmittedLaterMessage(t *testing.T) {
	for _, mode := range []string{v2.ModePrivate, v2.ModeCompliance} {
		t.Run(mode, func(t *testing.T) {
			sender, receiver, platform := newV2SequencePair(t, mode)
			const firstID, laterID = "collided-reply", "later-reply"
			var original, later []byte
			var laterCEK []byte
			for _, item := range []struct {
				id, text string
				sequence uint64
			}{
				{firstID, "original user reply", 2},
				{laterID, "later user reply", 3},
			} {
				if _, err := sender.ds.mailbox.acceptV2(StoreRequest{MessageID: item.id,
					RecipientURN: receiver.client.URN, MessageFields: MessageFields{Text: item.text}}); err != nil {
					t.Fatal(err)
				}
				raw, cek := sealSequenceBusiness(t, sender, receiver, item.sequence, item.id, item.text)
				if _, _, err := sender.ds.mailbox.saveV2Envelope(item.id, receiver.client.URN,
					item.sequence, raw, cek, v2.PolicyHash(sender.policy), "ready-session"); err != nil {
					t.Fatal(err)
				}
				if item.id == firstID {
					original = raw
				} else {
					later, laterCEK = raw, cek
				}
			}
			if err := sender.ds.mailbox.updateV2Outgoing(firstID, "conflict", v2ConflictErrorText+"\n", 41, nil); err != nil {
				t.Fatal(err)
			}
			laterReceipt := platform.admission(t, later)
			if err := sender.ds.mailbox.updateV2Outgoing(laterID, "platform_queued", "", 1, laterReceipt); err != nil {
				t.Fatal(err)
			}
			if len(laterCEK) == 0 && mode == v2.ModeCompliance {
				t.Fatal("compliance later message lacked CEK")
			}
			if err := receiver.processMessages(context.Background(), receiver.policy); err == nil || platform.isAcked(laterID) {
				t.Fatal("receiver accepted seq3 before missing seq2")
			}
			if err := sender.scheduleV2SequenceSkip(context.Background(), sender.policy); err != nil {
				t.Fatal(err)
			}
			skipID := v2SequenceSkipID(original)
			originalStatus, err := sender.ds.mailbox.v2OutgoingStatus(firstID)
			if err != nil || originalStatus["repair_message_id"] != skipID || originalStatus["repair_status"] != "accepted" {
				t.Fatalf("conflict status omitted queued repair: %+v, %v", originalStatus, err)
			}
			queued, err := sender.ds.mailbox.nextV2Outgoing()
			if err != nil || queued == nil || queued.request.MessageID != skipID {
				t.Fatalf("repair not queued: %+v, %v", queued, err)
			}
			repairBytes := append([]byte(nil), queued.envelope...)
			if err := sender.scheduleV2SequenceSkip(context.Background(), sender.policy); err != nil {
				t.Fatal(err)
			}
			again, err := sender.ds.mailbox.nextV2Outgoing()
			if err != nil || again == nil || !bytes.Equal(again.envelope, repairBytes) {
				t.Fatal("repeated scheduling replaced durable repair envelope")
			}
			platform.mu.Lock()
			platform.dropFirstSkipResponse = true
			platform.mu.Unlock()
			if err := sender.deliverNext(context.Background(), sender.policy); err == nil {
				t.Fatal("simulated lost admission response did not trigger retry")
			}
			if _, err := sender.ds.mailbox.db.Exec(`UPDATE helper_v2_outbox SET next_attempt=0 WHERE message_id=?`, skipID); err != nil {
				t.Fatal(err)
			}
			if err := sender.deliverNext(context.Background(), sender.policy); err != nil {
				t.Fatal(err)
			}
			if platform.count(skipID) != 1 {
				t.Fatal("repair retry created a second Platform envelope")
			}
			skipStatus, err := sender.ds.mailbox.v2OutgoingStatus(skipID)
			if err != nil || skipStatus["status"] != "platform_queued" || skipStatus["receipt_verified"] != true {
				t.Fatalf("repair not admitted with receipt: %+v, %v", skipStatus, err)
			}
			originalStatus, err = sender.ds.mailbox.v2OutgoingStatus(firstID)
			if err != nil || originalStatus["repair_status"] != "platform_queued" {
				t.Fatalf("conflict status omitted admitted repair: %+v, %v", originalStatus, err)
			}
			if err := receiver.processMessages(context.Background(), receiver.policy); err == nil || !platform.isAcked(skipID) || platform.isAcked(laterID) {
				t.Fatal("seq3 was not held until seq2 repair was authenticated and ACKed")
			}
			var count int
			if err := receiver.ds.mailbox.db.QueryRow(`SELECT COUNT(*) FROM helper_inbox`).Scan(&count); err != nil || count != 0 {
				t.Fatal("sequence control leaked into user inbox")
			}
			if err := receiver.ds.mailbox.db.QueryRow(`SELECT COUNT(*) FROM helper_v2_skipped_inbox`).Scan(&count); err != nil || count != 1 {
				t.Fatal("hidden sequence tombstone missing")
			}
			if err := receiver.processMessages(context.Background(), receiver.policy); err != nil || !platform.isAcked(laterID) {
				t.Fatalf("seq3 failed after seq2 repair: %v", err)
			}
			if err := receiver.processOneMessage(receiver.policy, platform.item(skipID)); err != nil {
				t.Fatalf("lost ACK replay was not idempotent: %v", err)
			}
			if err := receiver.ds.mailbox.db.QueryRow(`SELECT COUNT(*) FROM helper_inbox`).Scan(&count); err != nil || count != 1 {
				t.Fatal("business reply missing or sequence control exposed")
			}
			session, err := receiver.ds.mailbox.loadV2Session(sender.client.URN)
			if err != nil || session.ReceiveSequence != 3 {
				t.Fatalf("receiver did not advance exactly through 2 then 3: %+v %v", session, err)
			}
			var oldRaw, laterRaw []byte
			if err := sender.ds.mailbox.db.QueryRow(`SELECT envelope FROM helper_v2_outbox WHERE message_id=?`, firstID).Scan(&oldRaw); err != nil {
				t.Fatal(err)
			}
			if err := sender.ds.mailbox.db.QueryRow(`SELECT envelope FROM helper_v2_outbox WHERE message_id=?`, laterID).Scan(&laterRaw); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(oldRaw, original) || !bytes.Equal(laterRaw, later) {
				t.Fatal("original business envelopes were changed")
			}
		})
	}
}

func TestV2ReservedSequenceControlCannotUsePublicStore(t *testing.T) {
	keysDir := t.TempDir()
	keys, err := crypto.LoadOrCreateIdentity(keysDir)
	if err != nil {
		t.Fatal(err)
	}
	mail, err := openMailbox(filepath.Join(keysDir, "mailbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer mail.db.Close()
	rootPub, rootPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	policy := disclosureTestPolicy(t, rootPriv, v2.ModePrivate, 1)
	if _, err := mail.saveV2Policy(policy); err != nil {
		t.Fatal(err)
	}
	ds := &DaemonServer{agent: &agent.Agent{Keys: keys}, mailbox: mail}
	ds.v2 = &v2Engine{ds: ds, client: &v2.HTTPClient{ExpectedPlatformID: policy.PlatformID}, root: rootPub, keysDir: keysDir, policy: policy}
	for _, path := range []string{"/api/v1/mq/store", "/api/v2/mq/store"} {
		for _, req := range []StoreRequest{
			{MessageID: v2SequenceSkipPrefix + "forged", RecipientURN: keys.Ed25519.URN(), MessageFields: MessageFields{Text: "forged"}},
			{MessageID: "ordinary", RecipientURN: keys.Ed25519.URN(), MessageFields: MessageFields{Text: v2SequenceSkipText, Kind: v2SequenceSkipKind}},
		} {
			code, _ := helperRequest(t, ds, http.MethodPost, path, req)
			if code != http.StatusBadRequest {
				t.Fatalf("%s accepted reserved request %+v with HTTP %d", path, req, code)
			}
		}
	}
	var count int
	if err := mail.db.QueryRow(`SELECT COUNT(*) FROM helper_v2_outbox`).Scan(&count); err != nil || count != 0 {
		t.Fatal("reserved request persisted in v2 outbox")
	}
}

func TestV2SequenceSkipSkipsIneligibleEarlierConflict(t *testing.T) {
	sender, receiver, platform := newV2SequencePair(t, v2.ModePrivate)
	for _, item := range []struct {
		id       string
		sequence uint64
		status   string
	}{
		{"repairable-seq2", 2, "conflict"},
		{"admitted-seq3", 3, "platform_queued"},
		{"older-row-no-later-receipt", 4, "conflict"},
		{"unadmitted-seq5", 5, "accepted"},
	} {
		if _, err := sender.ds.mailbox.acceptV2(StoreRequest{MessageID: item.id,
			RecipientURN: receiver.client.URN, MessageFields: MessageFields{Text: item.id}}); err != nil {
			t.Fatal(err)
		}
		raw, cek := sealSequenceBusiness(t, sender, receiver, item.sequence, item.id, item.id)
		if _, _, err := sender.ds.mailbox.saveV2Envelope(item.id, receiver.client.URN,
			item.sequence, raw, cek, v2.PolicyHash(sender.policy), "ready-session"); err != nil {
			t.Fatal(err)
		}
		switch item.status {
		case "conflict":
			if err := sender.ds.mailbox.updateV2Outgoing(item.id, "conflict", v2ConflictErrorText+"\n", 2, nil); err != nil {
				t.Fatal(err)
			}
		case "platform_queued":
			receipt := platform.admission(t, raw)
			if err := sender.ds.mailbox.updateV2Outgoing(item.id, "platform_queued", "", 1, receipt); err != nil {
				t.Fatal(err)
			}
		}
	}
	// Row ordering puts the unrepairable seq4 first. It has no receipt at seq>4,
	// while the later row for seq2 can be repaired from the admitted seq3.
	if _, err := sender.ds.mailbox.db.Exec(`UPDATE helper_v2_outbox SET created_at=0 WHERE message_id=?`,
		"older-row-no-later-receipt"); err != nil {
		t.Fatal(err)
	}
	if err := sender.scheduleV2SequenceSkip(context.Background(), sender.policy); err != nil {
		t.Fatal(err)
	}
	var repairedOriginal string
	if err := sender.ds.mailbox.db.QueryRow(`SELECT original_message_id FROM helper_v2_sequence_repairs`).Scan(&repairedOriginal); err != nil || repairedOriginal != "repairable-seq2" {
		t.Fatalf("ineligible earlier gap blocked repairable one: %q %v", repairedOriginal, err)
	}
}
