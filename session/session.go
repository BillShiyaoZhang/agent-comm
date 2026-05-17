// Package session handles encrypted peer-to-peer message exchange over libp2p streams.
package session

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/nousresearch/hermes-agent/agent-comm/crypto"
	"github.com/nousresearch/hermes-agent/agent-comm/proto"
	goproto "google.golang.org/protobuf/proto"
)

// ProtoID is the libp2p protocol identifier for session messages.
const ProtoID = "/hermes/agent-comm/session/1.0.0"

// ProtoAAD is the protocol-level AAD constant used by both parties for encryption.
// Using a constant avoids PeerID↔URN↔staticPubKey↔PeerID derivation mismatches.
const ProtoAAD = "agent-comm-v1"

// Manager manages a session with a remote peer: send/receive encrypted messages.
type Manager struct {
	host          host.Host
	ecies         *crypto.ECIES
	keys          *crypto.IdentityKeys
	peerX25519PK  map[peer.ID][]byte // cache of known peer X25519 PKs
}

// NewManager creates a new session manager.
func NewManager(h host.Host, keys *crypto.IdentityKeys) *Manager {
	return &Manager{host: h, ecies: crypto.NewECIES(), keys: keys}
}

// PublicKey returns this node's X25519 public key.
func (m *Manager) PublicKey() ([]byte, error) {
	if len(m.keys.X25519PK) == 0 {
		return nil, fmt.Errorf("X25519 public key not initialized")
	}
	return m.keys.X25519PK, nil
}

// Host returns the underlying libp2p host.
func (m *Manager) Host() host.Host { return m.host }

// Ecies returns the underlying ECIES instance (for use by test programs and handlers).
func (m *Manager) Ecies() *crypto.ECIES { return m.ecies }

// PeerStaticX25519PK returns the peer's static X25519 public key (if known).
// This is populated during session establishment when the peer's static key
// is verified via WoT and stored in the host's peer store.
func (m *Manager) PeerStaticX25519PK(p peer.ID) ([]byte, error) {
	// Try peerstore first
	pk := m.host.Peerstore().PubKey(p)
	if pk != nil {
		if x25519pk, ok := pk.(interface{ GetX25519PublicKey() []byte }); ok {
			return x25519pk.GetX25519PublicKey(), nil
		}
	}
	// Check if we have it cached in a local map (set by SetPeerX25519PK)
	if m.peerX25519PK != nil {
		if pk, ok := m.peerX25519PK[p]; ok {
			return pk, nil
		}
	}
	_ = m.host.Peerstore().PeerInfo(p) // ensure peer is known
	return nil, fmt.Errorf("peer static X25519 PK not found for %s (must be passed during session setup)", p)
}

// SetPeerX25519PK stores a peer's X25519 public key for later retrieval.
func (m *Manager) SetPeerX25519PK(p peer.ID, pk []byte) {
	if m.peerX25519PK == nil {
		m.peerX25519PK = make(map[peer.ID][]byte)
	}
	m.peerX25519PK[p] = pk
}

// SendMessage opens a stream to target, encrypts and sends a message, waits for encrypted reply.
// recipientPubKey is the recipient's X25519 static public key (32 bytes).
func (m *Manager) SendMessage(ctx context.Context, target peer.AddrInfo, recipientPubKey []byte, plaintext string) (string, error) {
	stream, err := m.host.NewStream(ctx, target.ID, protocol.ID(ProtoID))
	if err != nil {
		return "", fmt.Errorf("open stream: %w", err)
	}
	defer stream.Close()

	msg := &proto.ChatMessage{
		Body: &proto.ChatMessage_Text{
			Text: &proto.TextMessage{
				Text:      plaintext,
				Timestamp: time.Now().UnixMilli(),
			},
		},
	}
	payload, err := goproto.Marshal(msg)
	if err != nil {
		return "", fmt.Errorf("marshal message: %w", err)
	}

	// ECIES encrypt: ECDH(sender_static_SK, recipient_static_PK) → shared secret → HKDF → AES-GCM
	// AAD = ProtoAAD (protocol-level constant, same for both parties)
	sharedSecret, err := m.ecies.ComputeSharedSecret(m.keys.X25519SK, recipientPubKey)
	if err != nil {
		return "", fmt.Errorf("ECDH: %w", err)
	}
	aad := sha256.Sum256([]byte(ProtoAAD))
	ephemeral, nonce, ciphertext, tag, err := m.ecies.EncryptWithSharedSecret(sharedSecret, payload, aad[:16])
	if err != nil {
		return "", fmt.Errorf("encrypt: %w", err)
	}

	env := &proto.EncryptedEnvelope{
		SenderUrn:          m.keys.Ed25519.URN(),
		SenderStaticPubkey: m.keys.X25519PK,
		EphemeralPubkey:    ephemeral,
		Nonce:              nonce,
		Ciphertext:         ciphertext,
		Tag:                tag,
		MessageId:          fmt.Sprintf("msg-%d", time.Now().UnixNano()),
	}
	envBytes, err := goproto.Marshal(env)
	if err != nil {
		return "", fmt.Errorf("marshal envelope: %w", err)
	}

	if err := writeUint32BE(stream, uint32(len(envBytes))); err != nil {
		return "", fmt.Errorf("send envelope header: %w", err)
	}
	if _, err := stream.Write(envBytes); err != nil {
		return "", fmt.Errorf("send envelope: %w", err)
	}
	if err := stream.CloseWrite(); err != nil {
		return "", fmt.Errorf("signal write done: %w", err)
	}

	// Read response
	respSize, err := readUint32BE(stream)
	if err != nil {
		return "", fmt.Errorf("read response size: %w", err)
	}
	respBytes := make([]byte, respSize)
	if _, err := io.ReadFull(stream, respBytes); err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	var respEnv proto.EncryptedEnvelope
	if err := goproto.Unmarshal(respBytes, &respEnv); err != nil {
		return "", fmt.Errorf("unmarshal response envelope: %w", err)
	}

	// Decrypt response
	sharedSecretResp, err := m.ecies.ComputeSharedSecret(m.keys.X25519SK, respEnv.SenderStaticPubkey)
	if err != nil {
		return "", fmt.Errorf("ECDH reply: %w", err)
	}
	aadResp := sha256.Sum256([]byte(ProtoAAD))
	plaintextResp, err := m.ecies.DecryptWithSharedSecret(
		sharedSecretResp, respEnv.EphemeralPubkey, respEnv.Nonce, respEnv.Ciphertext, respEnv.Tag, aadResp[:16])
	if err != nil {
		return "", fmt.Errorf("decrypt response: %w", err)
	}

	var respMsg proto.ChatMessage
	if err := goproto.Unmarshal(plaintextResp, &respMsg); err != nil {
		return "", fmt.Errorf("unmarshal chat message: %w", err)
	}

	if txt := respMsg.GetText(); txt != nil {
		return txt.Text, nil
	}
	return "", nil
}

