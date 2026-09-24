package v2

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

const (
	FrameInit     = "init"
	FrameAccept   = "accept"
	FrameFinished = "finished"
)

type HandshakeFrame struct {
	Version      int    `json:"version"`
	Type         string `json:"type"`
	SessionID    string `json:"session_id"`
	SenderURN    string `json:"sender_urn"`
	RecipientURN string `json:"recipient_urn"`
	Payload      []byte `json:"payload"`
	Signature    []byte `json:"signature"`
}

type InitPayload struct {
	EphemeralPublicKey []byte `json:"ephemeral_public_key"`
	Nonce              []byte `json:"nonce"`
	PolicyHash         string `json:"policy_hash"`
	Mode               string `json:"mode"`
	Suite              string `json:"suite"`
	RecipientKeyID     string `json:"recipient_key_id"`
	Expiry             int64  `json:"expiry"`
}

type AcceptPayload struct {
	InitHash           string `json:"init_hash"`
	EphemeralPublicKey []byte `json:"ephemeral_public_key"`
	Nonce              []byte `json:"nonce"`
	PolicyHash         string `json:"policy_hash"`
	Mode               string `json:"mode"`
	Suite              string `json:"suite"`
	RecipientKeyID     string `json:"recipient_key_id"`
	Expiry             int64  `json:"expiry"`
}

// Session is a resumable local secret. The caller must persist it in a private
// atomic store and erase ephemeral private keys after both Finished frames.
// SendSequence and ReceiveSequence are monotonic, never shared across modes.
type Session struct {
	ID                   string `json:"id"`
	InitiatorURN         string `json:"initiator_urn"`
	ResponderURN         string `json:"responder_urn"`
	Mode                 string `json:"mode"`
	PolicyHash           string `json:"policy_hash"`
	PeerKeyID            string `json:"peer_key_id"`
	TranscriptHash       []byte `json:"transcript_hash"`
	PRK                  []byte `json:"prk"`
	OwnFinishedSent      bool   `json:"own_finished_sent"`
	PeerFinishedVerified bool   `json:"peer_finished_verified"`
	SendSequence         uint64 `json:"send_sequence"`
	ReceiveSequence      uint64 `json:"receive_sequence"`
}

func ParseFrame(raw []byte) (*HandshakeFrame, error) {
	var frame HandshakeFrame
	if err := parseCanonical(raw, &frame, 16<<10); err != nil {
		return nil, err
	}
	return &frame, nil
}

func frameSignable(frame *HandshakeFrame) HandshakeFrame {
	copy := *frame
	copy.Signature = nil
	return copy
}

func SignFrame(frame *HandshakeFrame, private ed25519.PrivateKey) error {
	if frame == nil || len(private) != ed25519.PrivateKeySize {
		return errors.New("frame and signing key required")
	}
	if frame.Version != Version || frame.SessionID == "" || frame.SenderURN == "" || frame.RecipientURN == "" || len(frame.Payload) == 0 || (frame.Type != FrameInit && frame.Type != FrameAccept && frame.Type != FrameFinished) {
		return errors.New("invalid handshake frame")
	}
	preimage, err := signedBytes(frameDomain, frameSignable(frame))
	if err != nil {
		return err
	}
	frame.Signature = ed25519.Sign(private, preimage)
	return nil
}

func VerifyFrame(frame *HandshakeFrame, sender ed25519.PublicKey) error {
	if frame == nil || len(sender) != ed25519.PublicKeySize {
		return errors.New("pinned peer public key required")
	}
	if !URNMatchesPublicKey(frame.SenderURN, sender) {
		return errors.New("handshake sender URN does not match pinned key")
	}
	if frame.Version != Version || frame.SessionID == "" || frame.SenderURN == "" || frame.RecipientURN == "" || len(frame.Payload) == 0 || (frame.Type != FrameInit && frame.Type != FrameAccept && frame.Type != FrameFinished) {
		return errors.New("invalid handshake frame")
	}
	preimage, err := signedBytes(frameDomain, frameSignable(frame))
	if err != nil {
		return err
	}
	if !ed25519.Verify(sender, preimage, frame.Signature) {
		return errors.New("handshake signature invalid")
	}
	return nil
}

