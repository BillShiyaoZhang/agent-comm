---
name: agent-comm
description: >
  P2P encrypted agent messaging: libp2p + DHT + Double Ratchet.
  Project: ~/.hermes/agent-comm/ — Go implementation only.
  Activate when: setting up agent-to-agent comm, exchanging contacts via libp2p,
  sending DR-encrypted messages, or doing libp2p/DR development.
---

# agent-comm

## Overview

P2P encrypted messaging for AI agents. No central server for routing or storage. End-to-end encrypted with Double Ratchet (forward secrecy).

## Current Status

| Phase | Status | What |
|-------|--------|------|
| 1 | ✅ | libp2p host + Relay v2 + AutoNAT |
| 2 | ✅ | Identity + DHT registry + ECIES sessions |
| 3 | ✅ | Async MQ via relay (offline storage) |
| 4b | ✅ | Double Ratchet (forward secrecy) |
| 5 | ✅ | SQLite-backed DRSession persistence |
| 6 | ✅ | E2E DR over libp2p streams (bidirectional) |
| 4a WoT | ⚠️ | `wot/` package exists, not integrated |

**Go binary:** `~/.local/go/bin/go` (Go 1.25.10)

## Key Design

- **URN**: `urn:hermes:agent:<base58(SHA256(pubkey)[:16])>` — self-certifying
- **DR mode**: simplex (one stream per message); `io.EOF` on response read is normal
- **Peer X25519 PK**: must call `mgr.SetPeerX25519PK(peerID, pk)` before DR sessions work
- **AAD constant**: `SHA256("agent-comm-v1")[:16]` — never use PeerID/URN as AAD
- **Storage**: `dr/store.go` — SQLite per-peer `RatchetState`

## DR Double Ratchet Gotchas

**1. Both sides need DR stream handlers**
If only one node registers `/agent/dr/1.0.0`, the opposite direction fails with "protocols not supported". Each node must call `host.SetStreamHandler(dr.ProtoID, handler)`.

**2. `mgr` must be created BEFORE `SetPeerX25519PK`**
```go
mgrB := session.NewManager(hostB, keysB)
mgrB.SetPeerX25519PK(aPeerID, aX25519PK) // ✅ correct order
```

**3. Simplex EOF is normal**
```go
respSize, err := readUint32BE(stream)
if err != nil {
    if err == io.EOF {
        return nil // simplex: no reply expected
    }
    return fmt.Errorf("read response size: %w", err)
}
```

**4. DR responder needs sender's X25519 PK**
`Receive()` looks up sender's PK via `mgr.PeerStaticX25519PK(senderPeerID)`. Cache it before the DR handler fires.

## Test Commands

```bash
cd ~/.hermes/agent-comm

~/.local/go/bin/go run ./cmd/test_dr/           # DR handshake
~/.local/go/bin/go run ./cmd/test_dr_persist/    # DR persistence
~/.local/go/bin/go run ./cmd/test_dr_net/        # Bidirectional DR over libp2p
~/.local/go/bin/go run ./cmd/test_session/       # ECIES session (Phase 2)
~/.local/go/bin/go run ./cmd/test_mq/            # Offline MQ
```

## Phase 2 Session Encryption

```
sender:  ECDH(sender_SK, recipient_PK) → shared_secret
         HKDF(shared_secret, "agent-comm-ephemeral-v1") → ephemeral (32 bytes)
         HKDF(shared_secret, ephemeral) → encKey (32 bytes)
         AES-GCM(encKey, nonce, payload, AAD=ProtoAAD) → ciphertext+tag

recipient: ECDH(recipient_SK, sender_PK) → same shared_secret
           AES-GCM-Decrypt(encKey, ciphertext, nonce, tag, AAD=ProtoAAD)
```

**Envelope fields:** `sender_urn`, `sender_static_pubkey` (32B X25519), `ephemeral_pubkey` (32B), `nonce` (12B), `ciphertext`, `tag` (16B), `message_id`

## libp2p v0.36+ API Changes

- `h.ID().Pretty()` → `h.ID().String()`
- `h.Connect(ctx, peerID)` → `h.Connect(ctx, peer.AddrInfo)`
- Import `github.com/libp2p/go-libp2p/core/host` NOT `go-libp2p-core/host`
- Import `github.com/libp2p/go-libp2p/core/peerstore` NOT `go-libp2p-core/peerstore`

## Project Structure

```
agent-comm/
├── crypto/        # Ed25519/X25519 keys + ECIES
├── libp2p/        # Host construction
├── dht/           # Kad-DHT wrapper
├── registry/      # URN registry (client + server handler)
├── session/       # ECIES session manager + peerX25519PK cache
├── mq/            # Async message queue (client + relay server)
├── dr/            # Double Ratchet (ratchet + session + store)
├── wot/           # Web of Trust ⚠️ partial
├── proto/         # Protobuf definitions
└── cmd/           # Bootstrap, client, test_*
```

## Dependencies (Verified)

```
go-libp2p: v0.36.3
go-libp2p-core: v0.20.1  (use core/ subpaths)
go-libp2p-kad-dht: v0.39.2
quic-go: v0.45.2 (transitive)
Go: 1.25.10
```

## Key Files

- `dr/session.go` — `DRSession`, `NewDRSessionInitiator`, `NewDRSessionResponder`
- `dr/store.go` — `DRStore`, `SaveSession`, `LoadSession`, `DeleteSession`
- `dr/ratchet.go` — `RatchetState`, `SerializeRatchetState`, `DeserializeRatchetState`
- `session/session.go` — `Manager`, `SetPeerX25519PK`, `PeerStaticX25519PK`, `SendMessage`
- `registry/client.go` — `Resolve`, `Register`
- `SPEC.md` — full architecture specification