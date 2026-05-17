// Package dr implements the Double Ratchet (Signal Protocol) for forward-secret encrypted sessions.
// This file provides DRSession — a self-contained session wrapper that uses the
// session.Manager's ECIES only for the initial key agreement (to derive a seed),
// then handles all subsequent messages via the Double Ratchet for forward secrecy.
package dr

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"sync"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/nousresearch/hermes-agent/agent-comm/crypto"
	"github.com/nousresearch/hermes-agent/agent-comm/proto"
	"github.com/nousresearch/hermes-agent/agent-comm/session"
	"golang.org/x/crypto/curve25519"
	goproto "google.golang.org/protobuf/proto"
)

// ProtoID for DR messages over libp2p.
const ProtoID = "/agent/dr/1.0.0"

// DRSession wraps a RatchetState for a specific peer.
// It uses session.Manager's ECIES only for the bootstrap key agreement,
// then applies Double Ratchet for all subsequent messages.
type DRSession struct {
	peerURN  string
	peerID   peer.ID
	manager  *session.Manager
	keys     *crypto.IdentityKeys
	ratchet  RatchetState
	mu       sync.RWMutex
}

// NewDRSessionInitiator creates a DR session as the initiator (Alice).
// peerX25519PK is the peer's X25519 static public key from the registry.
// The initial key agreement uses ECIES ECDH to seed the Double Ratchet.
func NewDRSessionInitiator(ctx context.Context, mgr *session.Manager, keys *crypto.IdentityKeys, peerID peer.ID, peerX25519PK []byte, peerURN string) (*DRSession, error) {
	if len(peerX25519PK) != 32 {
		return nil, fmt.Errorf("peer X25519 pubkey must be 32 bytes")
	}

	s := &DRSession{
		peerURN: peerURN,
		peerID:  peerID,
		manager: mgr,
		keys:    keys,
	}

	// Bootstrap: use ECIES ComputeSharedSecret (ECDH with static keys)
	// as the initial shared secret to seed the Double Ratchet.
	// This is the "IK" (Identity Key) agreement from Signal's X3DH.
	sharedSecret, err := mgr.Ecies().ComputeSharedSecret(keys.X25519SK, peerX25519PK)
	if err != nil {
		return nil, fmt.Errorf("ecdh bootstrap: %w", err)
	}

	// Seed Double Ratchet as Alice (initiator sends first)
	if err := s.ratchet.InitAlice(array32(sharedSecret)); err != nil {
		return nil, fmt.Errorf("init alice: %w", err)
	}

	return s, nil
}

// NewDRSessionResponder creates a DR session as the responder (Bob).
// The ratchet is initialized when the first message arrives.
func NewDRSessionResponder(ctx context.Context, mgr *session.Manager, keys *crypto.IdentityKeys, peerID peer.ID, peerURN string) *DRSession {
	return &DRSession{
		peerURN: peerURN,
		peerID:  peerID,
		manager: mgr,
		keys:    keys,
	}
}

// Send encrypts a plaintext using the current ratchet chain and sends it over a new stream.
func (s *DRSession) Send(ctx context.Context, plaintext []byte) error {
	s.mu.Lock()
	drMsg, err := s.ratchet.Send(plaintext)
	s.mu.Unlock()
	if err != nil {
		return fmt.Errorf("dr send: %w", err)
	}

	// Serialize DR message: header (40 bytes) + ciphertext
	hdrBytes := SerializeHeader(drMsg.Header)
	msgBytes := append(hdrBytes, drMsg.Ciphertext...)

	// Open a new stream to the peer using the DR protocol
	stream, err := s.manager.Host().NewStream(ctx, s.peerID, protocol.ID(ProtoID))
	if err != nil {
		return fmt.Errorf("open stream: %w", err)
	}
	defer stream.Close()

	// Write: length-prefixed DR message
	sizeBuf := make([]byte, 4)
	binary.BigEndian.PutUint32(sizeBuf, uint32(len(msgBytes)))
	if _, err := stream.Write(sizeBuf); err != nil {
		return fmt.Errorf("write size: %w", err)
	}
	if _, err := stream.Write(msgBytes); err != nil {
		return fmt.Errorf("write dr msg: %w", err)
	}
	if err := stream.CloseWrite(); err != nil {
		return fmt.Errorf("close write: %w", err)
	}

	// Read response (if any) — DR is symmetric, so we also get a reply
	respSize, err := readUint32BE(stream)
	if err != nil {
		// In simplex mode (no response expected), stream may be already closed.
		// EOF here is acceptable — it just means the peer sent and closed.
		if err == io.EOF {
			return nil
		}
		return fmt.Errorf("read response size: %w", err)
	}
	respBytes := make([]byte, respSize)
	if _, err := io.ReadFull(stream, respBytes); err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	// Decrypt response using the now-advanced ratchet
	s.mu.Lock()
	hdr, err := DeserializeHeader(respBytes)
	if err != nil {
		s.mu.Unlock()
		return fmt.Errorf("deserialize resp header: %w", err)
	}
	ct := respBytes[40:]
	plaintextResp, err := s.ratchet.Receive(&DrMessage{Header: hdr, Ciphertext: ct})
	s.mu.Unlock()
	if err != nil {
		return fmt.Errorf("dr receive response: %w", err)
	}

	_ = plaintextResp // response handled by caller if needed
	return nil
}

