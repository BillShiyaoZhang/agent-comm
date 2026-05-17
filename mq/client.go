// Package mq provides the async message queue client for storing and retrieving
// offline messages via a relay node.
package mq

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/nousresearch/hermes-agent/agent-comm/proto"
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