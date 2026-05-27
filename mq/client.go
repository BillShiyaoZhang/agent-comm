// Package mq provides the async message queue client for storing and retrieving
// offline messages via a relay node.
package mq

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/BillShiyaoZhang/agent-comm/crypto"
	"github.com/BillShiyaoZhang/agent-comm/proto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	goproto "google.golang.org/protobuf/proto"
)

// Client is the MQ client — used by regular agents to store/retrieve messages via relay.
type Client struct {
	host host.Host
}

// NewClient creates a new MQ client.
func NewClient(h host.Host) *Client {
	return &Client{host: h}
}

// Store sends an encrypted envelope to a relay for a specific recipient.
// The relay stores the blob keyed by recipientURN; relay cannot read the content.
// ttlDays: how many days before relay auto-deletes (0 = use relay default, typically 7).
func (c *Client) Store(ctx context.Context, relay peer.AddrInfo, recipientURN string, envelope *proto.EncryptedEnvelope, ttlDays int) (string, error) {
	stream, err := c.host.NewStream(ctx, relay.ID, protocol.ID(ProtoID))
	if err != nil {
		return "", fmt.Errorf("open stream to relay: %w", err)
	}
	defer stream.Close()

	expiry := int64(0)
	if ttlDays > 0 {
		expiry = time.Now().Add(time.Duration(ttlDays) * 24 * time.Hour).Unix()
	}

	req := &proto.MQRequest{
		Op: &proto.MQRequest_Store{
			Store: &proto.StoreRequest{
				RecipientUrn: recipientURN,
				Payload:     envelope,
				ExpiryUnix:  expiry,
			},
		},
	}

	if err := sendMQRequest(stream, req); err != nil {
		return "", fmt.Errorf("send store request: %w", err)
	}

	resp, err := readMQResponse(stream)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	if resp.GetError() != nil {
		return "", fmt.Errorf("relay error: %s", resp.GetError().Message)
	}

	storeResp := resp.GetStore()
	if storeResp == nil {
		return "", fmt.Errorf("unexpected response type")
	}
	if !storeResp.Ok {
		return "", fmt.Errorf("store failed")
	}
	return storeResp.MessageId, nil
}

// Retrieve fetches all pending messages for a recipient from a relay.
func (c *Client) Retrieve(ctx context.Context, relay peer.AddrInfo, recipientURN string) ([]*proto.EncryptedEnvelope, error) {
	stream, err := c.host.NewStream(ctx, relay.ID, protocol.ID(ProtoID))
	if err != nil {
		return nil, fmt.Errorf("open stream to relay: %w", err)
	}
	defer stream.Close()

	req := &proto.MQRequest{
		Op: &proto.MQRequest_Retrieve{
			Retrieve: &proto.RetrieveRequest{RecipientUrn: recipientURN},
		},
	}

	if err := sendMQRequest(stream, req); err != nil {
		return nil, fmt.Errorf("send retrieve request: %w", err)
	}

	resp, err := readMQResponse(stream)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.GetError() != nil {
		return nil, fmt.Errorf("relay error: %s", resp.GetError().Message)
	}

	retrieveResp := resp.GetRetrieve()
	if retrieveResp == nil {
		return nil, fmt.Errorf("unexpected response type")
	}

	return retrieveResp.Payloads, nil
}

// Ack deletes successfully processed messages from the relay.
func (c *Client) Ack(ctx context.Context, relay peer.AddrInfo, messageIDs []string) (int, error) {
	if len(messageIDs) == 0 {
		return 0, nil
	}

	stream, err := c.host.NewStream(ctx, relay.ID, protocol.ID(ProtoID))
	if err != nil {
		return 0, fmt.Errorf("open stream to relay: %w", err)
	}
	defer stream.Close()

	req := &proto.MQRequest{
		Op: &proto.MQRequest_Ack{
			Ack: &proto.AckRequest{MessageIds: messageIDs},
		},
	}

	if err := sendMQRequest(stream, req); err != nil {
		return 0, fmt.Errorf("send ack request: %w", err)
	}

	resp, err := readMQResponse(stream)
	if err != nil {
		return 0, fmt.Errorf("read response: %w", err)
	}

	if resp.GetError() != nil {
		return 0, fmt.Errorf("relay error: %s", resp.GetError().Message)
	}

	ackResp := resp.GetAck()
	if ackResp == nil {
		return 0, fmt.Errorf("unexpected response type")
	}
	return int(ackResp.DeletedCount), nil
}