// ValidateFrameForRelay limits handshake frames to their fixed metadata format.
// Authentication and sender URN binding are checked separately by VerifyFrame.
func ValidateFrameForRelay(policy *Policy, frame *HandshakeFrame, now time.Time) error {
	if policy == nil || frame == nil || frame.Version != Version || len(frame.SessionID) == 0 || len(frame.SessionID) > 128 || len(frame.SenderURN) > 256 || len(frame.RecipientURN) > 256 || frame.SenderURN == frame.RecipientURN || !strings.HasPrefix(frame.SenderURN, "urn:") || !strings.HasPrefix(frame.RecipientURN, "urn:") {
		return errors.New("invalid handshake routing fields")
	}
	switch frame.Type {
	case FrameInit:
		var p InitPayload
		if err := parseCanonical(frame.Payload, &p, 4<<10); err != nil {
			return err
		}
		if len(p.EphemeralPublicKey) != 32 || len(p.Nonce) != 32 || p.PolicyHash != PolicyHash(policy) || p.Mode != policy.Mode || p.Suite != policy.Suite || p.RecipientKeyID == "" || p.Expiry <= now.Unix() || p.Expiry > now.Add(24*time.Hour).Unix() || p.Expiry > policy.ExpiresAt {
			return errors.New("invalid init payload")
		}
	case FrameAccept:
		var p AcceptPayload
		if err := parseCanonical(frame.Payload, &p, 4<<10); err != nil {
			return err
		}
		if len(p.InitHash) != 64 || len(p.EphemeralPublicKey) != 32 || len(p.Nonce) != 32 || p.PolicyHash != PolicyHash(policy) || p.Mode != policy.Mode || p.Suite != policy.Suite || p.RecipientKeyID == "" || p.Expiry <= now.Unix() || p.Expiry > now.Add(24*time.Hour).Unix() || p.Expiry > policy.ExpiresAt {
			return errors.New("invalid accept payload")
		}
		if decoded, err := hex.DecodeString(p.InitHash); err != nil || len(decoded) != 32 || p.InitHash != hex.EncodeToString(decoded) {
			return errors.New("invalid init hash")
		}
	case FrameFinished:
		if len(frame.Payload) != 32 {
			return errors.New("finished payload must be 32-byte MAC")
		}
	default:
		return errors.New("unsupported handshake frame type")
	}
	return nil
}

func FrameHash(frame *HandshakeFrame) string {
	if frame == nil {
		return ""
	}
	raw, err := Canonical(frame)
	if err != nil {
		return ""
	}
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}

func NewInit(policy *Policy, senderURN, recipientURN, recipientKeyID, sessionID string, expiry int64, senderPrivate ed25519.PrivateKey) (*HandshakeFrame, []byte, error) {
	if policy == nil || len(senderPrivate) != ed25519.PrivateKeySize || senderURN == "" || recipientURN == "" || senderURN == recipientURN || recipientKeyID == "" || sessionID == "" || expiry <= time.Now().Unix() || expiry > policy.ExpiresAt || !URNMatchesPublicKey(senderURN, senderPrivate.Public().(ed25519.PublicKey)) {
		return nil, nil, errors.New("invalid handshake init input")
	}
	ephemeral, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	nonce := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, nil, err
	}
	payload, err := Canonical(InitPayload{EphemeralPublicKey: ephemeral.PublicKey().Bytes(), Nonce: nonce, PolicyHash: PolicyHash(policy), Mode: policy.Mode, Suite: policy.Suite, RecipientKeyID: recipientKeyID, Expiry: expiry})
	if err != nil {
		return nil, nil, err
	}
	frame := &HandshakeFrame{Version: Version, Type: FrameInit, SessionID: sessionID, SenderURN: senderURN, RecipientURN: recipientURN, Payload: payload}
	if err := SignFrame(frame, senderPrivate); err != nil {
		return nil, nil, err
	}
	return frame, ephemeral.Bytes(), nil
}

func parseInit(frame *HandshakeFrame, policy *Policy, senderPub ed25519.PublicKey, now time.Time) (*InitPayload, error) {
	if frame == nil || frame.Type != FrameInit || policy == nil {
		return nil, errors.New("init frame and policy required")
	}
	if err := VerifyFrame(frame, senderPub); err != nil {
		return nil, err
	}
	var payload InitPayload
	if err := parseCanonical(frame.Payload, &payload, 4<<10); err != nil {
		return nil, err
	}
	if len(payload.EphemeralPublicKey) != 32 || len(payload.Nonce) != 32 || payload.PolicyHash != PolicyHash(policy) || payload.Mode != policy.Mode || payload.Suite != policy.Suite || payload.RecipientKeyID == "" || payload.Expiry <= now.Unix() || payload.Expiry > policy.ExpiresAt {
		return nil, errors.New("init frame policy or lifetime mismatch")
	}
	return &payload, nil
}

