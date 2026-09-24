package v2

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

const (
	kemID  uint16 = 0x0020 // DHKEM(X25519, HKDF-SHA256)
	kdfID  uint16 = 0x0001 // HKDF-SHA256
	aeadID uint16 = 0x0002 // AES-256-GCM
)

func constantTimeEqual(a, b []byte) bool { return subtle.ConstantTimeCompare(a, b) == 1 }

func hkdfExtract(salt, ikm []byte) []byte {
	if len(salt) == 0 {
		salt = make([]byte, sha256.Size)
	}
	mac := hmac.New(sha256.New, salt)
	mac.Write(ikm)
	return mac.Sum(nil)
}

func hkdfExpand(prk, info []byte, n int) ([]byte, error) {
	if n < 0 || n > 255*sha256.Size {
		return nil, errors.New("invalid HKDF output length")
	}
	out := make([]byte, 0, n)
	var last []byte
	for counter := byte(1); len(out) < n; counter++ {
		mac := hmac.New(sha256.New, prk)
		mac.Write(last)
		mac.Write(info)
		mac.Write([]byte{counter})
		last = mac.Sum(nil)
		out = append(out, last...)
	}
	return out[:n], nil
}

func suiteID(kemOnly bool) []byte {
	var pair [2]byte
	if kemOnly {
		out := []byte("KEM")
		binary.BigEndian.PutUint16(pair[:], kemID)
		return append(out, pair[:]...)
	}
	out := []byte("HPKE")
	for _, id := range []uint16{kemID, kdfID, aeadID} {
		binary.BigEndian.PutUint16(pair[:], id)
		out = append(out, pair[:]...)
	}
	return out
}

func labeledExtract(salt []byte, kemOnly bool, label string, ikm []byte) []byte {
	input := make([]byte, 0, 7+len(suiteID(kemOnly))+len(label)+len(ikm))
	input = append(input, "HPKE-v1"...)
	input = append(input, suiteID(kemOnly)...)
	input = append(input, label...)
	input = append(input, ikm...)
	return hkdfExtract(salt, input)
}

func labeledExpand(prk []byte, kemOnly bool, label string, info []byte, n int) ([]byte, error) {
	var length [2]byte
	binary.BigEndian.PutUint16(length[:], uint16(n))
	input := make([]byte, 0, 2+7+len(suiteID(kemOnly))+len(label)+len(info))
	input = append(input, length[:]...)
	input = append(input, "HPKE-v1"...)
	input = append(input, suiteID(kemOnly)...)
	input = append(input, label...)
	input = append(input, info...)
	return hkdfExpand(prk, input, n)
}

func hpkeSharedSecret(private *ecdh.PrivateKey, recipientPub []byte, enc []byte) ([]byte, error) {
	pub, err := ecdh.X25519().NewPublicKey(recipientPub)
	if err != nil {
		return nil, err
	}
	dh, err := private.ECDH(pub)
	if err != nil {
		return nil, fmt.Errorf("HPKE X25519: %w", err)
	}
	eaePRK := labeledExtract(nil, true, "eae_prk", dh)
	kemContext := make([]byte, 0, len(enc)+len(recipientPub))
	kemContext = append(kemContext, enc...)
	kemContext = append(kemContext, recipientPub...)
	return labeledExpand(eaePRK, true, "shared_secret", kemContext, 32)
}

func hpkeKeyNonce(sharedSecret, info []byte) (key, nonce []byte, err error) {
	pskIDHash := labeledExtract(nil, false, "psk_id_hash", nil)
	infoHash := labeledExtract(nil, false, "info_hash", info)
	context := append([]byte{0}, pskIDHash...)
	context = append(context, infoHash...)
	secret := labeledExtract(sharedSecret, false, "secret", nil)
	key, err = labeledExpand(secret, false, "key", context, 32)
	if err != nil {
		return nil, nil, err
	}
	nonce, err = labeledExpand(secret, false, "base_nonce", context, 12)
	return key, nonce, err
}

