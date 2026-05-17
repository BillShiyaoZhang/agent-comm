# agent-comm Specification

## Overview

A peer-to-peer encrypted messaging protocol for AI agents, built on libp2p. Each agent has a stable identity (URN + Ed25519 key) and can reach other agents via their URN through a DHT-based registry.

**Design principles:**
- No central server for message routing (uses DHT)
- No central server for message storage (uses relay nodes)
- Messages are end-to-end encrypted (ECIES/X25519)
- Relay nodes store encrypted blobs they cannot read
- Agents poll relay nodes to retrieve offline messages

---

## Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                         libp2p Host                             │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌─────────────────┐  │
│  │   DHT    │  │ Registry  │  │ Session  │  │  MQ (relay)     │  │
│  │ Kad-DHT  │  │ URN→Peer  │  │ ECIES    │  │  Store/Retrieve │  │
│  └──────────┘  └──────────┘  └──────────┘  └─────────────────┘  │
│                                                                   │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌─────────────────┐  │
│  │  Crypto  │  │  Identity │  │  Proto   │  │  Session        │  │
│  │ Ed25519  │  │  Keys     │  │  .proto  │  │  Manager        │  │
│  │ X25519   │  │  (files)  │  │          │  │                 │  │
│  └──────────┘  └──────────┘  └──────────┘  └─────────────────┘  │
└─────────────────────────────────────────────────────────────────┘
```

---

## Phase 1 — Transport Layer

**Done.** libp2p v0.36+ with:
- TCP + QUIC listen
- Relay v2 (connection relay for NAT traversal)
- AutoNAT
- gossipsub (future use)

---

## Phase 2 — Identity and Encrypted Sessions

### Identity

Each node has an `IdentityKeys` persisted to disk:
- **Ed25519**: used for `libp2p.Identity()` — stable PeerID, URN derivation
- **X25519**: used for ECIES encryption — separate from Ed25519 for forward secrecy separation

URN format: `urn:hermes:agent:<base58(random16bytes)>`

URN is derived from Ed25519 public key, so it's self-certifying.

### Registry

URN → PeerID/addrs/X25519PubKey resolution via libp2p streams.

**Protocol:** `/hermes/agent-comm/registry/1.0.0`

```
Client                              Server
   │── URNRegistryRequest(register) ──→  │
   │←─ URNRegistryResponse(ok) ─────────  │

Client                              Server
   │── URNRegistryRequest(resolve) ──→   │
   │←─ URNRegistryResponse(found) ───────  │
```

### Encrypted Session

**Protocol:** `/hermes/agent-comm/session/1.0.0`

Stream-based request/response using `EncryptedEnvelope`:

```
Sender                                  Recipient
  │─ ECDH(sender_SK, recipient_PK) ──────→│
  │─ HKDF → ephemeral + encKey              │
  │─ AES-GCM(plaintext, AAD=ProtoAAD)      │
  │─ EncryptedEnvelope{wire} ─────────────→│
  │                                        │─ ECDH(recipient_SK, sender_PK)
  │                                        │─ re-derive ephemeral + encKey
  │                                        │─ AES-GCM decrypt
  │←─ EncryptedEnvelope{reply} ←────────── │
  │─ decrypt reply                         │
```

**Envelope fields:**
- `sender_urn` — sender's URN
- `sender_static_pubkey` — X25519 static public key (for ECDH reply)
- `ephemeral_pubkey` — HKDF-derived 32-byte value (deterministic from shared secret)
- `nonce` — random 12-byte AES-GCM nonce
- `ciphertext` — encrypted payload
- `tag` — GCM auth tag (16 bytes)
- `message_id` — unique ID for deduplication

**AAD:** `SHA256("agent-comm-v1")` — protocol constant, same for both directions.

---

## Phase 3 — Async Message Queue

### Problem

When recipient is offline, message is lost. Need persistent offline storage.

### Design

A **relay node** (bootstrap) acts as a mailbox: it stores encrypted messages for offline recipients. Relay cannot read message contents (encrypted blob).

### Architecture

```
Sender ──→ Relay (store) ──→ Recipient (online later)
                    └── SQLite: urn → [encrypted_envelope, msg_id, expiry]