// SendReply encrypts and sends a reply message over an existing stream.
func (m *Manager) SendReply(stream io.Writer, recipientStaticPubKey []byte, recipientURN, plaintext string) error {
	msg := &proto.ChatMessage{
		Body: &proto.ChatMessage_Text{
			Text: &proto.TextMessage{
				Text:      plaintext,
				Timestamp: time.Now().UnixMilli(),
			},
		},
	}
	payload, err := goproto.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	sharedSecret, err := m.ecies.ComputeSharedSecret(m.keys.X25519SK, recipientStaticPubKey)
	if err != nil {
		return fmt.Errorf("ECDH: %w", err)
	}

	aad := sha256.Sum256([]byte(ProtoAAD))
	ephemeral, nonce, ciphertext, tag, err := m.ecies.EncryptWithSharedSecret(sharedSecret, payload, aad[:16])
	if err != nil {
		return fmt.Errorf("encrypt reply: %w", err)
	}

	replyEnv := &proto.EncryptedEnvelope{
		SenderUrn:          m.keys.Ed25519.URN(),
		SenderStaticPubkey: m.keys.X25519PK,
		EphemeralPubkey:    ephemeral,
		Nonce:              nonce,
		Ciphertext:         ciphertext,
		Tag:                tag,
		MessageId:          fmt.Sprintf("reply-%d", time.Now().UnixNano()),
	}
	envBytes, err := goproto.Marshal(replyEnv)
	if err != nil {
		return fmt.Errorf("marshal reply: %w", err)
	}

	if err := writeUint32BE(stream, uint32(len(envBytes))); err != nil {
		return fmt.Errorf("send size: %w", err)
	}
	if _, err := stream.Write(envBytes); err != nil {
		return fmt.Errorf("send reply: %w", err)
	}
	return nil
}

// BuildEnvelope builds an encrypted envelope for a given plaintext and recipient pubkey.
// This is useful when the caller wants the raw envelope for MQ storage.
func (m *Manager) BuildEnvelope(recipientPubKey []byte, plaintext string) (*proto.EncryptedEnvelope, error) {
	msg := &proto.ChatMessage{
		Body: &proto.ChatMessage_Text{
			Text: &proto.TextMessage{
				Text:      plaintext,
				Timestamp: time.Now().UnixMilli(),
			},
		},
	}
	payload, err := goproto.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
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

	return &proto.EncryptedEnvelope{
		SenderUrn:          m.keys.Ed25519.URN(),
		SenderStaticPubkey: m.keys.X25519PK,
		EphemeralPubkey:    ephemeral,
		Nonce:              nonce,
		Ciphertext:         ciphertext,
		Tag:                tag,
		MessageId:          fmt.Sprintf("msg-%d", time.Now().UnixNano()),
	}, nil
}

// DecryptEnvelope decrypts an EncryptedEnvelope and returns the plaintext payload.
func (m *Manager) DecryptEnvelope(env *proto.EncryptedEnvelope) ([]byte, error) {
	sharedSecret, err := m.ecies.ComputeSharedSecret(m.keys.X25519SK, env.SenderStaticPubkey)
	if err != nil {
		return nil, fmt.Errorf("ECDH: %w", err)
	}
	aad := sha256.Sum256([]byte(ProtoAAD))
	return m.ecies.DecryptWithSharedSecret(sharedSecret, env.EphemeralPubkey, env.Nonce, env.Ciphertext, env.Tag, aad[:16])
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