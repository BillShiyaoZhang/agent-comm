// Package session handles authenticated encrypted peer-to-peer message exchange.
package session

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/BillShiyaoZhang/agent-comm/crypto"
	pb "github.com/BillShiyaoZhang/agent-comm/proto"
	"github.com/google/uuid"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	goproto "google.golang.org/protobuf/proto"
)

const ProtoID = "/hermes/agent-comm/session/1.0.0"
const ProtoAAD = "agent-comm-v1"

type Manager struct {
	host         host.Host
	ecies        *crypto.ECIES
	keys         *crypto.IdentityKeys
	peerMu       sync.RWMutex
	peerX25519PK map[peer.ID][]byte
}

func NewManager(h host.Host, keys *crypto.IdentityKeys) *Manager {
	return &Manager{host: h, ecies: crypto.NewECIES(), keys: keys, peerX25519PK: make(map[peer.ID][]byte)}
}
func (m *Manager) PublicKey() ([]byte, error) {
	if len(m.keys.X25519PK) == 0 {
		return nil, fmt.Errorf("X25519 public key not initialized")
	}
	return append([]byte(nil), m.keys.X25519PK...), nil
}
func (m *Manager) Host() host.Host      { return m.host }
func (m *Manager) Ecies() *crypto.ECIES { return m.ecies }
func (m *Manager) PeerStaticX25519PK(p peer.ID) ([]byte, error) {
	m.peerMu.RLock()
	defer m.peerMu.RUnlock()
	if pk, ok := m.peerX25519PK[p]; ok {
		return append([]byte(nil), pk...), nil
	}
	return nil, fmt.Errorf("peer static X25519 PK not found for %s", p)
}
func (m *Manager) SetPeerX25519PK(p peer.ID, pk []byte) {
	m.peerMu.Lock()
	defer m.peerMu.Unlock()
	m.peerX25519PK[p] = append([]byte(nil), pk...)
}

// BuildEnvelopeForRecipient encrypts and signs a text message with a caller-owned
// stable ID. Persist the returned envelope before the first delivery attempt.
func (m *Manager) BuildEnvelopeForRecipient(recipientURN string, recipientPubKey []byte, plaintext, messageID string) (*pb.EncryptedEnvelope, error) {
	if recipientURN == "" || messageID == "" {
		return nil, fmt.Errorf("recipient URN and message ID are required")
	}
	msg := &pb.ChatMessage{Body: &pb.ChatMessage_Text{Text: &pb.TextMessage{Text: plaintext, Timestamp: time.Now().UnixMilli()}}}
	payload, err := goproto.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("marshal message: %w", err)
	}
	sharedSecret, err := m.ecies.ComputeSharedSecret(m.keys.X25519SK, recipientPubKey)
	if err != nil {
		return nil, fmt.Errorf("ECDH: %w", err)
	}
	aad := sha256.Sum256([]byte(ProtoAAD))
	ephemeral, nonce, ciphertext, tag, err := m.ecies.EncryptWithSharedSecret(sharedSecret, payload, aad[:16])
	if err != nil {
		return nil, fmt.Errorf("encrypt: %w", err)
	}
	env := &pb.EncryptedEnvelope{SenderUrn: m.keys.Ed25519.URN(), SenderStaticPubkey: append([]byte(nil), m.keys.X25519PK...), EphemeralPubkey: ephemeral, Nonce: nonce, Ciphertext: ciphertext, Tag: tag, MessageId: messageID, RecipientUrn: recipientURN}
	if err := crypto.SignEnvelope(env, m.keys.Ed25519); err != nil {
		return nil, err
	}
	return env, nil
}

// BuildEnvelope is retained for source compatibility. A destination URN is now
// required; callers should migrate to BuildEnvelopeForRecipient for stable IDs.
func (m *Manager) BuildEnvelope(recipientPubKey []byte, plaintext string, recipientURN ...string) (*pb.EncryptedEnvelope, error) {
	if len(recipientURN) != 1 {
		return nil, fmt.Errorf("authenticated envelopes require recipient URN; use BuildEnvelopeForRecipient")
	}
	return m.BuildEnvelopeForRecipient(recipientURN[0], recipientPubKey, plaintext, uuid.NewString())
}

func VerifyEnvelope(env *pb.EncryptedEnvelope, expectedRecipientURN string) error {
	return crypto.VerifyEnvelope(env, expectedRecipientURN)
}