Recipient ──→ Relay (pull) ──→ Retrieve pending messages
Recipient ──→ Relay (ack)  ──→ Delete read messages
```

### Protocol: `/hermes/agent-comm/mq/1.0.0`

**Messages (protobuf, same framing as registry):**

```proto
message MQRequest {
  oneof op {
    StoreRequest store = 1;
    RetrieveRequest retrieve = 2;
    AckRequest ack = 3;
  }
}

message StoreRequest {
  string recipient_urn = 1;
  bytes encrypted_payload = 2;  // EncryptedEnvelope bytes
  int64 expiry_unix = 3;         // TTL, relay deletes after
}

message RetrieveRequest {
  string recipient_urn = 1;
}

message AckRequest {
  repeated string message_ids = 1;
}

message MQResponse {
  oneof op {
    StoreResponse store = 1;
    RetrieveResponse retrieve = 2;
    AckResponse ack = 3;
    ErrorResponse error = 4;
  }
}

message StoreResponse {
  bool ok = 1;
  string message_id = 2;
}

message RetrieveResponse {
  repeated EncryptedEnvelope payloads = 1;  // raw envelope bytes
}

message AckResponse {
  bool ok = 1;
  int32 deleted_count = 2;
}

message ErrorResponse {
  string message = 1;
}
```

### Storage (Relay Side)

SQLite table:
```sql
CREATE TABLE messages (
  id          TEXT PRIMARY KEY,
  recipient   TEXT NOT NULL,          -- URN
  payload     BLOB NOT NULL,           -- EncryptedEnvelope bytes
  expiry      INTEGER NOT NULL,         -- Unix timestamp
  stored_at   INTEGER NOT NULL         -- Unix timestamp
);
CREATE INDEX idx_recipient ON messages(recipient);
CREATE INDEX idx_expiry ON messages(expiry);
```

Relay deletes expired messages on startup and periodically (every 5 min).

### Client Behavior

**Sending a message:**
1. Resolve recipient's PeerID via Registry
2. Attempt direct session (`SendMessage`)
3. If recipient online → done
4. If recipient offline / connection failed → try MQ store:
   a. Find relay(s) — use bootstrap node as default relay
   b. `MQStoreRequest` with encrypted payload
   c. Relay returns `message_id`

**Receiving messages:**
1. On startup: `MQRetrieveRequest` to pull all pending messages
2. Decrypt each message normally via Session
3. After processing: `MQAckRequest` to delete from relay

### Relay Discovery

Each node can specify a relay URN. If not set, defaults to the bootstrap node's URN (registered in the DHT bootstrap process).

The relay's registry entry is reused: the same node that handles URN registry also handles MQ.

---

## Phase 5 — Storage Layer (DRSession Persistence)

**Done.** `dr/store.go` — SQLite-backed persistent DR session store.

### Problem

Double Ratchet is stateful. Each peer must persist `RatchetState` (root key, chain keys, DH key pairs, message numbers) to survive restarts. Without persistence, every restart resets the ratchet and breaks forward secrecy.

### Implementation

- `dr/store.go` — `DRStore` struct with `SaveSession`, `LoadSession`, `DeleteSession`
- `dr/ratchet.go` — `SerializeRatchetState()` / `DeserializeRatchetState()` for unexported field handling
- `cmd/test_dr_persist/main.go` — persistence test: create session → send/receive → simulate restart → recover → continue

### Storage Schema

```sql
CREATE TABLE dr_sessions (
    peer_urn    TEXT PRIMARY KEY,   -- peer's URN
    state       BLOB NOT NULL,      -- serialized RatchetState
    updated_at  INTEGER NOT NULL    -- Unix timestamp
);
```

### Test

```bash
go run ./cmd/test_dr_persist/
```

---

## Phase 6 — Network Transport (E2E DR over libp2p)

**Done.** `cmd/test_dr_net/main.go` — two-node bidirectional DR message exchange over real libp2p.

### What works

- **B → A**: B (initiator) opens DR stream to A, A's responder handler receives and decrypts
- **A → B**: A (initiator) opens DR stream to B, B's responder handler receives and decrypts
- Both directions use independent DRSession initiator sessions
- Simplex mode: each message is a new stream; EOF on read side is acceptable
- X25519 PK caching: `session.Manager.SetPeerX25519PK()` + `mgr.PeerStaticX25519PK()` for in-band peer key lookup

### Architecture

```
B (initiator)                         A (responder)
  │ ── OpenStream(/agent/dr/1.0.0) ──→ │
  │ ── DR encrypted message ────────────→ │
  │                                      │ Receive() decrypts using responder ratchet
  │ ←── EOF (simplex, no reply) ─────── │
  │                                      │