// sendMQRequest marshals and sends a length-prefixed MQ request.
func sendMQRequest(w io.Writer, req *proto.MQRequest) error {
	data, err := goproto.Marshal(req)
	if err != nil {
		return err
	}
	sizeBuf := [4]byte{byte(len(data) >> 24), byte(len(data) >> 16), byte(len(data) >> 8), byte(len(data))}
	if _, err := w.Write(sizeBuf[:]); err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

// readMQResponse reads a length-prefixed MQ response.
func readMQResponse(r io.Reader) (*proto.MQResponse, error) {
	sizeBuf := make([]byte, 4)
	if _, err := io.ReadFull(r, sizeBuf); err != nil {
		return nil, fmt.Errorf("read size: %w", err)
	}
	size := uint32(sizeBuf[0])<<24 | uint32(sizeBuf[1])<<16 | uint32(sizeBuf[2])<<8 | uint32(sizeBuf[3])
	data := make([]byte, size)
	if _, err := io.ReadFull(r, data); err != nil {
		return nil, fmt.Errorf("read data: %w", err)
	}
	var resp proto.MQResponse
	if err := goproto.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	return &resp, nil
}

// HTTPClient stores, retrieves and acknowledges messages via the HTTP REST API.
type HTTPClient struct {
	BaseURL string
	Keys    *crypto.IdentityKeys
}

// NewHTTPClient creates a new MQ HTTP client.
func NewHTTPClient(baseURL string, keys *crypto.IdentityKeys) *HTTPClient {
	return &HTTPClient{
		BaseURL: baseURL,
		Keys:    keys,
	}
}

type storeReq struct {
	RecipientURN string `json:"recipient_urn"`
	ExpiryUnix   int64  `json:"expiry_unix"`
	PayloadProto []byte `json:"payload_proto"`
}

// Store sends an encrypted envelope to the platform MQ using HTTP signature verification.
func (c *HTTPClient) Store(ctx context.Context, recipientURN string, envelope *proto.EncryptedEnvelope, ttlDays int) (string, error) {
	expiry := int64(0)
	if ttlDays > 0 {
		expiry = time.Now().Add(time.Duration(ttlDays) * 24 * time.Hour).Unix()
	}

	payloadProto, err := goproto.Marshal(envelope)
	if err != nil {
		return "", fmt.Errorf("marshal envelope: %w", err)
	}

	reqObj := storeReq{
		RecipientURN: recipientURN,
		ExpiryUnix:   expiry,
		PayloadProto: payloadProto,
	}

	bodyBytes, err := json.Marshal(reqObj)
	if err != nil {
		return "", fmt.Errorf("marshal JSON: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.BaseURL+"/api/v1/mq/store", bytes.NewReader(bodyBytes))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	// Sign the body and inject into Authorization header
	payloadSig := ed25519.Sign(c.Keys.Ed25519.PrivateKey, bodyBytes)
	req.Header.Set("Authorization", "Ed25519 "+hex.EncodeToString(payloadSig)+":"+hex.EncodeToString(c.Keys.Ed25519.PublicKey))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("HTTP status %d: %s", resp.StatusCode, string(respBody))
	}

	var res map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}

	if res["ok"] != true {
		return "", fmt.Errorf("store failed: ok is not true")
	}

	msgID, _ := res["message_id"].(string)
	return msgID, nil
}

type msgItem struct {
	MessageID    string `json:"message_id"`
	PayloadProto []byte `json:"payload_proto"`
}

type retrieveResp struct {
	Count    int       `json:"count"`
	Messages []msgItem `json:"messages"`
}

// Retrieve fetches all pending messages for a recipient from the platform MQ using HTTP signature verification.
func (c *HTTPClient) Retrieve(ctx context.Context, recipientURN string) ([]*proto.EncryptedEnvelope, error) {
	timestamp := time.Now().Unix()

	// Generate signature over "mq-retrieve|<urn>|<timestamp 8 bytes big-endian>"
	tsBuf := make([]byte, 8)
	binary.BigEndian.PutUint64(tsBuf, uint64(timestamp))
	msg := append([]byte("mq-retrieve|"+recipientURN+"|"), tsBuf...)
	sig := ed25519.Sign(c.Keys.Ed25519.PrivateKey, msg)

	req, err := http.NewRequestWithContext(ctx, "GET", c.BaseURL+"/api/v1/mq/retrieve", nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("X-URN", recipientURN)
	req.Header.Set("X-Timestamp", fmt.Sprintf("%d", timestamp))
	req.Header.Set("X-Pubkey", hex.EncodeToString(c.Keys.Ed25519.PublicKey))
	req.Header.Set("X-Signature", hex.EncodeToString(sig))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("HTTP status %d: %s", resp.StatusCode, string(respBody))
	}

	var res retrieveResp
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	var envelopes []*proto.EncryptedEnvelope
	for _, item := range res.Messages {
		var env proto.EncryptedEnvelope
		if err := goproto.Unmarshal(item.PayloadProto, &env); err != nil {
			continue // skip corrupted envelope
		}
		envelopes = append(envelopes, &env)
	}

	return envelopes, nil
}

type ackReq struct {
	MessageIDs []string `json:"message_ids"`
}

// Ack deletes successfully processed messages from the platform MQ.
func (c *HTTPClient) Ack(ctx context.Context, messageIDs []string) (int, error) {
	if len(messageIDs) == 0 {
		return 0, nil
	}

	reqObj := ackReq{
		MessageIDs: messageIDs,
	}

	bodyBytes, err := json.Marshal(reqObj)
	if err != nil {
		return 0, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.BaseURL+"/api/v1/mq/ack", bytes.NewReader(bodyBytes))
	if err != nil {
		return 0, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("HTTP status %d: %s", resp.StatusCode, string(respBody))
	}

	var res map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return 0, fmt.Errorf("decode response: %w", err)
	}

	if res["ok"] != true {
		return 0, fmt.Errorf("ack failed: ok is not true")
	}

	deletedFloat, _ := res["deleted"].(float64)
	return int(deletedFloat), nil
}