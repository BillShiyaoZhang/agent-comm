package agent

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/BillShiyaoZhang/agent-comm/crypto"
	pb "github.com/BillShiyaoZhang/agent-comm/proto"
	"github.com/BillShiyaoZhang/agent-comm/registry"
	"github.com/libp2p/go-libp2p/core/peer"
)

// PrepareMessage resolves a verified recipient key and constructs a signed,
// immutable envelope. Persist it in an outbox before calling DeliverEnvelope.
func (a *Agent) PrepareMessage(ctx context.Context, recipientURN, plaintext, messageID string) (*pb.EncryptedEnvelope, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if recipientURN == "" || messageID == "" {
		return nil, fmt.Errorf("recipient URN and message ID are required")
	}
	if a.Contacts != nil {
		contact, err := a.Contacts.Get(recipientURN)
		if err == nil && contact.Trusted && len(contact.X25519PK) == 32 && crypto.URNMatchesPublicKey(recipientURN, contact.Ed25519PK) {
			pid, err := PeerIDFromEd25519PK(contact.Ed25519PK)
			if err == nil && pid.String() == contact.PeerID {
				return a.Session.BuildEnvelopeForRecipient(recipientURN, contact.X25519PK, plaintext, messageID)
			}
		}
	}
	if a.MQHTTPClient != nil {
		resolved, err := a.resolveHTTP(ctx, recipientURN)
		if err == nil {
			return a.Session.BuildEnvelopeForRecipient(recipientURN, resolved.X25519PubKey, plaintext, messageID)
		}
		// Keep the HTTP error when no P2P discovery is configured.
		if len(a.BootstrapNodes) == 0 {
			return nil, err
		}
	}
	type result struct {
		res registry.ResolveResult
		err error
	}
	results := make(chan result, len(a.BootstrapNodes))
	for _, node := range a.BootstrapNodes {
		go func(node peer.AddrInfo) {
			res, err := a.Registry.Resolve(node, recipientURN)
			results <- result{res, err}
		}(node)
	}
	for range a.BootstrapNodes {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case r := <-results:
			if r.err == nil && verifyRecipient(recipientURN, &r.res) == nil {
				return a.Session.BuildEnvelopeForRecipient(recipientURN, r.res.X25519PubKey, plaintext, messageID)
			}
		}
	}
	return nil, fmt.Errorf("failed to discover authenticated peer %s", recipientURN)
}

// DeliverEnvelope stores the same prepared ciphertext in the MQ on each retry.
// Success means relay acceptance; it does not mean the recipient processed it.
// Reliable delivery intentionally uses MQ so a transport EOF cannot bypass a
// recipient's durable inbox transaction.
func (a *Agent) DeliverEnvelope(ctx context.Context, env *pb.EncryptedEnvelope) error {
	if env == nil {
		return fmt.Errorf("missing envelope")
	}
	if err := crypto.VerifyEnvelope(env, env.RecipientUrn); err != nil {
		return err
	}
	if env.SenderUrn != a.Keys.Ed25519.URN() || !bytes.Equal(env.SenderEd25519Pubkey, a.Keys.Ed25519.PublicKey) {
		return fmt.Errorf("outbox envelope is not signed by this agent")
	}
	if a.MQHTTPClient != nil {
		id, err := a.MQHTTPClient.Store(ctx, env.RecipientUrn, env, 7)
		if err != nil {
			return err
		}
		if id != env.MessageId {
			return fmt.Errorf("relay returned a different message ID")
		}
		return nil
	}
	if len(a.BootstrapNodes) == 0 {
		return fmt.Errorf("no MQ transport configured")
	}
	var failures []error
	for _, node := range a.BootstrapNodes {
		id, err := a.MQClient.Store(ctx, node, env.RecipientUrn, env, 7)
		if err == nil && id == env.MessageId {
			return nil
		}
		if err == nil {
			err = fmt.Errorf("relay returned a different message ID")
		}
		failures = append(failures, err)
	}
	return errors.Join(failures...)
}