func NewAccept(policy *Policy, init *HandshakeFrame, pinnedInitiator ed25519.PublicKey, responderPrivate ed25519.PrivateKey, ownRecipientKeyID string, now time.Time) (*HandshakeFrame, []byte, error) {
	initPayload, err := parseInit(init, policy, pinnedInitiator, now)
	if err != nil {
		return nil, nil, err
	}
	if len(responderPrivate) != ed25519.PrivateKeySize || !URNMatchesPublicKey(init.RecipientURN, responderPrivate.Public().(ed25519.PublicKey)) || initPayload.RecipientKeyID != ownRecipientKeyID {
		return nil, nil, errors.New("init targets a different responder or key")
	}
	ephemeral, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	nonce := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, nil, err
	}
	payload, err := Canonical(AcceptPayload{InitHash: FrameHash(init), EphemeralPublicKey: ephemeral.PublicKey().Bytes(), Nonce: nonce, PolicyHash: PolicyHash(policy), Mode: policy.Mode, Suite: policy.Suite, RecipientKeyID: initPayload.RecipientKeyID, Expiry: initPayload.Expiry})
	if err != nil {
		return nil, nil, err
	}
	frame := &HandshakeFrame{Version: Version, Type: FrameAccept, SessionID: init.SessionID, SenderURN: init.RecipientURN, RecipientURN: init.SenderURN, Payload: payload}
	if err := SignFrame(frame, responderPrivate); err != nil {
		return nil, nil, err
	}
	return frame, ephemeral.Bytes(), nil
}

func parseAccept(policy *Policy, init, accept *HandshakeFrame, pinnedResponder ed25519.PublicKey, now time.Time) (*AcceptPayload, error) {
	if policy == nil || init == nil || accept == nil || accept.Type != FrameAccept || accept.SessionID != init.SessionID || accept.SenderURN != init.RecipientURN || accept.RecipientURN != init.SenderURN {
		return nil, errors.New("accept frame does not match init")
	}
	if err := VerifyFrame(accept, pinnedResponder); err != nil {
		return nil, err
	}
	var payload AcceptPayload
	if err := parseCanonical(accept.Payload, &payload, 4<<10); err != nil {
		return nil, err
	}
	var initPayload InitPayload
	if err := parseCanonical(init.Payload, &initPayload, 4<<10); err != nil {
		return nil, err
	}
	if payload.InitHash != FrameHash(init) || len(payload.EphemeralPublicKey) != 32 || len(payload.Nonce) != 32 || payload.PolicyHash != PolicyHash(policy) || payload.Mode != policy.Mode || payload.Suite != policy.Suite || payload.RecipientKeyID != initPayload.RecipientKeyID || payload.Expiry != initPayload.Expiry || payload.Expiry <= now.Unix() {
		return nil, errors.New("accept frame policy or transcript mismatch")
	}
	return &payload, nil
}

func transcriptHash(init, accept *HandshakeFrame) ([]byte, error) {
	a, err := Canonical(init)
	if err != nil {
		return nil, err
	}
	b, err := Canonical(accept)
	if err != nil {
		return nil, err
	}
	if len(a) > 1<<16 || len(b) > 1<<16 {
		return nil, errors.New("handshake transcript too long")
	}
	buf := append([]byte("agent-comm-v2-transcript\x00"), 0, 0, 0, 0)
	binary.BigEndian.PutUint32(buf[len(buf)-4:], uint32(len(a)))
	buf = append(buf, a...)
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(b)))
	buf = append(buf, size[:]...)
	buf = append(buf, b...)
	digest := sha256.Sum256(buf)
	return digest[:], nil
}

func deriveSession(policy *Policy, init, accept *HandshakeFrame, ownEphemeralPrivate, peerEphemeralPublic []byte) (*Session, error) {
	if len(ownEphemeralPrivate) != 32 || len(peerEphemeralPublic) != 32 {
		return nil, errors.New("ephemeral X25519 key required")
	}
	private, err := ecdh.X25519().NewPrivateKey(ownEphemeralPrivate)
	if err != nil {
		return nil, err
	}
	public, err := ecdh.X25519().NewPublicKey(peerEphemeralPublic)
	if err != nil {
		return nil, err
	}
	shared, err := private.ECDH(public)
	if err != nil {
		return nil, fmt.Errorf("invalid ephemeral X25519 exchange: %w", err)
	}
	transcript, err := transcriptHash(init, accept)
	if err != nil {
		return nil, err
	}
	return &Session{ID: init.SessionID, InitiatorURN: init.SenderURN, ResponderURN: init.RecipientURN, Mode: policy.Mode, PolicyHash: PolicyHash(policy), TranscriptHash: transcript, PRK: hkdfExtract(transcript, shared)}, nil
}