// DecryptEnvelope authenticates all routing and encryption fields before ECDH.
func (m *Manager) DecryptEnvelope(env *pb.EncryptedEnvelope) ([]byte, error) {
	if err := crypto.VerifyEnvelope(env, m.keys.Ed25519.URN()); err != nil {
		return nil, err
	}
	sharedSecret, err := m.ecies.ComputeSharedSecret(m.keys.X25519SK, env.SenderStaticPubkey)
	if err != nil {
		return nil, fmt.Errorf("ECDH: %w", err)
	}
	aad := sha256.Sum256([]byte(ProtoAAD))
	return m.ecies.DecryptWithSharedSecret(sharedSecret, env.EphemeralPubkey, env.Nonce, env.Ciphertext, env.Tag, aad[:16])
}

// VerifyPeerURN binds a transport's authenticated Ed25519 peer to a claimed URN.
func VerifyPeerURN(remote peer.ID, urn string) error {
	publicKey, err := remote.ExtractPublicKey()
	if err != nil {
		return fmt.Errorf("extract peer identity: %w", err)
	}
	raw, err := publicKey.Raw()
	if err != nil {
		return err
	}
	if !crypto.URNMatchesPublicKey(urn, raw) {
		return fmt.Errorf("peer identity does not match sender URN")
	}
	return nil
}

// SendMessage sends an authenticated request and waits for a signed reply. For
// durable delivery use Agent.PrepareMessage and Agent.DeliverEnvelope instead.
func (m *Manager) SendMessage(ctx context.Context, target peer.AddrInfo, recipientPubKey []byte, plaintext string) (string, error) {
	publicKey, err := target.ID.ExtractPublicKey()
	if err != nil {
		return "", err
	}
	raw, err := publicKey.Raw()
	if err != nil {
		return "", err
	}
	recipientURN := (&crypto.IdentityKeyPair{PublicKey: raw}).URN()
	env, err := m.BuildEnvelopeForRecipient(recipientURN, recipientPubKey, plaintext, uuid.NewString())
	if err != nil {
		return "", err
	}
	stream, err := m.host.NewStream(ctx, target.ID, protocol.ID(ProtoID))
	if err != nil {
		return "", fmt.Errorf("open stream: %w", err)
	}
	defer stream.Close()
	deadline := time.Now().Add(30 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = stream.SetDeadline(deadline)
	if err := writeEnvelope(stream, env); err != nil {
		return "", err
	}
	if err := stream.CloseWrite(); err != nil {
		return "", err
	}
	resp, err := ReadEnvelope(stream)
	if err != nil {
		return "", err
	}
	if err := VerifyPeerURN(target.ID, resp.SenderUrn); err != nil {
		return "", err
	}
	payload, err := m.DecryptEnvelope(resp)
	if err != nil {
		return "", err
	}
	var msg pb.ChatMessage
	if err := goproto.Unmarshal(payload, &msg); err != nil {
		return "", err
	}
	if txt := msg.GetText(); txt != nil {
		return txt.Text, nil
	}
	return "", nil
}

func (m *Manager) SendReply(stream io.Writer, recipientStaticPubKey []byte, recipientURN, plaintext string) error {
	env, err := m.BuildEnvelopeForRecipient(recipientURN, recipientStaticPubKey, plaintext, uuid.NewString())
	if err != nil {
		return err
	}
	return writeEnvelope(stream, env)
}

func writeEnvelope(w io.Writer, env *pb.EncryptedEnvelope) error {
	data, err := goproto.Marshal(env)
	if err != nil {
		return err
	}
	if err := writeUint32BE(w, uint32(len(data))); err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

// ReadEnvelope reads a bounded, length-prefixed envelope using complete reads.
func ReadEnvelope(r io.Reader) (*pb.EncryptedEnvelope, error) {
	size, err := readUint32BE(r)
	if err != nil {
		return nil, err
	}
	if size == 0 || size > crypto.MaxEnvelopeSize {
		return nil, fmt.Errorf("invalid envelope frame size: %d", size)
	}
	data := make([]byte, size)
	if _, err := io.ReadFull(r, data); err != nil {
		return nil, err
	}
	env := new(pb.EncryptedEnvelope)
	if err := goproto.Unmarshal(data, env); err != nil {
		return nil, err
	}
	return env, nil
}
func writeUint32BE(w io.Writer, v uint32) error {
	buf := [4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}
	_, err := w.Write(buf[:])
	return err
}
func readUint32BE(r io.Reader) (uint32, error) {
	var buf [4]byte
	if _, err := io.ReadFull(r, buf[:]); err != nil {
		return 0, err
	}
	return uint32(buf[0])<<24 | uint32(buf[1])<<16 | uint32(buf[2])<<8 | uint32(buf[3]), nil
}
