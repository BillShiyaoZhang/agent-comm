package session

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/BillShiyaoZhang/agent-comm/crypto"
	pb "github.com/BillShiyaoZhang/agent-comm/proto"
	"github.com/libp2p/go-libp2p/core/peer"
	goproto "google.golang.org/protobuf/proto"
)

func testKeys(t *testing.T) *crypto.IdentityKeys {
	t.Helper()
	ed, err := crypto.GenerateIdentityKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	sk, pk, err := crypto.GenerateX25519KeyPair()
	if err != nil {
		t.Fatal(err)
	}
	return &crypto.IdentityKeys{Ed25519: ed, X25519SK: sk, X25519PK: pk}
}

func TestSignedEnvelopeRejectsTampering(t *testing.T) {
	alice, bob, mallory := testKeys(t), testKeys(t), testKeys(t)
	bob.Ed25519.URNPrefix = "urn:custom:agent"
	sender, receiver := NewManager(nil, alice), NewManager(nil, bob)
	env, err := sender.BuildEnvelopeForRecipient(bob.Ed25519.URN(), bob.X25519PK, "hello", "stable-id")
	if err != nil {
		t.Fatal(err)
	}
	payload, err := receiver.DecryptEnvelope(env)
	if err != nil {
		t.Fatal(err)
	}
	var msg pb.ChatMessage
	if err := goproto.Unmarshal(payload, &msg); err != nil {
		t.Fatal(err)
	}
	if msg.GetText().Text != "hello" {
		t.Fatalf("unexpected plaintext: %v", &msg)
	}
	changes := map[string]func(*pb.EncryptedEnvelope){
		"sender URN":       func(e *pb.EncryptedEnvelope) { e.SenderUrn = mallory.Ed25519.URN() },
		"recipient URN":    func(e *pb.EncryptedEnvelope) { e.RecipientUrn = alice.Ed25519.URN() },
		"message ID":       func(e *pb.EncryptedEnvelope) { e.MessageId = "replacement-id" },
		"signing key":      func(e *pb.EncryptedEnvelope) { e.SenderEd25519Pubkey = mallory.Ed25519.PublicKey },
		"static key":       func(e *pb.EncryptedEnvelope) { e.SenderStaticPubkey[0] ^= 1 },
		"ephemeral key":    func(e *pb.EncryptedEnvelope) { e.EphemeralPubkey[0] ^= 1 },
		"nonce":            func(e *pb.EncryptedEnvelope) { e.Nonce[0] ^= 1 },
		"ciphertext":       func(e *pb.EncryptedEnvelope) { e.Ciphertext[0] ^= 1 },
		"tag":              func(e *pb.EncryptedEnvelope) { e.Tag[0] ^= 1 },
		"signature":        func(e *pb.EncryptedEnvelope) { e.Signature[0] ^= 1 },
		"legacy unsigned":  func(e *pb.EncryptedEnvelope) { e.Signature = nil; e.SenderEd25519Pubkey = nil; e.RecipientUrn = "" },
		"bad nonce length": func(e *pb.EncryptedEnvelope) { e.Nonce = e.Nonce[:1] },
		"unknown field":    func(e *pb.EncryptedEnvelope) { e.ProtoReflect().SetUnknown([]byte{0x78, 0x01}) },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			altered := goproto.Clone(env).(*pb.EncryptedEnvelope)
			change(altered)
			if _, err := receiver.DecryptEnvelope(altered); err == nil {
				t.Fatal("tampered envelope accepted")
			}
		})
	}
	if _, err := receiver.DecryptEnvelope(nil); err == nil {
		t.Fatal("nil envelope accepted")
	}
}

func TestSenderCannotForgeAnotherIdentity(t *testing.T) {
	alice, bob, mallory := testKeys(t), testKeys(t), testKeys(t)
	env, err := NewManager(nil, mallory).BuildEnvelopeForRecipient(bob.Ed25519.URN(), bob.X25519PK, "forged", "forged-id")
	if err != nil {
		t.Fatal(err)
	}
	// A malicious sender owns this ECDH key and can construct valid ciphertext,
	// but cannot attach somebody else's sender label or signing public key.
	env.SenderUrn = alice.Ed25519.URN()
	if _, err := NewManager(nil, bob).DecryptEnvelope(env); err == nil {
		t.Fatal("forged sender accepted")
	}
	pidString, err := mallory.PeerID()
	if err != nil {
		t.Fatal(err)
	}
	pid, err := peer.Decode(pidString)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyPeerURN(pid, alice.Ed25519.URN()); err == nil {
		t.Fatal("peer allowed to claim another URN")
	}
	if err := VerifyPeerURN(pid, mallory.Ed25519.URN()); err != nil {
		t.Fatal(err)
	}
}

type oneByteReader struct{ *bytes.Reader }

func (r oneByteReader) Read(p []byte) (int, error) {
	if len(p) > 1 {
		p = p[:1]
	}
	return r.Reader.Read(p)
}

func TestReadEnvelopeHandlesFragmentedFramesAndBounds(t *testing.T) {
	alice, bob := testKeys(t), testKeys(t)
	env, err := NewManager(nil, alice).BuildEnvelopeForRecipient(bob.Ed25519.URN(), bob.X25519PK, "text", "framed-id")
	if err != nil {
		t.Fatal(err)
	}
	var frame bytes.Buffer
	if err := writeEnvelope(&frame, env); err != nil {
		t.Fatal(err)
	}
	decoded, err := ReadEnvelope(oneByteReader{bytes.NewReader(frame.Bytes())})
	if err != nil {
		t.Fatal(err)
	}
	if !goproto.Equal(env, decoded) {
		t.Fatal("fragmented frame did not round trip")
	}
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], crypto.MaxEnvelopeSize+1)
	if _, err := ReadEnvelope(bytes.NewReader(size[:])); err == nil {
		t.Fatal("oversized frame accepted")
	}
}