func aesSeal(key, nonce, plaintext, aad []byte) ([]byte, error) {
	if len(key) != 32 || len(nonce) != 12 {
		return nil, errors.New("invalid AES-256-GCM key or nonce")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return gcm.Seal(nil, nonce, plaintext, aad), nil
}

func aesOpen(key, nonce, ciphertext, aad []byte) ([]byte, error) {
	if len(key) != 32 || len(nonce) != 12 || len(ciphertext) < 16 {
		return nil, errors.New("invalid AES-256-GCM fields")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return gcm.Open(nil, nonce, ciphertext, aad)
}

// HPKESeal uses RFC 9180 Base mode, suite 0x0020/0x0001/0x0002, sequence zero.
// The caller binds the body and slot role through info and aad.
func HPKESeal(publicKey, plaintext, info, aad []byte) (enc, ciphertext []byte, err error) {
	private, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	return hpkeSealWithPrivate(private, publicKey, plaintext, info, aad)
}

func hpkeSealWithPrivate(private *ecdh.PrivateKey, publicKey, plaintext, info, aad []byte) (enc, ciphertext []byte, err error) {
	if private == nil || len(publicKey) != 32 {
		return nil, nil, errors.New("invalid HPKE key")
	}
	enc = private.PublicKey().Bytes()
	shared, err := hpkeSharedSecret(private, publicKey, enc)
	if err != nil {
		return nil, nil, err
	}
	key, nonce, err := hpkeKeyNonce(shared, info)
	if err != nil {
		return nil, nil, err
	}
	ciphertext, err = aesSeal(key, nonce, plaintext, aad)
	return enc, ciphertext, err
}

func HPKEOpen(privateKey, enc, ciphertext, info, aad []byte) ([]byte, error) {
	if len(privateKey) != 32 || len(enc) != 32 || len(ciphertext) < 16 {
		return nil, errors.New("invalid HPKE slot")
	}
	private, err := ecdh.X25519().NewPrivateKey(privateKey)
	if err != nil {
		return nil, err
	}
	public, err := ecdh.X25519().NewPublicKey(enc)
	if err != nil {
		return nil, err
	}
	dh, err := private.ECDH(public)
	if err != nil {
		return nil, fmt.Errorf("HPKE X25519: %w", err)
	}
	recipientPub := private.PublicKey().Bytes()
	eaePRK := labeledExtract(nil, true, "eae_prk", dh)
	kemContext := append(append([]byte{}, enc...), recipientPub...)
	shared, err := labeledExpand(eaePRK, true, "shared_secret", kemContext, 32)
	if err != nil {
		return nil, err
	}
	key, nonce, err := hpkeKeyNonce(shared, info)
	if err != nil {
		return nil, err
	}
	return aesOpen(key, nonce, ciphertext, aad)
}

func BodyAAD(h Header) ([]byte, error) {
	raw, err := Canonical(h)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(raw)
	return append([]byte("agent-comm-v2-body\x00"), digest[:]...), nil
}

func slotContext(h Header, ciphertext []byte, role, keyID string) ([]byte, error) {
	aad, err := BodyAAD(h)
	if err != nil {
		return nil, err
	}
	aadHash := sha256.Sum256(aad)
	ctHash := sha256.Sum256(ciphertext)
	result := append([]byte("agent-comm-v2-hpke\x00"), aadHash[:]...)
	result = append(result, ctHash[:]...)
	result = append(result, role...)
	result = append(result, 0)
	result = append(result, keyID...)
	return result, nil
}

func sealSlot(h Header, bodyCiphertext, cek, pub []byte, role, keyID string) (Slot, error) {
	context, err := slotContext(h, bodyCiphertext, role, keyID)
	if err != nil {
		return Slot{}, err
	}
	enc, encrypted, err := HPKESeal(pub, cek, context, context)
	if err != nil {
		return Slot{}, err
	}
	return Slot{Role: role, KeyID: keyID, Enc: enc, Ciphertext: encrypted}, nil
}

func openSlot(h Header, bodyCiphertext []byte, slot Slot, privateKey []byte) ([]byte, error) {
	context, err := slotContext(h, bodyCiphertext, slot.Role, slot.KeyID)
	if err != nil {
		return nil, err
	}
	return HPKEOpen(privateKey, slot.Enc, slot.Ciphertext, context, context)
}

func validateHeaderForSeal(policy *Policy, h Header, mode string) error {
	if policy == nil || policy.Mode != mode || h.Version != Version || h.PlatformID != policy.PlatformID || h.PolicyEpoch != policy.Epoch || h.PolicyHash != PolicyHash(policy) || h.Mode != mode || h.Suite != Suite || h.SenderURN == "" || h.RecipientURN == "" || h.MessageID == "" || h.SessionID == "" || h.Sequence == 0 || h.RecipientKeyID == "" || h.ContentType != ContentTypeAgentJSON {
		return errors.New("invalid v2 message header")
	}
	return nil
}

func SealCompliance(policy *Policy, h Header, plaintext, recipientPublic []byte, senderPrivate ed25519.PrivateKey) (*Envelope, []byte, error) {
	if err := validateHeaderForSeal(policy, h, ModeCompliance); err != nil {
		return nil, nil, err
	}
	if len(recipientPublic) != 32 || len(policy.GatewayPublicKey) != 32 {
		return nil, nil, errors.New("recipient and gateway public keys required")
	}
	h.GatewayKeyID = policy.GatewayKeyID
	h.SlotRoles = []string{RoleRecipient, RoleGateway}
	cek := make([]byte, 32)
	nonce := make([]byte, 12)
	if _, err := io.ReadFull(rand.Reader, cek); err != nil {
		return nil, nil, err
	}
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, nil, err
	}
	aad, err := BodyAAD(h)
	if err != nil {
		return nil, nil, err
	}
	ciphertext, err := aesSeal(cek, nonce, plaintext, aad)
	if err != nil {
		return nil, nil, err
	}
	recipientSlot, err := sealSlot(h, ciphertext, cek, recipientPublic, RoleRecipient, h.RecipientKeyID)
	if err != nil {
		return nil, nil, err
	}
	gatewaySlot, err := sealSlot(h, ciphertext, cek, policy.GatewayPublicKey, RoleGateway, policy.GatewayKeyID)
	if err != nil {
		return nil, nil, err
	}
	env := &Envelope{Header: h, Nonce: nonce, Ciphertext: ciphertext, Slots: []Slot{recipientSlot, gatewaySlot}}
	if err := SignEnvelope(env, senderPrivate); err != nil {
		return nil, nil, err
	}
	return env, cek, nil
}

func RecipientOpenCompliance(policy *Policy, env *Envelope, recipientPrivate []byte) (cek, plaintext []byte, err error) {
	if policy == nil || env == nil || policy.Mode != ModeCompliance || env.Header.Mode != ModeCompliance || len(env.Slots) != 2 || env.Slots[0].Role != RoleRecipient || env.Slots[1].Role != RoleGateway || env.Slots[1].KeyID != policy.GatewayKeyID {
		return nil, nil, errors.New("invalid compliance slot set")
	}
	cek, err = openSlot(env.Header, env.Ciphertext, env.Slots[0], recipientPrivate)
	if err != nil {
		return nil, nil, err
	}
	aad, err := BodyAAD(env.Header)
	if err != nil {
		return nil, nil, err
	}
	plaintext, err = aesOpen(cek, env.Nonce, env.Ciphertext, aad)
	return cek, plaintext, err
}

// GatewayOpen must run after VerifyEnvelope and before MakeReceipt. It opens
// the gateway slot and authenticates the exact shared body ciphertext.
func GatewayOpen(policy *Policy, env *Envelope, gatewayPrivate []byte) (cek, plaintext []byte, err error) {
	if policy == nil || env == nil || policy.Mode != ModeCompliance || env.Header.Mode != ModeCompliance || env.Header.GatewayKeyID != policy.GatewayKeyID || len(env.Slots) != 2 || env.Slots[0].Role != RoleRecipient || env.Slots[1].Role != RoleGateway || env.Slots[1].KeyID != policy.GatewayKeyID {
		return nil, nil, errors.New("invalid gateway slot set")
	}
	cek, err = openSlot(env.Header, env.Ciphertext, env.Slots[1], gatewayPrivate)
	if err != nil {
		return nil, nil, err
	}
	aad, err := BodyAAD(env.Header)
	if err != nil {
		return nil, nil, err
	}
	plaintext, err = aesOpen(cek, env.Nonce, env.Ciphertext, aad)
	return cek, plaintext, err
}

func SealPrivate(policy *Policy, h Header, plaintext, messageKey []byte, senderPrivate ed25519.PrivateKey) (*Envelope, error) {
	if err := validateHeaderForSeal(policy, h, ModePrivate); err != nil {
		return nil, err
	}
	if len(messageKey) != 32 {
		return nil, errors.New("private session message key required")
	}
	h.GatewayKeyID = ""
	h.SlotRoles = []string{}
	nonce := make([]byte, 12)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	aad, err := BodyAAD(h)
	if err != nil {
		return nil, err
	}
	ciphertext, err := aesSeal(messageKey, nonce, plaintext, aad)
	if err != nil {
		return nil, err
	}
	env := &Envelope{Header: h, Nonce: nonce, Ciphertext: ciphertext, Slots: []Slot{}}
	if err := SignEnvelope(env, senderPrivate); err != nil {
		return nil, err
	}
	return env, nil
}

func OpenPrivate(policy *Policy, env *Envelope, messageKey []byte) ([]byte, error) {
	if policy == nil || env == nil || policy.Mode != ModePrivate || env.Header.Mode != ModePrivate || len(env.Slots) != 0 || len(messageKey) != 32 {
		return nil, errors.New("invalid private envelope or key")
	}
	aad, err := BodyAAD(env.Header)
	if err != nil {
		return nil, err
	}
	return aesOpen(messageKey, env.Nonce, env.Ciphertext, aad)
}

// Proof binds the gateway's opened CEK to the exact original envelope bytes.
func Proof(cek, rawEnvelope []byte) []byte {
	if len(cek) != 32 {
		return nil
	}
	key, _ := hkdfExpand(hkdfExtract(make([]byte, 32), cek), []byte("agent-comm-v2/admission-proof"), 32)
	digest := sha256.Sum256(rawEnvelope)
	mac := hmac.New(sha256.New, key)
	mac.Write(digest[:])
	return mac.Sum(nil)
}

func sameBytes(a, b []byte) bool { return bytes.Equal(a, b) }
