# agent-comm

**P2P encrypted messaging for AI agents — libp2p + Double Ratchet + DHT registry.**

```
~/.hermes/agent-comm/
```

## Architecture

```
┌──────────────────────────────────────────────────────────────┐
│                         libp2p Host                          │
│  ┌─────────┐  ┌────────────┐  ┌──────────┐  ┌───────────┐ │
│  │   DHT   │  │  Registry  │  │  Session  │  │  MQ/Relay  │ │
│  │ Kad-DHT │  │  URN→Peer  │  │  ECIES/DR │  │   Store/   │ │
│  │         │  │  + X25519  │  │          │  │  Retrieve  │ │
│  └─────────┘  └────────────┘  └──────────┘  └───────────┘ │
│                                                              │
│  ┌─────────┐  ┌────────────┐  ┌────────────────────────────┐ │
│  │ Crypto  │  │  Identity   │  │     Double Ratchet         │ │
│  │Ed25519  │  │  Keys(pem)  │  │  (forward secrecy, Phase 4b)│ │
│  │ X25519  │  └────────────┘  └────────────────────────────┘ │
│  └─────────┘                                                  │
└──────────────────────────────────────────────────────────────┘
```

## Phases

| Phase | Status | Description |
|-------|--------|-------------|
| 1 | ✅ | libp2p host + Relay v2 + AutoNAT |
| 2 | ✅ | Ed25519 identity + DHT registry + ECIES encrypted sessions |
| 3 | ✅ | Async message queue via relay (offline storage) |
| 4b | ✅ | Double Ratchet (simplex, forward secrecy) |
| 5 | ✅ | SQLite-backed DRSession persistence |
| 6 | ✅ | E2E DR over libp2p streams (bidirectional) |
| 4a | ⚠️ | WoT package scaffolded, not yet integrated |

## Test Commands

```bash
cd ~/.hermes/agent-comm

# Phase 1 — libp2p host
~/.local/go/bin/go run ./cmd/test_host/

# Phase 2 — ECIES session (bidirectional)
~/.local/go/bin/go run ./cmd/test_session/

# Phase 3 — async MQ (Relay + Sender + Receiver)
~/.local/go/bin/go run ./cmd/test_mq/

# Phase 4b — Double Ratchet handshake
~/.local/go/bin/go run ./cmd/test_dr/

# Phase 5 — DR session persistence
~/.local/go/bin/go run ./cmd/test_dr_persist/

# Phase 6 — Bidirectional DR over libp2p
~/.local/go/bin/go run ./cmd/test_dr_net/
```

**Go binary path:** `~/.local/go/bin/go` (Go 1.25.10)

## Key Design

- **URN identity**: `urn:hermes:agent:<base58(SHA256(pubkey)[:16])>` — self-certifying, stable across restarts
- **URN resolution**: `/hermes/agent-comm/registry/1.0.0` stream protocol over libp2p
- **ECIES session**: X25519 ECDH + HKDF + AES-256-GCM-SIV, AAD = `SHA256("agent-comm-v1")[:16]`
- **Double Ratchet**: simplex (one stream per message), `io.EOF` on response read is normal
- **Offline messages**: encrypted blob stored on relay (relay cannot read contents)
- **Persistence**: `dr/store.go` — SQLite per-peer `RatchetState` storage

## Project Structure

```
agent-comm/
├── crypto/          # Ed25519/X25519 keys + ECIES
├── libp2p/          # Host construction
├── dht/             # Kad-DHT wrapper
├── registry/        # URN registry client + server handler
├── session/         # ECIES session manager
├── mq/              # Async message queue (client + relay server)
├── dr/              # Double Ratchet (ratchet + session + store)
├── wot/             # Web of Trust (claim + store + resolver) ⚠️ partial
├── proto/           # Protobuf definitions
├── contacts/        # Identity keys (auto-created on first run)
└── cmd/
    ├── bootstrap/   # Bootstrap node (DHT server)
    ├── client/      # Client node
    └── test_*/      # Phase verification tests
```

## Security Properties

- Ed25519 for identity signing (self-certifying URN)
- X25519 for ECIES key exchange (separate from identity key)
- Double Ratchet for forward secrecy (compromise of one key exposes bounded window)
- Relay stores only encrypted blobs (no read access)
- One-time token in contact exchange prevents contact reuse