A (initiator)                         B (responder)
  │ ── OpenStream(/agent/dr/1.0.0) ──→ │
  │ ── DR encrypted message ────────────→ │
  │                                      │ Receive() decrypts using responder ratchet
  │ ←── EOF (simplex, no reply) ─────── │
```

### Key Design Decisions

- **Responder ratchet initialization**: `NewDRSessionResponder` creates an empty ratchet; first incoming message's header is used to initialize it (X3DH-style agreement embedded in DR header)
- **Peer X25519 PK lookup**: `session.Manager` has a local `peerX25519PK map[peer.ID][]byte` cache. Callers must `SetPeerX25519PK()` before DR sessions can look up a peer's static key during `Receive()`
- **Simplex streams**: `DRSession.Send()` reads response size with `io.EOF` tolerance — in simplex mode the peer closes the stream after sending
- **Protocol IDs**: ECIES session uses `/hermes/agent-comm/session/1.0.0`, DR uses `/agent/dr/1.0.0` (separate协议协商)

### Test

```bash
go run ./cmd/test_dr_net/
```

Expected output:
```
--- B sends 'Message 1 from B' to A ---
[OK] B: Message 1 sent

--- A creates DRSession(initiator) -> B ---
[OK] Bidirectional DR test passed

=== ALL TESTS PASSED ===
```

---

## Future Phases (Not Implemented)

### Phase 4 — Web of Trust

Contact list with trust levels, introduction certificates, and path-based trust resolution.

**Current state**: `wot/` package exists and compiles but is not integrated into the main client. `wot/store.go`, `wot/claim.go`, `wot/resolver.go` provide the core logic. A stream handler on `/hermes/agent-comm/wot/1.0.0` and network test (`cmd/test_wot/`) remain to be built.

## Phase 3 — Async Message Queue (Offline Support)

**Done.** Relay-based offline message storage and retrieval.

### Problem

Agent A is offline. Agent B sends a message. Without MQ, the message fails or is discarded. With MQ, B stores an encrypted blob on a relay node; A retrieves it when online.

### Architecture

```
B (sender) ──→ [Direct session to A?] ──→ SUCCESS (A online)
                                 └─→ [Store via relay] ──→ SUCCESS (relay stores blob)

A (receiver) ──→ [Pull from relay] ──→ Decrypt ──→ Read
```

### Protocol (proto/mq.proto)

```
MQRequest = StoreRequest | RetrieveRequest | AckRequest
MQResponse = StoreResponse | RetrieveResponse | AckResponse

StoreRequest:
  recipient_urn: string        # who this message is for
  payload: bytes               # EncryptedEnvelope bytes (relay can't read)
  ttl_seconds: int64           # expiry

RetrieveRequest:
  recipient_urn: string       # client's own URN

RetrieveResponse:
  envelopes: [EncryptedEnvelope]

