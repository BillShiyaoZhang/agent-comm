// Package mq provides the async message queue relay server and client.
// Relay nodes store encrypted message blobs for offline recipients.
// A relay cannot read message contents — only the recipient can decrypt.
package mq

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/BillShiyaoZhang/agent-comm/crypto"
	"github.com/BillShiyaoZhang/agent-comm/proto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	goproto "google.golang.org/protobuf/proto"
	_ "modernc.org/sqlite"
)

const MaxFrameSize = 16 << 20      // bound allocations for untrusted wire frames
const maxRetrievePayload = 4 << 20 // leave room for transport framing/JSON encoding

const ProtoID = "/hermes/agent-comm/mq/1.0.0"

// Store defines the message queue database backend interface.
type Store interface {
	StoreEnvelope(ctx context.Context, recipientURN string, env *proto.EncryptedEnvelope, expiryUnix int64) (string, error)
	Retrieve(ctx context.Context, recipientURN string) ([]*proto.EncryptedEnvelope, error)
	Ack(ctx context.Context, recipientURN string, messageIDs []string) (int, error)
}

// SQLiteStore is an SQLite-backed implementation of Store.
type SQLiteStore struct {
	db        *sql.DB
	done      chan struct{}
	closeOnce sync.Once
}

// NewSQLiteStore opens (or creates) the MQ database.
func NewSQLiteStore(dbPath string) (*SQLiteStore, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	// Create schema
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("create schema: %w", err)
	}

	_, _ = db.Exec("ALTER TABLE messages ADD COLUMN read_at INTEGER NOT NULL DEFAULT 0")
	db.SetMaxOpenConns(1)
	s := &SQLiteStore{db: db, done: make(chan struct{})}

	// Start background expiry cleanup
	go s.cleanupLoop()

	return s, nil
}

// Close closes the database connection.
func (s *SQLiteStore) Close() error {
	s.closeOnce.Do(func() { close(s.done) })
	return s.db.Close()
}

func (s *SQLiteStore) StoreEnvelope(ctx context.Context, recipientURN string, env *proto.EncryptedEnvelope, expiryUnix int64) (string, error) {
	if err := crypto.VerifyEnvelope(env, recipientURN); err != nil {
		return "", err
	}
	if err := AuthorizeRecipient(ctx, env.SenderUrn); err != nil {
		return "", err
	}
	msgID := env.MessageId
	expiry := expiryUnix
	now := time.Now().Unix()
	if expiry < 0 || (expiry != 0 && expiry <= now) {
		return "", fmt.Errorf("expiry must be in the future")
	}
	maximumExpiry := now + 7*24*60*60
	if expiry == 0 || expiry > maximumExpiry {
		// A caller cannot create a message outside the relay retention policy.
		expiry = maximumExpiry
	}

	payloadBytes, err := goproto.Marshal(env)
	if err != nil {
		return "", fmt.Errorf("marshal payload: %w", err)
	}

	result, err := s.db.ExecContext(ctx,
		"INSERT OR IGNORE INTO messages (id, recipient, payload, expiry, stored_at) VALUES (?, ?, ?, ?, ?)",
		msgID, recipientURN, payloadBytes, expiry, time.Now().Unix(),
	)
	if err != nil {
		return "", fmt.Errorf("insert: %w", err)
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return "", err
	}
	if inserted == 0 {
		var existingRecipient string
		var existingPayload []byte
		if err := s.db.QueryRowContext(ctx, "SELECT recipient, payload FROM messages WHERE id = ?", msgID).Scan(&existingRecipient, &existingPayload); err != nil {
			return "", err
		}
		if existingRecipient != recipientURN || !bytes.Equal(existingPayload, payloadBytes) {
			return "", fmt.Errorf("message ID conflict")
		}
	}
	return msgID, nil
}

func (s *SQLiteStore) Retrieve(ctx context.Context, recipientURN string) ([]*proto.EncryptedEnvelope, error) {
	if err := AuthorizeRecipient(ctx, recipientURN); err != nil {
		return nil, err
	}
	if recipientURN == "" {
		return nil, fmt.Errorf("recipient_urn is required")
	}

	rows, err := s.db.QueryContext(ctx,
		"SELECT id, payload FROM messages WHERE recipient = ? AND read_at = 0 AND (expiry = 0 OR expiry > ?) ORDER BY stored_at, rowid LIMIT 500",
		recipientURN, time.Now().Unix(),
	)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	defer rows.Close()

	var envelopes []*proto.EncryptedEnvelope
	var totalBytes int
	for rows.Next() {
		var id string
		var payload []byte
		if err := rows.Scan(&id, &payload); err != nil {
			continue
		}
		if len(payload) > crypto.MaxEnvelopeSize {
			continue // reject oversized legacy or corrupt database records
		}
		if totalBytes+len(payload) > maxRetrievePayload {
			break // remaining messages stay pending until this batch is ACKed
		}
		var env proto.EncryptedEnvelope
		if err := goproto.Unmarshal(payload, &env); err != nil {
			continue // corrupted entry, skip
		}
		envelopes = append(envelopes, &env)
		totalBytes += len(payload)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// If nothing found, return empty but ok
	if envelopes == nil {
		envelopes = []*proto.EncryptedEnvelope{}
	}

	return envelopes, nil
}

func (s *SQLiteStore) Ack(ctx context.Context, recipientURN string, messageIDs []string) (int, error) {
	if len(messageIDs) == 0 || len(messageIDs) > 1000 {
		return 0, fmt.Errorf("1 to 1000 message_ids required")
	}

	if err := AuthorizeRecipient(ctx, recipientURN); err != nil {
		return 0, err
	}
	query := "UPDATE messages SET read_at = ? WHERE recipient = ? AND read_at = 0 AND id IN (?" + strings.Repeat(",?", len(messageIDs)-1) + ")"
	args := make([]interface{}, len(messageIDs)+2)
	args[0], args[1] = time.Now().Unix(), recipientURN
	for i, id := range messageIDs {
		args[i+2] = id
	}
	result, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("delete: %w", err)
	}

	deleted, _ := result.RowsAffected()
	return int(deleted), nil
}