// SendMessage is a helper: builds a ChatMessage, then calls Send.
func (s *DRSession) SendMessage(ctx context.Context, text string) error {
	msg := &proto.ChatMessage{
		Body: &proto.ChatMessage_Text{
			Text: &proto.TextMessage{
				Text: text,
			},
		},
	}
	payload, err := goproto.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	return s.Send(ctx, payload)
}

// Receive reads and decrypts a DR message from the stream.
// On the first message (Bob's side), it initializes the ratchet from the header.
// Returns the plaintext payload.
func (s *DRSession) Receive(ctx context.Context, stream network.Stream) ([]byte, error) {
	// Read length-prefixed DR message
	sizeBuf := make([]byte, 4)
	if _, err := io.ReadFull(stream, sizeBuf); err != nil {
		return nil, fmt.Errorf("read size: %w", err)
	}
	size := binary.BigEndian.Uint32(sizeBuf)
	if size > 1<<20 {
		return nil, fmt.Errorf("message too large: %d", size)
	}
	msgBytes := make([]byte, size)
	if _, err := io.ReadFull(stream, msgBytes); err != nil {
		return nil, fmt.Errorf("read dr msg: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// First message? Initialize Bob's ratchet from static ECDH SS, then
	// decrypt the first message BEFORE doing the DH ratchet (Signal-protocol-correct).
	if !s.ratchet.Initialized {
		if len(msgBytes) < 40 {
			return nil, fmt.Errorf("first DR message too short")
		}

		// Bob's static ECDH SS: ECDH(our_X25519_SK, their_X25519_PK)
		theirStaticPK, err := s.manager.PeerStaticX25519PK(s.peerID)
		if err != nil {
			return nil, fmt.Errorf("get peer static pk: %w", err)
		}
		dhOut, err := curve25519.X25519(s.keys.X25519SK[:], theirStaticPK)
		if err != nil {
			return nil, fmt.Errorf("ecdh static: %w", err)
		}
		var staticSS [32]byte
		copy(staticSS[:], dhOut)

		// Initialize Bob's ratchet with the static SS
		if err := s.ratchet.InitBobWithSS(staticSS); err != nil {
			return nil, fmt.Errorf("init bob with ss: %w", err)
		}

		// ReceiveMsg1 decrypts the FIRST message using the original receive chain key
		// (derived from static SS), BEFORE the DH ratchet.
		pt, theirDHPub, err := s.ratchet.ReceiveMsg1(msgBytes)
		if err != nil {
			return nil, fmt.Errorf("receive msg1: %w", err)
		}

		// NOW do the DH ratchet (Signal-protocol-correct: decrypt first, then ratchet)
		if err := s.ratchet.FinishRatchet(theirDHPub); err != nil {
			return nil, fmt.Errorf("finish ratchet: %w", err)
		}

		return pt, nil
	}

	// Deserialize and decrypt (subsequent messages)
	var hdr DrHeader
	hdr, err := DeserializeHeader(msgBytes)
	if err != nil {
		return nil, fmt.Errorf("deserialize header: %w", err)
	}
	ct := msgBytes[40:]

	plaintext, err := s.ratchet.Receive(&DrMessage{Header: hdr, Ciphertext: ct})
	if err != nil {
		return nil, fmt.Errorf("dr receive: %w", err)
	}

	return plaintext, nil
}

// PeerURN returns the peer's URN.
func (s *DRSession) PeerURN() string { return s.peerURN }

// IsInitialized reports whether the ratchet has been set up.
func (s *DRSession) IsInitialized() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ratchet.Initialized
}

// GenPeerDHSecret generates a fresh X25519 keypair for DR key exchange.
func GenPeerDHSecret() ([]byte, []byte, error) {
	var scalar [32]byte
	if _, err := rand.Read(scalar[:]); err != nil {
		return nil, nil, fmt.Errorf("rand: %w", err)
	}
	scalar[0] &= 248
	scalar[31] &= 127
	scalar[31] |= 64

	var pub, base [32]byte
	base[0] = 9
	curve25519.ScalarMult(&pub, &scalar, &base)
	return scalar[:], pub[:], nil
}

// HashSharedSecret hashes a shared secret for use as a chain key seed.
func HashSharedSecret(secret []byte) [32]byte {
	return sha256.Sum256(secret)
}

// ---- Helpers ----

func array32(b []byte) [32]byte {
	var a [32]byte
	copy(a[:], b)
	return a
}

func readUint32BE(r io.Reader) (uint32, error) {
	var buf [4]byte
	if _, err := io.ReadFull(r, buf[:]); err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint32(buf[:]), nil
}

var _ = rand.Reader // used by GenPeerDHSecret