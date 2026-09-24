package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/BillShiyaoZhang/agent-comm/dr"
	pb "github.com/BillShiyaoZhang/agent-comm/proto"
	"github.com/BillShiyaoZhang/agent-comm/session"
	"github.com/libp2p/go-libp2p/core/network"
)

// DurableHandler receives authenticated envelope metadata and decrypted
// ChatMessage protobuf bytes. Returning nil commits receipt: the caller must
// have persisted its inbox record before doing so. Errors retain MQ messages.
// Delivery is at least once; deduplicate by (SenderUrn, MessageId) in that store.
type DurableHandler func(*pb.EncryptedEnvelope, []byte) error

// StartListeningDurable polls immediately and only acknowledges messages after
// the application confirms its durable inbox transaction. Legacy DR streams
// lack stable IDs and durable receipts, so this listener resets those streams
// and makes legacy senders fall back to authenticated MQ envelopes.
// The returned channel closes after context cancellation, polling shutdown,
// and all in-flight application callbacks; wait for it before closing the inbox.
func (a *Agent) StartListeningDurable(ctx context.Context, handler DurableHandler) <-chan struct{} {
	return a.startListeningDurable(ctx, handler, true)
}

// StartListeningMQDurable disables direct v1/DR streams and polls only the
// platform MQ. A v2 compliance route must not admit unchecked direct frames.
func (a *Agent) StartListeningMQDurable(ctx context.Context, handler DurableHandler) <-chan struct{} {
	return a.startListeningDurable(ctx, handler, false)
}

func (a *Agent) startListeningDurable(ctx context.Context, handler DurableHandler, allowDirect bool) <-chan struct{} {
	done := make(chan struct{})
	if handler == nil {
		close(done)
		return done
	}
	var admission sync.Mutex
	var active sync.WaitGroup
	stopped := false
	guarded := func(env *pb.EncryptedEnvelope, plaintext []byte) error {
		admission.Lock()
		if stopped || ctx.Err() != nil {
			admission.Unlock()
			return context.Canceled
		}
		active.Add(1)
		admission.Unlock()
		defer active.Done()
		return handler(env, plaintext)
	}
	a.Host.SetStreamHandler(dr.ProtoID, func(stream network.Stream) { _ = stream.Reset() })
	if !allowDirect {
		a.Host.SetStreamHandler(session.ProtoID, func(stream network.Stream) { _ = stream.Reset() })
	} else {
		a.Host.SetStreamHandler(session.ProtoID, func(stream network.Stream) {
			defer stream.Close()
			_ = stream.SetDeadline(time.Now().Add(30 * time.Second))
			env, err := session.ReadEnvelope(stream)
			if err != nil {
				_ = stream.Reset()
				return
			}
			if err := session.VerifyPeerURN(stream.Conn().RemotePeer(), env.SenderUrn); err != nil {
				_ = stream.Reset()
				return
			}
			plaintext, err := a.Session.DecryptEnvelope(env)
			if err != nil {
				_ = stream.Reset()
				return
			}
			if err := guarded(env, plaintext); err != nil {
				_ = stream.Reset()
				return
			}
			_ = a.Session.SendReply(stream, env.SenderStaticPubkey, env.SenderUrn, "persisted")
		})
	}
	pollDone := make(chan struct{})
	go func() {
		defer close(pollDone)
		a.pollMQDurable(ctx, guarded)
	}()
	go func() {
		<-ctx.Done()
		admission.Lock()
		stopped = true
		admission.Unlock()
		<-pollDone
		active.Wait()
		close(done)
	}()
	return done
}

func (a *Agent) pollMQDurable(ctx context.Context, handler DurableHandler) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		attemptCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
		_ = a.PollMessages(attemptCtx, handler)
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// PollMessages performs one MQ pass. Verification failures and handler errors
// leave envelopes available for retry; only successful durable callbacks ACK.
func (a *Agent) PollMessages(ctx context.Context, handler DurableHandler) error {
	if handler == nil {
		return fmt.Errorf("durable handler is required")
	}
	urn := a.Keys.Ed25519.URN()
	if a.MQHTTPClient != nil {
		envelopes, err := a.MQHTTPClient.Retrieve(ctx, urn)
		if err != nil {
			return err
		}
		return a.processEnvelopes(ctx, envelopes, handler, func(ids []string) error { _, err := a.MQHTTPClient.Ack(ctx, ids); return err })
	}
	var failures []error
	for _, node := range a.BootstrapNodes {
		envelopes, err := a.MQClient.Retrieve(ctx, node, urn)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if err := a.processEnvelopes(ctx, envelopes, handler, func(ids []string) error { _, err := a.MQClient.AckForRecipient(ctx, node, urn, ids); return err }); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (a *Agent) processEnvelopes(ctx context.Context, envelopes []*pb.EncryptedEnvelope, handler DurableHandler, ack func([]string) error) error {
	var failures []error
	for _, env := range envelopes {
		if err := ctx.Err(); err != nil {
			return err
		}
		plaintext, err := a.Session.DecryptEnvelope(env)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		// Capture the authenticated ID before exposing metadata to a callback.
		id := env.MessageId
		if err := handler(env, plaintext); err != nil {
			failures = append(failures, err)
			continue
		}
		if err := ack([]string{id}); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