AckRequest:
  message_ids: [string]      # delete after successful read

StoreResponse:
  message_id: string          # assigned by relay for ack

AckResponse:
  deleted_count: int32
```

### Relay Server (mq/server.go)

- SQLite-backed (`store/relay/mq.db`)
- Per-recipientURN index on `recipient_urn`
- Auto-deletes expired messages on retrieve and on startup
- Message IDs: SHA256(envelope+SENDER_URN+timestamp)[:16] hex
- No read access to envelope content — only stores/deletes/returns blobs

### Client (mq/client.go)

- `Store(ctx, relay, recipientURN, envelope, ttl)` — send to relay
- `Retrieve(ctx, relay, myURN)` — pull all pending messages
- `Ack(ctx, relay, messageIDs)` — delete after read
- On startup: pull → decrypt → display → ack

### End-to-End Flow

1. **B → A (A online):** Direct session via libp2p stream, ECIES encrypted, reply encrypted
2. **B → A (A offline):** B builds `EncryptedEnvelope`, calls `MQ.Store`, relay stores blob
3. **A comes online:** A calls `MQ.Retrieve`, decrypts each envelope, acks deletion

### Test

```bash
go run ./cmd/test_mq/    # Three-node test: Relay + Sender + Receiver (offline then online)
```

---

## Phase 4 — WoT (Web of Trust) + Double Ratchet

### 4a — Web of Trust

**Problem:** When Bob sends a message to Alice for the first time, how does Bob know `urn:hermes:agent:Alice` actually belongs to the real Alice and not an imposter? The registry only maps URN→PeerID→pubkey, it does not authenticate identity.

**Solution:** Signed trust claims. If Charlie says "I trust urn:hermes:agent:Alice (key=X, peer=12D3...)", and Bob already trusts Charlie, then Bob can derive transitive trust for Alice.

**Trust Claim (wot.proto):**

```proto
message TrustClaim {
  string issuer_urn          // Charlie (who made the claim)
  string subject_urn          // Alice (who is being claimed about)
  string subject_peer_id      // Alice's libp2p PeerID
  bytes  subject_x25519_pk    // Alice's X25519 static pubkey
  TrustLevel level            // TRUSTED / UNTRUSTED / UNKNOWN
  bytes  issuer_signature     // Ed25519 signature over SHA256(issuer||subject||peer||pk||level)
  int64  issued_at_unix
}
```

**Trust Levels:**
- `TRUSTED` — issuer explicitly vouches for subject's identity
- `UNTRUSTED` — issuer explicitly distrusts (revocation use case)
- `UNKNOWN` — neutral statement (awareness without judgment)

**Trust Path Resolution (BFS):**
- Local store: `wot/store.go` — SQLite of all known claims (from self or fetched from network)
- Resolver: BFS from `myURN` → any node that issued a `TRUSTED` claim about target
- Path found → verify signatures chain → accept pubkey
- No path → warn: "Untrusted peer, proceed manually?"

**Fetching claims from network:**
- New stream handler on `/hermes/agent-comm/wot/1.0.0`
- `WOTQuery`: given a target URN, return all claims about it known by the peer
- Peers gossip claims lazily (only fetch when needed for trust resolution)

**Bootstrap trust:** First-run setup requires manual bootstrap trust — user inputs a known URN they want to trust directly. After that, WoT network grows via transitive claims.

---

### 4b — Double Ratchet

**Problem:** If Bob's static key is compromised tomorrow, all past messages between Bob and Alice can be decrypted (static-static ECDH has no forward secrecy).

**Solution:** Double Ratchet (Signal Protocol). Each message uses a new ephemeral key derived from a ratchet state. Compromise of one key only exposes a bounded window of messages.

**Architecture:**

```
dr/
├── ratchet.go   # RatchetState: root key, chain keys, DH ratchet step
├── session.go   # DRSession: encrypt/decrypt with ratchet, ECIES for initial key exchange
└── store.go     # Persistent session state (per-peer URN)
```

**Key derivation chain:**

```
Initial: ECDH(our_DH_SK, their_DH_PK) → SKR
SKR → HKDF → root_key + chain_key_0

