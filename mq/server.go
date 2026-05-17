// Package mq provides the async message queue relay server and client.
// Relay nodes store encrypted message blobs for offline recipients.
// A relay cannot read message contents — only the recipient can decrypt.
package mq

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/nousresearch/hermes-agent/agent-comm/proto"
	goproto "google.golang.org/protobuf/proto"
	_ "modernc.org/sqlite"
)

const ProtoID = "/hermes/agent-comm/mq/1.0.0"

// Server implements the relay-side MQ storage service.
// It persists encrypted message blobs in SQLite, keyed by recipient URN.
type Server struct {
	host host.Host
	db   *sql.DB
}

// NewServer creates a new MQ relay server.
func NewServer(h host.Host, dbPath string) (*Server, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	// Create schema
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("create schema: %w", err)
	}

	s := &Server{host: h, db: db}

	// Register stream handler
	h.SetStreamHandler(ProtoID, s.handleStream)

	// Start background expiry cleanup
	go s.cleanupLoop()

	return s, nil
}

const schema = `
CREATE TABLE IF NOT EXISTS messages (
  id         TEXT PRIMARY KEY,
  recipient  TEXT NOT NULL,
  payload    BLOB NOT NULL,
  expiry     INTEGER NOT NULL,
  stored_at  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_recipient ON messages(recipient);
CREATE INDEX IF NOT EXISTS idx_expiry ON messages(expiry);
`

// Close closes the server.
func (s *Server) Close() error {
	return s.db.Close()
}

// handleStream services a single MQ request/response exchange.
func (s *Server) handleStream(stream network.Stream) {
	defer stream.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Read request
	req, err := readMQRequest(stream)
	if err != nil {
		fmt.Fprintf(stream, "read request: %v\n", err)
		stream.Close()
		return
	}

	// Dispatch
	var resp *proto.MQResponse
	switch op := req.Op.(type) {
	case *proto.MQRequest_Store:
		resp = s.handleStore(ctx, op.Store)
	case *proto.MQRequest_Retrieve:
		resp = s.handleRetrieve(ctx, op.Retrieve)
	case *proto.MQRequest_Ack:
		resp = s.handleAck(ctx, op.Ack)
	default:
		resp = &proto.MQResponse{
			Op: &proto.MQResponse_Error{Error: &proto.ErrorResponse{Message: "unknown op"}},
		}
	}

	// Marshal and send response (length-prefixed like registry)
	respBytes, err := goproto.Marshal(resp)
	if err != nil {
		fmt.Fprintf(stream, "marshal response: %v\n", err)
		stream.Close()
		return
	}

	if err := writeUint32BE(stream, uint32(len(respBytes))); err != nil {
		return
	}
	if _, err := stream.Write(respBytes); err != nil {
		return
	}
}

func (s *Server) handleStore(ctx context.Context, req *proto.StoreRequest) *proto.MQResponse {
	if req.RecipientUrn == "" {
		return errorResp("recipient_urn is required")
	}
	if req.Payload == nil {
		return errorResp("payload is required")
	}

	// Use message_id from envelope if set, otherwise generate
	msgID := req.Payload.MessageId
	if msgID == "" {
		// Generate a message ID
		msgID = fmt.Sprintf("msg-%d-%d", time.Now().UnixNano(), time.Now().UnixNano()%10000)
	}

	expiry := req.ExpiryUnix
	if expiry == 0 {
		// Default 7-day TTL
		expiry = time.Now().Add(7 * 24 * time.Hour).Unix()
	}

	payloadBytes, err := goproto.Marshal(req.Payload)
	if err != nil {
		return errorResp(fmt.Sprintf("marshal payload: %v", err))
	}

	_, err = s.db.ExecContext(ctx,
		"INSERT INTO messages (id, recipient, payload, expiry, stored_at) VALUES (?, ?, ?, ?, ?)",
		msgID, req.RecipientUrn, payloadBytes, expiry, time.Now().Unix(),
	)
	if err != nil {
		return errorResp(fmt.Sprintf("insert: %v", err))
	}

	return &proto.MQResponse{
		Op: &proto.MQResponse_Store{Store: &proto.StoreResponse{Ok: true, MessageId: msgID}},
	}
}

