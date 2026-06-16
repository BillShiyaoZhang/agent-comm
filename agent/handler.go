package agent

import (
	"context"
	"encoding/binary"
	"fmt"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/BillShiyaoZhang/agent-comm/dr"
	"github.com/BillShiyaoZhang/agent-comm/session"
	goproto "google.golang.org/protobuf/proto"
	pb "github.com/BillShiyaoZhang/agent-comm/proto"
)

// StartListening registers libp2p network stream handlers and spins up background
// routines to actively drain offline messages from the platform MQ.
func (a *Agent) StartListening(ctx context.Context, handler func(urn string, msg string)) {

	// 1. Listen for realtime peer connections (ECIES / DR Streams)
	a.Host.SetStreamHandler(session.ProtoID, func(stream network.Stream) {
		defer stream.Close()

		sizeBuf := make([]byte, 4)
		if _, err := stream.Read(sizeBuf); err != nil {
			return
		}
		size := binary.BigEndian.Uint32(sizeBuf)
		envBytes := make([]byte, size)
		if _, err := stream.Read(envBytes); err != nil {
			return
		}

		var env pb.EncryptedEnvelope
		if err := goproto.Unmarshal(envBytes, &env); err != nil {
			return
		}

		plaintext, err := a.Session.DecryptEnvelope(&env)
		if err == nil {
			handler(env.SenderUrn, string(plaintext))
		}
	})

	// 1b. Listen for direct Double Ratchet streams
	a.Host.SetStreamHandler(dr.ProtoID, func(stream network.Stream) {
		defer stream.Close()

		senderPeerID := stream.Conn().RemotePeer()

		// Look up contact to get their URN and cache their static X25519 PK for the responder session
		var senderURN string
		contact, err := a.Contacts.GetByPeerID(senderPeerID.String())
		if err == nil {
			senderURN = contact.URN
			a.Session.SetPeerX25519PK(senderPeerID, contact.X25519PK)
		} else {
			// Fallback: use PeerID as URN if not in contacts (though Double Ratchet requires contacts)
			senderURN = "urn:agent-comm:peer:" + senderPeerID.String()
		}

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
			}
			handler(senderURN, string(plaintext))
		} else {
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
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	urn := a.Keys.Ed25519.URN()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Poll all known platform bootstrap nodes
			for _, node := range a.BootstrapNodes {
				envs, err := a.MQClient.Retrieve(ctx, node, urn)
				if err != nil || len(envs) == 0 {
					continue
				}

				fmt.Printf("[Agent] Synchronized %d offline messages from Platform MQ (%s)\n", len(envs), node.ID)
				for _, env := range envs {
					plaintext, err := a.Session.DecryptEnvelope(env)
					if err == nil {
						handler(env.SenderUrn, string(plaintext))
						// Ack destruction upon successful decryption
						_, _ = a.MQClient.Ack(ctx, node, []string{env.MessageId})
					}
				}
			}
		}
	}
}