Message send (k=0):
  chain_key_0 → HKDF → message_key_0 + chain_key_1
  encrypt(message_key_0, plaintext) → ciphertext

DH Ratchet step (after receiving their new DH pubkey):
  ECDH(our_new_DH_SK, their_DH_PK) → SKR2
  SKR2 → HKDF → root_key_2 + chain_key_2
```

**Integration with existing ECIES session:**
- `session/session.go` already does ECDH + ECIES for the initial key exchange
- Replace the static-static ECDH with an initial Double Ratchet handshake
- `dr/session.go` provides `DRSendMessage` / `DRReceiveMessage` as a drop-in for `SendMessage` / stream handler
- Forward secrecy: after N messages, old chain keys are deleted

**Note:** Double Ratchet requires stateful session per peer (unlike current stateless ECIES). This adds complexity — keep current `session/session.go` as fallback for cases where DR state is not available.

---

## Project Structure

```
agent-comm/
├── crypto/
│   ├── keys.go       # Identity key loading/creation (Ed25519 + X25519)
│   └── ecies.go      # ECIES encryption/decryption
├── dht/
│   └── dht.go        # Kad-DHT wrapper
├── libp2p/
│   └── host.go       # libp2p.Host construction
├── proto/
│   ├── registry.proto
│   ├── agentcomm.proto   # legacy Envelope (not currently used)
│   ├── mq.proto          # Phase 3: async queue
│   └── *.pb.go
├── registry/
│   ├── client.go     # URN resolve/register via libp2p stream
│   └── server.go     # URN registry server handler
├── session/
│   └── session.go    # Encrypted session send/receive
├── mq/               # Phase 3: async message queue
│   ├── server.go     # Relay: store/retrieve/ack (SQLite)
│   └── client.go     # Client: store via relay, retrieve on startup
├── store/
│   └── relay/        # (legacy, see mq/server.go)
├── contacts/         # Phase 4a: contact management + trusted pubkey cache
├── wot/               # Phase 4a: web of trust (claim, store, resolver)
├── dr/                # Phase 4b: double ratchet (ratchet, session, store)
└── cmd
    ├── bootstrap/    # Bootstrap/relay node (registry + MQ server)
    ├── client/       # Regular client node
    ├── test_session/ # Two-node encrypted session test
    └── test_mq/       # Three-node offline message test
```

---

## Security Notes

- Relay stores encrypted blobs — cannot read message content
- Relay can be removed without compromising message confidentiality
- AAD = protocol constant (not peer-specific) — avoids URN↔PeerID derivation issues
- WoT (Phase 4a): trust claims are Ed25519 signed; trust path is verified before accepting a new peer's pubkey
- Double Ratchet (Phase 4b): forward secrecy — old chain keys are deleted after use
- No WoT → first-contact identity relies on registry alone (MITM possible)
- No Double Ratchet (current) → static-static ECDH; key compromise exposes all history

---

## Test Commands

```bash
# Phase 2: Two-node ECIES encrypted session
go run ./cmd/test_session/

# Phase 3: Three-node offline message test (relay + sender + receiver)
go run ./cmd/test_mq/

# Phase 5: DR session persistence (SQLite round-trip)
go run ./cmd/test_dr_persist/

# Phase 6: Two-node bidirectional DR over libp2p
go run ./cmd/test_dr_net/

# Bootstrap node (registry + MQ server)
go run ./cmd/bootstrap/

# Client node (interactive: send/pull/quit)
BOOTSTRAP_ADDR=/ip4/127.0.0.1/tcp/45041/p2p/... go run ./cmd/client/

# Phase 4a: WoT trust path test (planned)
go run ./cmd/test_wot/
```