func CompleteInitiator(policy *Policy, init, accept *HandshakeFrame, ephemeralPrivate []byte, ownInitiator, pinnedResponder ed25519.PublicKey, now time.Time) (*Session, error) {
	if _, err := parseInit(init, policy, ownInitiator, now); err != nil {
		return nil, err
	}
	payload, err := parseAccept(policy, init, accept, pinnedResponder, now)
	if err != nil {
		return nil, err
	}
	return deriveSession(policy, init, accept, ephemeralPrivate, payload.EphemeralPublicKey)
}

func CompleteResponder(policy *Policy, init, accept *HandshakeFrame, ephemeralPrivate []byte, pinnedInitiator, ownResponder ed25519.PublicKey, now time.Time) (*Session, error) {
	payload, err := parseInit(init, policy, pinnedInitiator, now)
	if err != nil {
		return nil, err
	}
	if payload.RecipientKeyID == "" {
		return nil, errors.New("missing recipient key ID")
	}
	if _, err := parseAccept(policy, init, accept, ownResponder, now); err != nil {
		return nil, err
	}
	return deriveSession(policy, init, accept, ephemeralPrivate, payload.EphemeralPublicKey)
}

func (s *Session) finishedKey(senderURN string) ([]byte, error) {
	if s == nil || len(s.PRK) != 32 || len(s.TranscriptHash) != 32 {
		return nil, errors.New("invalid session secret")
	}
	var direction string
	if senderURN == s.InitiatorURN {
		direction = "a_to_b"
	} else if senderURN == s.ResponderURN {
		direction = "b_to_a"
	} else {
		return nil, errors.New("sender is not a session party")
	}
	return hkdfExpand(s.PRK, []byte("agent-comm-v2/finished\x00"+s.ID+"\x00"+direction), 32)
}

func (s *Session) finishedMAC(senderURN string) ([]byte, error) {
	key, err := s.finishedKey(senderURN)
	if err != nil {
		return nil, err
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(s.TranscriptHash)
	return mac.Sum(nil), nil
}

func MakeFinished(s *Session, senderURN string, senderPrivate ed25519.PrivateKey) (*HandshakeFrame, error) {
	mac, err := s.finishedMAC(senderURN)
	if err != nil {
		return nil, err
	}
	to := s.InitiatorURN
	if senderURN == s.InitiatorURN {
		to = s.ResponderURN
	}
	frame := &HandshakeFrame{Version: Version, Type: FrameFinished, SessionID: s.ID, SenderURN: senderURN, RecipientURN: to, Payload: mac}
	if err := SignFrame(frame, senderPrivate); err != nil {
		return nil, err
	}
	s.OwnFinishedSent = true
	return frame, nil
}

func VerifyFinished(s *Session, frame *HandshakeFrame, pinnedPeer ed25519.PublicKey, ownURN string) error {
	if s == nil || frame == nil || frame.Type != FrameFinished || frame.SessionID != s.ID || frame.RecipientURN != ownURN || frame.SenderURN == ownURN {
		return errors.New("finished frame session mismatch")
	}
	if err := VerifyFrame(frame, pinnedPeer); err != nil {
		return err
	}
	expected, err := s.finishedMAC(frame.SenderURN)
	if err != nil {
		return err
	}
	if !constantTimeEqual(expected, frame.Payload) {
		return errors.New("finished MAC invalid")
	}
	s.PeerFinishedVerified = true
	return nil
}

func (s *Session) Ready() bool { return s != nil && s.OwnFinishedSent && s.PeerFinishedVerified }

func (s *Session) PrivateMessageKey(direction string, sequence uint64) ([]byte, error) {
	if !s.Ready() || s.Mode != ModePrivate || len(s.PRK) != 32 || sequence == 0 || (direction != "a_to_b" && direction != "b_to_a") {
		return nil, errors.New("verified private session and nonzero sequence required")
	}
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], sequence)
	info := append([]byte("agent-comm-v2/private-body\x00"+s.ID+"\x00"+direction+"\x00"), n[:]...)
	return hkdfExpand(s.PRK, info, 32)
}
