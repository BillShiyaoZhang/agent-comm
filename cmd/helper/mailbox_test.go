package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	pb "github.com/BillShiyaoZhang/agent-comm/proto"
	goproto "google.golang.org/protobuf/proto"
)

func testDaemon(t *testing.T, path string) *DaemonServer {
	t.Helper()
	m, err := openMailbox(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.db.Close() })
	return &DaemonServer{mailbox: m, clients: make(map[ClientChan]bool), outgoing: make(chan struct{}, 1)}
}

type concurrentTransport struct {
	mu             sync.Mutex
	prepared       int
	delivered      int
	ready          chan struct{}
	prepareAllowed chan struct{}
	failureAllowed chan struct{}
	sent           [][]byte
}

func (f *concurrentTransport) PrepareMessage(_ context.Context, _, plaintext, id string) (*pb.EncryptedEnvelope, error) {
	f.mu.Lock()
	f.prepared++
	nonce := byte(f.prepared)
	f.mu.Unlock()
	f.ready <- struct{}{}
	<-f.prepareAllowed
	return &pb.EncryptedEnvelope{MessageId: id, Ciphertext: []byte(plaintext), Nonce: []byte{nonce}}, nil
}

func (f *concurrentTransport) DeliverEnvelope(_ context.Context, env *pb.EncryptedEnvelope) error {
	data, _ := goproto.Marshal(env)
	f.mu.Lock()
	f.sent = append(f.sent, data)
	f.delivered++
	n := f.delivered
	f.mu.Unlock()
	if n == 1 {
		return nil
	}
	<-f.failureAllowed
	return errors.New("late duplicate transport failure")
}

func TestConcurrentOutboxWorkersUseCommittedCiphertextAndKeepSuccess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mailbox.db")
	a, b := testDaemon(t, path), testDaemon(t, path)
	f := &concurrentTransport{ready: make(chan struct{}, 2), prepareAllowed: make(chan struct{}), failureAllowed: make(chan struct{})}
	a.transport, b.transport = f, f
	if _, err := a.mailbox.accept(StoreRequest{MessageID: "concurrent-1", RecipientURN: "urn:agent-comm:agent:bob", MessageFields: MessageFields{Text: "hello"}}); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	for _, ds := range []*DaemonServer{a, b} {
		go func(ds *DaemonServer) { _, err := ds.deliverNext(context.Background()); results <- err }(ds)
	}
	for i := 0; i < 2; i++ {
		select {
		case <-f.ready:
		case <-time.After(3 * time.Second):
			t.Fatal("workers did not race preparation")
		}
	}
	close(f.prepareAllowed)
	select {
	case err := <-results:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("successful worker did not finish")
	}
	close(f.failureAllowed)
	select {
	case err := <-results:
		if err == nil {
			t.Fatal("expected late failure")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("failed worker did not finish")
	}
	var committed []byte
	if err := a.mailbox.db.QueryRow(`SELECT envelope FROM helper_outbox WHERE message_id='concurrent-1'`).Scan(&committed); err != nil {
		t.Fatal(err)
	}
	if len(f.sent) != 2 || !bytes.Equal(f.sent[0], committed) || !bytes.Equal(f.sent[1], committed) {
		t.Fatal("a concurrent worker sent uncommitted ciphertext")
	}
	status, err := a.mailbox.outgoingStatus("concurrent-1")
	if err != nil || status["status"] != "platform_queued" || status["last_error"] != "" {
		t.Fatalf("late failure downgraded successful receipt: %v %v", status, err)
	}
}

func TestDaemonShutdownCancelsActiveSSE(t *testing.T) {
	ds := testDaemon(t, filepath.Join(t.TempDir(), "mailbox.db"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := ds.httpServer(ctx, "127.0.0.1:0")
	listener, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	defer srv.Close()
	serveDone := make(chan error, 1)
	go func() { serveDone <- srv.Serve(listener) }()
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Get("http://" + listener.Addr().String() + "/api/v1/mq/subscribe")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatal(resp.StatusCode)
	}
	cancel()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("SSE blocked graceful shutdown: %v", err)
	}
	if err := <-serveDone; !errors.Is(err, http.ErrServerClosed) {
		t.Fatal(err)
	}
	ds.clientsMu.Lock()
	defer ds.clientsMu.Unlock()
	if len(ds.clients) != 0 {
		t.Fatal("SSE subscriber survived shutdown")
	}
}

