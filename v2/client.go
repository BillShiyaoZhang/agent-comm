package v2

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type HTTPClient struct {
	BaseURL            string
	ExpectedPlatformID string
	IdentityPrivate    ed25519.PrivateKey
	IdentityPublic     ed25519.PublicKey
	URN                string
	Client             *http.Client
}

type MessageItem struct {
	MessageID string `json:"message_id"`
	Envelope  []byte `json:"envelope"`
	Receipt   []byte `json:"receipt"`
}

type FrameItem struct {
	FrameID string `json:"frame_id"`
	Frame   []byte `json:"frame"`
}

func NewHTTPClient(baseURL, urn string, private ed25519.PrivateKey) (*HTTPClient, error) {
	if len(private) != ed25519.PrivateKeySize {
		return nil, errors.New("v2 identity private key required")
	}
	public := private.Public().(ed25519.PublicKey)
	if !URNMatchesPublicKey(urn, public) {
		return nil, errors.New("identity key does not match URN")
	}
	return &HTTPClient{BaseURL: strings.TrimRight(baseURL, "/"), IdentityPrivate: private, IdentityPublic: public, URN: urn, Client: &http.Client{Timeout: 20 * time.Second}}, nil
}

func (c *HTTPClient) doJSON(ctx context.Context, method, path string, request, response any, authenticated bool) error {
	if c == nil || c.BaseURL == "" {
		return errors.New("v2 platform HTTP client unavailable")
	}
	var body []byte
	if request != nil {
		var err error
		body, err = json.Marshal(request)
		if err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	if request != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if authenticated {
		if method == http.MethodGet {
			timestamp := time.Now().Unix()
			var ts [8]byte
			binary.BigEndian.PutUint64(ts[:], uint64(timestamp))
			msg := append([]byte("mq-retrieve|"+c.URN+"|"), ts[:]...)
			req.Header.Set("X-URN", c.URN)
			req.Header.Set("X-Timestamp", fmt.Sprintf("%d", timestamp))
			req.Header.Set("X-Pubkey", hex.EncodeToString(c.IdentityPublic))
			req.Header.Set("X-Signature", hex.EncodeToString(ed25519.Sign(c.IdentityPrivate, msg)))
		} else {
			req.Header.Set("Authorization", "Ed25519 "+hex.EncodeToString(ed25519.Sign(c.IdentityPrivate, body))+":"+hex.EncodeToString(c.IdentityPublic))
		}
	}
	client := c.Client
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		message, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("v2 platform %s: HTTP %d: %s", path, resp.StatusCode, string(message))
	}
	if response != nil {
		return json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(response)
	}
	return nil
}

// FetchPolicy requires an independently provisioned policy signing root. The
// caller must persist highestEpoch before using a newer policy to send.
func (c *HTTPClient) FetchPolicy(ctx context.Context, root ed25519.PublicKey, highestEpoch uint64) (*Policy, error) {
	if c == nil || c.ExpectedPlatformID == "" {
		return nil, errors.New("independently pinned platform ID required")
	}
	var result struct {
		Policy []byte `json:"policy"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/api/v2/policy", nil, &result, false); err != nil {
		return nil, err
	}
	p, err := ParsePolicy(result.Policy)
	if err != nil {
		return nil, err
	}
	if err := VerifyPolicy(p, root, time.Now()); err != nil {
		return nil, err
	}
	if p.PlatformID != c.ExpectedPlatformID {
		return nil, errors.New("signed policy platform ID differs from independently pinned platform")
	}
	if p.Epoch < highestEpoch {
		return nil, errors.New("policy epoch rolled back")
	}
	return p, nil
}

func (c *HTTPClient) StoreEnvelope(ctx context.Context, policy *Policy, rawEnvelope, cek []byte) (*Receipt, error) {
	env, err := ParseEnvelope(rawEnvelope)
	if err != nil {
		return nil, err
	}
	if env.Header.SenderURN != c.URN {
		return nil, errors.New("cannot store another sender's envelope")
	}
	var result struct {
		OK        bool   `json:"ok"`
		MessageID string `json:"message_id"`
		Receipt   []byte `json:"receipt"`
	}
	request := struct {
		RecipientURN string `json:"recipient_urn"`
		ExpiryUnix   int64  `json:"expiry_unix"`
		Envelope     []byte `json:"envelope"`
	}{env.Header.RecipientURN, env.Header.Expiry, rawEnvelope}
	if err := c.doJSON(ctx, http.MethodPost, "/api/v2/mq/store", request, &result, true); err != nil {
		return nil, err
	}
	if !result.OK || result.MessageID != env.Header.MessageID {
		return nil, errors.New("v2 store returned a different message ID")
	}
	receipt, err := ParseReceipt(result.Receipt)
	if err != nil {
		return nil, err
	}
	if err := VerifyReceipt(policy, receipt, rawEnvelope, cek, time.Now()); err != nil {
		return nil, err
	}
	return receipt, nil
}

func (c *HTTPClient) RetrieveMessages(ctx context.Context) ([]MessageItem, error) {
	var result struct {
		Messages []MessageItem `json:"messages"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/api/v2/mq/retrieve", nil, &result, true); err != nil {
		return nil, err
	}
	return result.Messages, nil
}

func (c *HTTPClient) AckMessages(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	var result struct {
		OK bool `json:"ok"`
	}
	request := struct {
		RecipientURN string   `json:"recipient_urn"`
		Timestamp    int64    `json:"timestamp"`
		MessageIDs   []string `json:"message_ids"`
	}{c.URN, time.Now().Unix(), ids}
	if err := c.doJSON(ctx, http.MethodPost, "/api/v2/mq/ack", request, &result, true); err != nil {
		return err
	}
	if !result.OK {
		return errors.New("v2 message ACK rejected")
	}
	return nil
}

func (c *HTTPClient) StoreFrame(ctx context.Context, frame *HandshakeFrame) (string, error) {
	if frame == nil || frame.SenderURN != c.URN {
		return "", errors.New("cannot store another sender's frame")
	}
	raw, err := Canonical(frame)
	if err != nil {
		return "", err
	}
	var result struct {
		OK      bool   `json:"ok"`
		FrameID string `json:"frame_id"`
	}
	request := struct {
		Frame []byte `json:"frame"`
	}{raw}
	if err := c.doJSON(ctx, http.MethodPost, "/api/v2/handshake/store", request, &result, true); err != nil {
		return "", err
	}
	if !result.OK {
		return "", errors.New("handshake frame rejected")
	}
	if result.FrameID != "" && result.FrameID != FrameHash(frame) {
		return "", errors.New("handshake frame ID mismatch")
	}
	return result.FrameID, nil
}

func (c *HTTPClient) RetrieveFrames(ctx context.Context, limit int) ([]FrameItem, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	var result struct {
		Frames []FrameItem `json:"frames"`
	}
	request := struct {
		RecipientURN string `json:"recipient_urn"`
		Limit        int    `json:"limit"`
	}{c.URN, limit}
	if err := c.doJSON(ctx, http.MethodPost, "/api/v2/handshake/retrieve", request, &result, true); err != nil {
		return nil, err
	}
	return result.Frames, nil
}

func (c *HTTPClient) AckFrames(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	var result struct {
		OK bool `json:"ok"`
	}
	request := struct {
		RecipientURN string   `json:"recipient_urn"`
		FrameIDs     []string `json:"frame_ids"`
	}{c.URN, ids}
	if err := c.doJSON(ctx, http.MethodPost, "/api/v2/handshake/ack", request, &result, true); err != nil {
		return err
	}
	if !result.OK {
		return errors.New("handshake ACK rejected")
	}
	return nil
}