func verifyRecipient(urn string, res *registry.ResolveResult) error {
	if len(res.Signature) != ed25519.SignatureSize || !crypto.URNMatchesPublicKey(urn, res.Ed25519PubKey) || len(res.X25519PubKey) != 32 {
		return fmt.Errorf("registry did not return an authenticated recipient key")
	}
	pid, err := PeerIDFromEd25519PK(res.Ed25519PubKey)
	if err != nil || pid != res.ID {
		return fmt.Errorf("registry peer ID does not match recipient identity")
	}
	signed := registry.BuildSignedMsg(urn, res.ID.String(), res.X25519PubKey, res.StoresUserData, res.Timestamp)
	if !ed25519.Verify(res.Ed25519PubKey, signed, res.Signature) {
		return fmt.Errorf("invalid registry signature")
	}
	return nil
}

func (a *Agent) resolveHTTP(ctx context.Context, urn string) (*registry.ResolveResult, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, a.MQHTTPClient.BaseURL+"/api/v1/registry/resolve?urn="+url.QueryEscape(urn), nil)
	if err != nil {
		return nil, err
	}
	response, err := (&http.Client{Timeout: 15 * time.Second}).Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("registry resolve: HTTP %d", response.StatusCode)
	}
	var data struct {
		Found          bool   `json:"found"`
		URN            string `json:"urn"`
		PeerID         string `json:"peer_id"`
		X25519Pubkey   []byte `json:"x25519_pubkey"`
		Ed25519Pubkey  []byte `json:"ed25519_pubkey"`
		Signature      []byte `json:"signature"`
		Timestamp      int64  `json:"timestamp"`
		StoresUserData bool   `json:"stores_user_data"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&data); err != nil {
		return nil, err
	}
	if !data.Found || (data.URN != "" && data.URN != urn) {
		return nil, fmt.Errorf("recipient not found")
	}
	pid, err := peer.Decode(data.PeerID)
	if err != nil {
		return nil, err
	}
	result := &registry.ResolveResult{AddrInfo: peer.AddrInfo{ID: pid}, X25519PubKey: data.X25519Pubkey, Ed25519PubKey: data.Ed25519Pubkey, Signature: data.Signature, Timestamp: data.Timestamp, StoresUserData: data.StoresUserData}
	if err := verifyRecipient(urn, result); err != nil {
		return nil, err
	}
	return result, nil
}

func (a *Agent) registerHTTP(ctx context.Context) {
	for {
		attemptCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		err := a.registerHTTPOnce(attemptCtx)
		cancel()
		delay := 5 * time.Minute
		if err != nil {
			delay = 5 * time.Second
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (a *Agent) registerHTTPOnce(ctx context.Context) error {
	urn := a.Keys.Ed25519.URN()
	timestamp := time.Now().Unix()
	signature := ed25519.Sign(a.Keys.Ed25519.PrivateKey, registry.BuildSignedMsg(urn, a.Host.ID().String(), a.Keys.X25519PK, false, timestamp))
	var addrs []string
	for _, addr := range a.Host.Addrs() {
		addrs = append(addrs, addr.String())
	}
	payload, err := json.Marshal(struct {
		URN            string   `json:"urn"`
		PeerID         string   `json:"peer_id"`
		Addrs          []string `json:"addrs"`
		X25519Pubkey   []byte   `json:"x25519_pubkey"`
		Ed25519Pubkey  []byte   `json:"ed25519_pubkey"`
		Signature      []byte   `json:"signature"`
		Timestamp      int64    `json:"timestamp"`
		StoresUserData bool     `json:"stores_user_data"`
	}{urn, a.Host.ID().String(), addrs, a.Keys.X25519PK, a.Keys.Ed25519.PublicKey, signature, timestamp, false})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, a.MQHTTPClient.BaseURL+"/api/v1/registry/register", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Ed25519 "+hex.EncodeToString(ed25519.Sign(a.Keys.Ed25519.PrivateKey, payload))+":"+hex.EncodeToString(a.Keys.Ed25519.PublicKey))
	response, err := (&http.Client{Timeout: 15 * time.Second}).Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("registry register: HTTP %d", response.StatusCode)
	}
	var result struct {
		OK bool `json:"ok"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result); err != nil {
		return err
	}
	if !result.OK {
		return fmt.Errorf("registry registration rejected")
	}
	return nil
}
