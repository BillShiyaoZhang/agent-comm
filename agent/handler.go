package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/BillShiyaoZhang/agent-comm/dr"
	pb "github.com/BillShiyaoZhang/agent-comm/proto"
	"github.com/BillShiyaoZhang/agent-comm/session"
	"github.com/libp2p/go-libp2p/core/network"
)

// StartListening registers libp2p network stream handlers and spins up background
// routines to actively drain offline messages from the platform MQ.
func (a *Agent) StartListening(ctx context.Context, handler func(urn string, msg string)) {

	// 1. Listen for realtime peer connections (ECIES / DR Streams)
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
		handler(env.SenderUrn, string(plaintext))
		_ = a.Session.SendReply(stream, env.SenderStaticPubkey, env.SenderUrn, "accepted")
	})

	// 1b. Listen for direct Double Ratchet streams
	a.Host.SetStreamHandler(dr.ProtoID, func(stream network.Stream) {
		defer stream.Close()

		senderPeerID := stream.Conn().RemotePeer()

		// Look up contact to get their URN and cache their static X25519 PK for the responder session
		var senderURN string
		contact, err := a.Contacts.GetByPeerID(senderPeerID.String())
		if err != nil || session.VerifyPeerURN(senderPeerID, contact.URN) != nil {
			_ = stream.Reset()
			return
		}
		senderURN = contact.URN
		a.Session.SetPeerX25519PK(senderPeerID, contact.X25519PK)

		a.drPeersMu.Lock()
		drSession, ok := a.drPeers[senderPeerID.String()]
		if !ok {
			// Try loading existing responder session from DRStore
			state, dbFound, err := a.DRStore.LoadSession(senderURN)
			if err == nil && dbFound && state != nil {
				drSession = dr.NewDRSessionFromState(a.Session, a.Keys, senderPeerID, senderURN, *state)
				fmt.Printf("[Agent] Loaded responder DR session for %s from database.\n", senderURN)
			} else {
				// Create a new responder DRSession
				drSession = dr.NewDRSessionResponder(ctx, a.Session, a.Keys, senderPeerID, senderURN)
				fmt.Printf("[Agent] Created new responder DR session for %s.\n", senderURN)
			}
			a.drPeers[senderPeerID.String()] = drSession
		}
		a.drPeersMu.Unlock()

		// Receive and decrypt
		plaintext, err := drSession.Receive(ctx, stream)
		if err == nil {
			// Save updated ratchet state after successful receive/decrypt
			updatedState := drSession.GetRatchetState()
			if err := a.DRStore.SaveSession(senderURN, senderPeerID.String(), &updatedState); err != nil {
				fmt.Printf("[Agent] Failed to save updated responder DR session for %s: %v\n", senderURN, err)
				_ = stream.Reset()
				return
			}
			handler(senderURN, string(plaintext))
		} else {
			_ = stream.Reset()
			// Discard the in-memory session if the receive/decrypt failed, so it will be reloaded
			// from the database (last known-good state) on the next attempt.
			a.drPeersMu.Lock()
			delete(a.drPeers, senderPeerID.String())
			a.drPeersMu.Unlock()
		}
	})

	// 2. Continuous background poller for MQ (Offline message hydration)
	go a.pollMQ(ctx, handler)
}

func (a *Agent) pollMQ(ctx context.Context, handler func(urn string, msg string)) {
	a.pollMQDurable(ctx, func(env *pb.EncryptedEnvelope, plaintext []byte) error {
		handler(env.SenderUrn, string(plaintext))
		return nil
	})
}