func (s *SQLiteStore) cleanupLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-s.done:
			return
		case <-ticker.C:
		}
		if _, err := s.db.Exec("DELETE FROM messages WHERE expiry > 0 AND expiry < ?", time.Now().Unix()); err != nil {
			log.Printf("[mq] cleanup error: %v", err)
		}
	}
}

// Server implements the relay-side MQ storage service.
type Server struct {
	host  host.Host
	store Store
}

// NewServer creates a new MQ relay server.
func NewServer(h host.Host, store Store) (*Server, error) {
	s := &Server{host: h, store: store}

	// Register stream handler
	h.SetStreamHandler(ProtoID, s.handleStream)

	return s, nil
}

const schema = `
CREATE TABLE IF NOT EXISTS messages (
  id         TEXT PRIMARY KEY,
  recipient  TEXT NOT NULL,
  payload    BLOB NOT NULL,
  expiry     INTEGER NOT NULL,
  read_at    INTEGER NOT NULL DEFAULT 0,
  stored_at  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_recipient ON messages(recipient);
CREATE INDEX IF NOT EXISTS idx_expiry ON messages(expiry);
`

// Close closes the server (noop if store closed independently).
func (s *Server) Close() error {
	if closer, ok := s.store.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}

// handleStream services a single MQ request/response exchange.
func (s *Server) handleStream(stream network.Stream) {
	defer stream.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Bind all storage operations to the cryptographically authenticated peer.
	publicKey := stream.Conn().RemotePublicKey()
	if publicKey == nil {
		_ = stream.Reset()
		return
	}
	rawKey, err := publicKey.Raw()
	if err != nil {
		_ = stream.Reset()
		return
	}
	ctx = WithAuthenticatedPublicKey(ctx, rawKey)
	_ = stream.SetDeadline(time.Now().Add(30 * time.Second))

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
	if req == nil || req.RecipientUrn == "" {
		return errorResp("recipient_urn is required")
	}
	if req.Payload == nil {
		return errorResp("payload is required")
	}
	if err := crypto.VerifyEnvelope(req.Payload, req.RecipientUrn); err != nil {
		return errorResp(err.Error())
	}
	if err := AuthorizeRecipient(ctx, req.Payload.SenderUrn); err != nil {
		return errorResp(err.Error())
	}

	msgID, err := s.store.StoreEnvelope(ctx, req.RecipientUrn, req.Payload, req.ExpiryUnix)
	if err != nil {
		return errorResp(err.Error())
	}

	return &proto.MQResponse{
		Op: &proto.MQResponse_Store{Store: &proto.StoreResponse{Ok: true, MessageId: msgID}},
	}
}

func (s *Server) handleRetrieve(ctx context.Context, req *proto.RetrieveRequest) *proto.MQResponse {
	if req == nil || req.RecipientUrn == "" {
		return errorResp("recipient_urn is required")
	}
	if err := AuthorizeRecipient(ctx, req.RecipientUrn); err != nil {
		return errorResp(err.Error())
	}

	envelopes, err := s.store.Retrieve(ctx, req.RecipientUrn)
	if err != nil {
		return errorResp(err.Error())
	}
	// Leave room for protobuf framing. Remaining messages stay pending for the
	// next poll after this batch is acknowledged.
	batchSize, count := 0, 0
	for _, env := range envelopes {
		size := goproto.Size(env) + 10
		if batchSize+size > MaxFrameSize-256 {
			break
		}
		batchSize += size
		count++
	}
	envelopes = envelopes[:count]

	return &proto.MQResponse{
		Op: &proto.MQResponse_Retrieve{Retrieve: &proto.RetrieveResponse{Payloads: envelopes}},
	}
}

func (s *Server) handleAck(ctx context.Context, req *proto.AckRequest) *proto.MQResponse {
	if req == nil || len(req.MessageIds) == 0 {
		return errorResp("message_ids required")
	}

	recipientURN := req.RecipientUrn
	if recipientURN == "" {
		recipientURN = AuthenticatedURN(ctx)
	}
	if err := AuthorizeRecipient(ctx, recipientURN); err != nil {
		return errorResp(err.Error())
	}
	deleted, err := s.store.Ack(ctx, recipientURN, req.MessageIds)
	if err != nil {
		return errorResp(err.Error())
	}

	return &proto.MQResponse{
		Op: &proto.MQResponse_Ack{Ack: &proto.AckResponse{Ok: true, DeletedCount: int32(deleted)}},
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
	if size == 0 || size > MaxFrameSize {
		return nil, fmt.Errorf("invalid MQ frame size: %d", size)
	}
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