func TestHelperCustomNamespaceAndRebindingProtection(t *testing.T) {
	ds := testDaemon(t, filepath.Join(t.TempDir(), "mailbox.db"))
	w := localRequest(ds, "POST", "/api/v1/mq/store", `{"recipient_urn":"urn:example:custom-agent:abc123","message_id":"custom-1","text":"hello"}`)
	if w.Code != http.StatusPreconditionRequired {
		t.Fatalf("custom namespace was misclassified: %d %s", w.Code, w.Body)
	}
	for _, urn := range []string{"urn:agent-comm:agent:", "urn:bad namespace:abc", "https://example.com"} {
		body, _ := json.Marshal(StoreRequest{RecipientURN: urn, MessageFields: MessageFields{Text: "hi"}})
		if got := localRequest(ds, "POST", "/api/v1/mq/store", string(body)).Code; got != http.StatusBadRequest {
			t.Fatalf("bad URN %q accepted: %d", urn, got)
		}
	}
	r := httptest.NewRequest("GET", "http://attacker.example/api/v1/mq/retrieve", nil)
	r.Header.Set("Origin", "http://attacker.example")
	w = httptest.NewRecorder()
	ds.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatal("same-origin DNS rebinding reached local mailbox")
	}
}

func localRequest(ds *DaemonServer, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://127.0.0.1"+path, strings.NewReader(body))
	w := httptest.NewRecorder()
	ds.ServeHTTP(w, r)
	return w
}

func TestInboxSurvivesNoSubscriberRestartAndAck(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mailbox.db")
	ds := testDaemon(t, path)
	env := &pb.EncryptedEnvelope{SenderUrn: "urn:agent-comm:agent:alice", MessageId: "durable-1"}
	payload, _ := goproto.Marshal(&pb.ChatMessage{Body: &pb.ChatMessage_Text{Text: &pb.TextMessage{Text: `{"agent_comm":1,"text":"hello","conversation_id":"conversation-a","task_id":"task-1","hop_limit":3}`}}})
	if err := ds.receiveMessage(env, payload); err != nil {
		t.Fatal(err)
	}
	if err := ds.mailbox.db.Close(); err != nil {
		t.Fatal(err)
	}
	ds = testDaemon(t, path)
	messages, err := ds.mailbox.pending()
	if err != nil || len(messages) != 1 {
		t.Fatalf("pending=%v err=%v", messages, err)
	}
	if messages[0].Text != "hello" || messages[0].ConversationID != "conversation-a" || *messages[0].HopLimit != 3 {
		t.Fatalf("metadata lost: %+v", messages[0])
	}
	if err := ds.receiveMessage(env, payload); err != nil {
		t.Fatal(err)
	}
	w := localRequest(ds, "POST", "/api/v1/mq/ack", `{"message_ids":["durable-1"]}`)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if err := ds.mailbox.db.Close(); err != nil {
		t.Fatal(err)
	}
	ds = testDaemon(t, path)
	if err := ds.receiveMessage(env, payload); err != nil {
		t.Fatal(err)
	}
	messages, err = ds.mailbox.pending()
	if err != nil || len(messages) != 0 {
		t.Fatalf("consumed message replayed: %v %v", messages, err)
	}
	forged := *env
	forged.SenderUrn = "urn:agent-comm:agent:mallory"
	if err := ds.receiveMessage(&forged, payload); !errors.Is(err, errMessageConflict) {
		t.Fatalf("collision accepted: %v", err)
	}
}

func TestSSEReplaysPendingOnEachConnection(t *testing.T) {
	ds := testDaemon(t, filepath.Join(t.TempDir(), "mailbox.db"))
	if err := ds.mailbox.receive(InboxMessage{MessageID: "replay-1", SenderURN: "alice", MessageFields: MessageFields{Text: "hello"}}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(ds)
	defer srv.Close()
	for i := 0; i < 2; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/v1/mq/subscribe", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		reader := bufio.NewReader(resp.Body)
		found := false
		for j := 0; j < 10; j++ {
			line, err := reader.ReadString('\n')
			if err != nil {
				break
			}
			if strings.Contains(line, `"message_id":"replay-1"`) {
				found = true
				break
			}
		}
		resp.Body.Close()
		cancel()
		if !found {
			t.Fatal("pending message was not replayed")
		}
	}
}

type retryTransport struct {
	prepared int
	sent     [][]byte
	fail     bool
	mail     *mailbox
	t        *testing.T
}

func (f *retryTransport) PrepareMessage(_ context.Context, recipient, plaintext, id string) (*pb.EncryptedEnvelope, error) {
	f.prepared++
	return &pb.EncryptedEnvelope{MessageId: id, SenderUrn: "alice", Ciphertext: []byte(plaintext), Nonce: []byte{byte(f.prepared)}}, nil
}