func (s *Server) handleRetrieve(ctx context.Context, req *proto.RetrieveRequest) *proto.MQResponse {
	if req.RecipientUrn == "" {
		return errorResp("recipient_urn is required")
	}

	rows, err := s.db.QueryContext(ctx,
		"SELECT id, payload FROM messages WHERE recipient = ? AND (expiry = 0 OR expiry > ?)",
		req.RecipientUrn, time.Now().Unix(),
	)
	if err != nil {
		return errorResp(fmt.Sprintf("query: %v", err))
	}
	defer rows.Close()

	var envelopes []*proto.EncryptedEnvelope
	for rows.Next() {
		var id string
		var payload []byte
		if err := rows.Scan(&id, &payload); err != nil {
			continue
		}
		var env proto.EncryptedEnvelope
		if err := goproto.Unmarshal(payload, &env); err != nil {
			continue // corrupted entry, skip
		}
		envelopes = append(envelopes, &env)
	}

	// If nothing found, return empty but ok
	if envelopes == nil {
		envelopes = []*proto.EncryptedEnvelope{}
	}

	return &proto.MQResponse{
		Op: &proto.MQResponse_Retrieve{Retrieve: &proto.RetrieveResponse{Payloads: envelopes}},
	}
}

func (s *Server) handleAck(ctx context.Context, req *proto.AckRequest) *proto.MQResponse {
	if len(req.MessageIds) == 0 {
		return errorResp("message_ids required")
	}

	// Build query: DELETE FROM messages WHERE id IN (?,?,...)
	query := "DELETE FROM messages WHERE id IN (?" + makeString(',', len(req.MessageIds)-1) + ")"
	args := make([]interface{}, len(req.MessageIds))
	for i, id := range req.MessageIds {
		args[i] = id
	}

	result, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return errorResp(fmt.Sprintf("delete: %v", err))
	}

	deleted, _ := result.RowsAffected()
	return &proto.MQResponse{
		Op: &proto.MQResponse_Ack{Ack: &proto.AckResponse{Ok: true, DeletedCount: int32(deleted)}},
	}
}

func (s *Server) cleanupLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		if _, err := s.db.Exec("DELETE FROM messages WHERE expiry > 0 AND expiry < ?", time.Now().Unix()); err != nil {
			log.Printf("[mq] cleanup error: %v", err)
		}
	}
}

func errorResp(msg string) *proto.MQResponse {
	return &proto.MQResponse{
		Op: &proto.MQResponse_Error{Error: &proto.ErrorResponse{Message: msg}},
	}
}

// readMQRequest reads a length-prefixed protobuf message from a stream.
func readMQRequest(r io.Reader) (*proto.MQRequest, error) {
	sizeBuf := make([]byte, 4)
	if _, err := io.ReadFull(r, sizeBuf); err != nil {
		return nil, fmt.Errorf("read size: %w", err)
	}
	size := uint32(sizeBuf[0])<<24 | uint32(sizeBuf[1])<<16 | uint32(sizeBuf[2])<<8 | uint32(sizeBuf[3])
	data := make([]byte, size)
	if _, err := io.ReadFull(r, data); err != nil {
		return nil, fmt.Errorf("read data: %w", err)
	}
	var req proto.MQRequest
	if err := goproto.Unmarshal(data, &req); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	return &req, nil
}

func writeUint32BE(w io.Writer, v uint32) error {
	buf := [4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}
	_, err := w.Write(buf[:])
	return err
}

func makeString(c byte, n int) string {
	if n <= 0 {
		return ""
	}
	b := make([]byte, n)
	for i := range b {
		b[i] = c
	}
	return string(b)
}