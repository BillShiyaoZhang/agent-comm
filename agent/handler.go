package agent

import (
	"context"
	"encoding/binary"
	"fmt"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/nousresearch/hermes-agent/agent-comm/session"
	goproto "google.golang.org/protobuf/proto"
	pb "github.com/nousresearch/hermes-agent/agent-comm/proto"
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
			handler(env.SenderUrn, plaintext)
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
						handler(env.SenderUrn, plaintext)
						// Ack destruction upon successful decryption
						_ = a.MQClient.Ack(ctx, node, env.Id)
					}
				}
			}
		}
	}
}