func (f *retryTransport) DeliverEnvelope(_ context.Context, env *pb.EncryptedEnvelope) error {
	data, _ := goproto.Marshal(env)
	var persisted []byte
	if err := f.mail.db.QueryRow(`SELECT envelope FROM helper_outbox WHERE message_id=?`, env.MessageId).Scan(&persisted); err != nil || !bytes.Equal(data, persisted) {
		f.t.Fatalf("transport called before ciphertext commit: %v", err)
	}
	f.sent = append(f.sent, data)
	if f.fail {
		return errors.New("response lost after platform stored message")
	}
	return nil
}

func TestOutboxRetryUsesPersistedEnvelopeAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mailbox.db")
	ds := testDaemon(t, path)
	body := `{"recipient_urn":"urn:agent-comm:agent:bob","message_id":"retry-1","text":"hello","conversation_id":"c"}`
	var request StoreRequest
	if err := json.Unmarshal([]byte(body), &request); err != nil {
		t.Fatal(err)
	}
	if _, err := ds.mailbox.accept(request); err != nil {
		t.Fatal(err)
	}
	f := &retryTransport{fail: true, mail: ds.mailbox, t: t}
	ds.transport = f
	if _, err := ds.deliverNext(context.Background()); err == nil {
		t.Fatal("expected transient failure")
	}
	if err := ds.mailbox.db.Close(); err != nil {
		t.Fatal(err)
	}
	ds = testDaemon(t, path)
	f.mail = ds.mailbox
	f.fail = false
	ds.transport = f
	if _, err := ds.mailbox.db.Exec(`UPDATE helper_outbox SET next_attempt=0`); err != nil {
		t.Fatal(err)
	}
	if _, err := ds.deliverNext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.prepared != 1 || len(f.sent) != 2 || !bytes.Equal(f.sent[0], f.sent[1]) {
		t.Fatal("retry did not reuse exact envelope")
	}
	w := localRequest(ds, "GET", "/api/v1/mq/status?message_id=retry-1", "")
	if !strings.Contains(w.Body.String(), `"status":"platform_queued"`) {
		t.Fatal(w.Body.String())
	}
	if status, err := ds.mailbox.accept(request); err != nil || status != "platform_queued" {
		t.Fatal("same legacy request did not preserve status", status, err)
	}
	request.Text = "changed"
	if _, err := ds.mailbox.accept(request); !errors.Is(err, errMessageConflict) {
		t.Fatal("reused id changed content", err)
	}
}

func TestExpiredOutboxAndInvalidAPIRequests(t *testing.T) {
	ds := testDaemon(t, filepath.Join(t.TempDir(), "mailbox.db"))
	if _, err := ds.mailbox.accept(StoreRequest{MessageID: "expired-1", RecipientURN: "urn:agent-comm:agent:bob", MessageFields: MessageFields{Text: "hello", Deadline: "2000-01-01T00:00:00Z"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := ds.deliverNext(context.Background()); err != nil {
		t.Fatal(err)
	}
	w := localRequest(ds, "GET", "/api/v1/mq/status?message_id=expired-1", "")
	if !strings.Contains(w.Body.String(), `"status":"expired"`) {
		t.Fatal(w.Body.String())
	}
	for _, path := range []string{"retrieve", "subscribe", "status"} {
		if got := localRequest(ds, "POST", "/api/v1/mq/"+path, "").Code; got != 405 {
			t.Fatalf("%s=%d", path, got)
		}
	}
	for _, body := range []string{
		`{"recipient_urn":"urn:agent-comm:agent:bob","message_id":"bad\nid","text":"x"}`,
		`{"recipient_urn":"urn:agent-comm:agent:bob","text":"x","hop_limit":-1}`,
		`{"recipient_urn":"urn:agent-comm:agent:bob","text":"x","deadline":"tomorrow"}`,
	} {
		if got := localRequest(ds, "POST", "/api/v1/mq/store", body).Code; got != 400 {
			t.Fatal(got, body)
		}
	}
	r := httptest.NewRequest("POST", "http://localhost/api/v1/mq/ack", strings.NewReader(`{"message_ids":["expired-1"]}`))
	r.Header.Set("Origin", "https://untrusted.example")
	w = httptest.NewRecorder()
	ds.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("cross origin API allowed")
	}
	response := localRequest(ds, "GET", "/api/v1/mq/retrieve", "")
	var decoded map[string]json.RawMessage
	if err := json.NewDecoder(io.NopCloser(response.Body)).Decode(&decoded); err != nil {
		t.Fatal(err)
	}
}
