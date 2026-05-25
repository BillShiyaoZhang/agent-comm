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

**Completed.** libp2p v0.36+, including:

- TCP + QUIC listeners
- Relay v2 (NAT traversal relay connection)
- AutoNAT
- gossipsub (for future extensions)

---

## Phase 2 — Identity and Encrypted Sessions

### Identity

Each node has a pair of `IdentityKeys` (persisted to disk):
- **Ed25519**: used for `libp2p.Identity()` — stable PeerID, URN derivation
- **X25519**: used for ECIES encryption — separated from Ed25519 to ensure forward secrecy

URN format: `urn:hermes:agent:<base58(random16bytes)>`

The URN is derived from the Ed25519 public key, hence it is self-certifying.

### Registry

URN → PeerID/addrs/X25519PubKey resolution via libp2p stream.

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
- `sender_urn` — sender URN
- `sender_static_pubkey` — X25519 static public key (used for ECDH reply)
- `ephemeral_pubkey` — 32-byte value derived from HKDF
- `nonce` — random 12-byte AES-GCM nonce
- `ciphertext` — encrypted payload
- `tag` — GCM authentication tag (16 bytes)
- `message_id` — unique ID for deduplication

**AAD:** `SHA256("agent-comm-v1")` — protocol constant, same for both directions.

**Implementation:** `session/session.go` — `Manager` struct

- `SendMessage()` — sends message and waits for encrypted reply
- `SendReply()` — sends encrypted reply on existing stream
- `BuildEnvelope()` — builds encrypted envelope (for MQ storage)
- `DecryptEnvelope()` — decrypts received envelope

---

## Phase 3 — Async Message Queue

### Problem

Messages are lost when the recipient is offline, requiring persistent offline storage.

### Design

**Relay nodes** (bootstrap nodes) act as mailboxes: storing encrypted messages for offline recipients. Relays cannot read the message contents (encrypted blob).

### Architecture

```
Sender ──→ Relay (store) ──→ Recipient (online later)
                    └── SQLite: urn → [encrypted_envelope, msg_id, expiry]

Recipient ──→ Relay (pull) ──→ Retrieve pending messages
Recipient ──→ Relay (ack)  ──→ Delete read messages
```

### Protocol: `/hermes/agent-comm/mq/1.0.0`

**Proto File:** `proto/mq.proto` (imports `proto/envelope.proto` for `EncryptedEnvelope`)

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
  EncryptedEnvelope payload = 2;  // encrypted message blob (unreadable by relay)
  int64 expiry_unix = 3;          // TTL: relay deletes after this timestamp (0 = never expires)
}

message RetrieveRequest {
  string recipient_urn = 1;
}

message AckRequest {
  repeated string message_ids = 1;  // client deletes after processing
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
  string message_id = 2;  // assigned by relay
}

message RetrieveResponse {
  repeated EncryptedEnvelope payloads = 1;  // chronological order
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

**Implementation:** `mq/server.go` — `Server` struct with SQLite persistence

```sql
CREATE TABLE messages (
  id         TEXT PRIMARY KEY,
  recipient  TEXT NOT NULL,          -- URN
  payload    BLOB NOT NULL,           -- EncryptedEnvelope bytes
  expiry     INTEGER NOT NULL,        -- Unix timestamp
  stored_at  INTEGER NOT NULL         -- Unix timestamp
);
CREATE INDEX idx_recipient ON messages(recipient);
CREATE INDEX idx_expiry ON messages(expiry);
```

Relays periodic delete expired messages at startup and every 5 minutes.

### Client Behavior

**Implementation:** `mq/client.go` — `Client` struct

- `Store(ctx, relay, recipientURN, envelope, ttlDays)` — sends to relay
- `Retrieve(ctx, relay, myURN)` — pulls all pending messages
- `Ack(ctx, relay, messageIDs)` — deletes after reading

**Sending Flow:**
1. Resolve recipient PeerID via Registry
2. Attempt direct session (`SendMessage`)
3. Recipient online → Completed
4. Recipient offline / connection failed → Attempt MQ store:
   a. Find relay (defaults to bootstrap node URN resolved during bootstrap)
   b. `MQStoreRequest` + encrypted payload
   c. Relay returns `message_id`

**Receiving Flow:**
1. At startup: `MQRetrieveRequest` pulls all pending messages
2. Decrypt each message (normally decrypted via Session)
3. Upon successful processing: `MQAckRequest` deletes from relay

### Relay Discovery

Each node can specify a relay URN. If not set, it defaults to the bootstrap node's URN (registered during DHT bootstrap).

The same node handles both URN registry and MQ.

### Test

```bash
go run ./cmd/test_mq/    # Three-node test: Relay + Sender + Receiver (offline then online)
```

---

## Phase 4a — Web of Trust

### Problem

When Bob sends a message to Alice for the first time, how does Bob verify that `urn:hermes:agent:Alice` belongs to the real Alice and not an impostor? The Registry only provides URN→PeerID→pubkey mapping, without identity authentication.

### Solution

Signed trust claims. If Charlie says "I trust `urn:hermes:agent:Alice` (key=X, peer=12D3...)", and Bob already trusts Charlie, Bob can infer transitive trust for Alice.

### Trust Claim

**Proto File:** `proto/wot.proto`

```proto
enum TrustLevel {
  UNKNOWN = 0;
  TRUSTED = 1;
  UNTRUSTED = 2;
}

message TrustClaim {
  string issuer_urn = 1;          // issuer (e.g., "urn:hermes:agent:Charlie")
  string subject_urn = 2;          // subject (e.g., "urn:hermes:agent:Alice")
  string subject_peer_id = 3;      // Subject's libp2p PeerID
  bytes  subject_x25519_pk = 4;   // Subject's X25519 static public key (32 bytes)
  TrustLevel level = 5;            // TRUSTED / UNTRUSTED / UNKNOWN
  bytes  issuer_signature = 6;     // Ed25519 signature
  int64  issued_at_unix = 7;       // Unix timestamp
}
```

**Trust Levels:**
- `TRUSTED` — issuer explicitly vouches for the subject's identity
- `UNTRUSTED` — issuer explicitly does not trust (revocation use case)
- `UNKNOWN` — neutral statement (aware of, but no judgment)

**Signature Content:** `SHA256(issuer_urn || subject_urn || subject_peer_id || subject_x25519_pk || level || issued_at)`

### Implementation

- `wot/claim.go` — `TrustClaim` struct, `NewTrustClaim()` creates claim, `Verify()` validates signature
- `wot/store.go` — `Store` struct, SQLite persistence for all known claims (local + fetched from network)
- `wot/resolver.go` — `Resolver` struct, BFS trust path search

**Store Schema:**
```sql
CREATE TABLE claims (
  id          TEXT PRIMARY KEY,      -- SHA256(issuer||subject||issued_at)[:16]
  issuer_urn  TEXT NOT NULL,
  subject_urn TEXT NOT NULL,
  peer_id     TEXT NOT NULL,
  x25519_pk   BLOB NOT NULL,
  level       INTEGER NOT NULL,
  signature   BLOB NOT NULL,
  issued_at   INTEGER NOT NULL,
  fetched_at  INTEGER NOT NULL
);
CREATE INDEX idx_subject ON claims(subject_urn);
CREATE INDEX idx_issuer  ON claims(issuer_urn);

CREATE TABLE known_peers (
  urn         TEXT PRIMARY KEY,
  peer_id     TEXT NOT NULL,
  x25519_pk   BLOB NOT NULL,
  ed25519_pk  BLOB NOT NULL,
  first_seen  INTEGER NOT NULL,
  last_seen   INTEGER NOT NULL
);
```

**Trust Path Resolution:**
- BFS starts from `myURN`, searching for any node that issued a `TRUSTED` claim about the target URN
- Path found → verify signature chain → accept pubkey
- No path → warning: "Untrusted peer, proceed manually?"

**Network Query:** `Resolver.FetchClaimsAbout(ctx, subjectURN)` — queries remote nodes for all claims about a URN via `/hermes/agent-comm/wot/1.0.0` protocol

**Bootstrap trust:** First run requires manually entering a known URN as a directly trusted target. Afterward, the WoT network grows via transitive claims.

---

## Phase 4b — Double Ratchet

### Problem

If Bob's static key is compromised tomorrow, all past messages with Alice will be decrypted (static-static ECDH lacks forward secrecy).

### Solution

Double Ratchet (Signal Protocol). Each message uses a new ephemeral key derived from the ratchet state. Compromising one key only exposes a limited window of messages.

### Architecture

```
dr/
├── ratchet.go   # RatchetState: root key, chain keys, DH ratchet step
├── session.go   # DRSession: encrypt/decrypt with ratchet, ECIES for initial key exchange
└── store.go     # Persistent session state (per-peer URN, SQLite)
```

### Key Derivation Chain

```
Initial: ECDH(our_DH_SK, their_DH_PK) → SKR
SKR → HKDF → root_key + chain_key_0

Sending message (k=0):
  chain_key_0 → HKDF → message_key_0 + chain_key_1
  encrypt(message_key_0, plaintext) → ciphertext

DH Ratchet step (after receiving partner's new DH public key):
  ECDH(our_new_DH_SK, their_DH_PK) → SKR2
  SKR2 → HKDF → root_key_2 + chain_key_2
```

### Integration with ECIES Session

- `session/session.go` already implements ECDH + ECIES initial key exchange
- Double Ratchet replaces static-static ECDH with initial ECDH handshake
- `dr/session.go` provides `DRSendMessage` / `DRReceiveMessage` as replacements for `SendMessage` / stream handlers
- Forward secrecy: old chain keys are deleted after N messages

**Note:** Double Ratchet requires stateful sessions per peer (different from the current stateless ECIES). Complexity increases — keep current `session/session.go` as a fallback when DR state is unavailable.

---

## Phase 5 — Storage Layer (DRSession Persistence)

**Completed.** `dr/store.go` — SQLite persistence for DR session storage.

### Problem

Double Ratchet is stateful. Each peer must persist `RatchetState` (root key, chain keys, DH key pairs, message numbers) to survive restarts. Without persistence, restarting resets the ratchet, breaking forward secrecy.

### Storage Schema

```sql
CREATE TABLE dr_sessions (
    peer_urn    TEXT PRIMARY KEY,   -- Peer URN
    state       BLOB NOT NULL,      -- serialized RatchetState
    updated_at  INTEGER NOT NULL    -- Unix timestamp
);
```

### Test

```bash
go run ./cmd/test_dr_persist/
```

Test flow: create session → send/receive → simulate restart → recover → continue communication.

---

## Phase 6 — Network Transport (E2E DR over libp2p)

**Completed.** `cmd/test_dr_net/main.go` — two-node bidirectional DR message exchange over real libp2p.

### What Works

- **B → A**: B (initiator) opens DR stream to A, A's responder handler receives and decrypts
- **A → B**: A (initiator) opens DR stream to B, B's responder handler receives and decrypts
- Bidirectional communication uses independent initiator DRSession instances
- Simplex mode: one stream per message; read EOF is tolerated
- X25519 PK Cache: `session.Manager.SetPeerX25519PK()` + `mgr.PeerStaticX25519PK()` used for in-band peer key lookup

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

- **Responder ratchet initialization**: `NewDRSessionResponder` creates an empty ratchet; the header of the first inbound message is used for initialization (X3DH-style agreement embedded in the DR header)
- **Peer X25519 PK Lookup**: `session.Manager` has a local `peerX25519PK map[peer.ID][]byte` cache. Callers must invoke `SetPeerX25519PK()` before calling `Receive()` to cache the peer's static key.
- **Simplex streams**: `DRSession.Send()` tolerates `io.EOF` when reading responses — in simplex mode, the peer closes the stream after sending.
- **Protocol IDs**: ECIES session uses `/hermes/agent-comm/session/1.0.0`, DR uses `/agent/dr/1.0.0` (independent protocol negotiations).

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

## Project Structure

```
agent-comm/
├── crypto/
│   ├── keys.go       # Identity key load/creation (Ed25519 + X25519)
│   └── ecies.go      # ECIES encryption/decryption
├── dht/
│   └── dht.go        # Kad-DHT encapsulation
├── libp2p/
│   └── host.go       # libp2p.Host construction
├── proto/
│   ├── registry.proto
│   ├── envelope.proto    # EncryptedEnvelope, ChatMessage
│   ├── mq.proto          # Phase 3: async queue
│   ├── wot.proto         # Phase 4a: trust claims
│   └── *.pb.go
├── registry/
│   ├── client.go     # URN resolve/register via libp2p stream
│   └── server.go     # URN registry server handler
├── session/
│   └── session.go    # Encrypted session send/receive (Phase 2)
├── mq/               # Phase 3: async message queue
│   ├── server.go     # Relay: store/retrieve/ack (SQLite)
│   └── client.go     # Client: store via relay, retrieve on startup
├── contacts/         # Phase 4a: contact management + trusted pubkey cache
├── wot/              # Phase 4a: web of trust
│   ├── claim.go      # TrustClaim creation and verification
│   ├── store.go      # SQLite persistence
│   └── resolver.go   # BFS trust path resolution
├── dr/               # Phase 4b: double ratchet
│   ├── ratchet.go    # RatchetState: root/chain keys, DH step
│   ├── session.go    # DRSession: encrypt/decrypt
│   └── store.go      # DRSession persistence (SQLite)
├── store/
│   └── relay/        # (empty, relay functionality implemented by mq/server.go)
├── cmd/
│   ├── bootstrap/    # Bootstrap/relay node (registry + MQ server)
│   ├── client/       # Regular client node
│   ├── test_session/ # Phase 2: two-node ECIES encrypted session test
│   ├── test_mq/      # Phase 3: three-node offline message test (relay + sender + receiver)
│   ├── test_dr_persist/  # Phase 5: DR session persistence test
│   ├── test_dr_net/  # Phase 6: two-node libp2p bidirectional DR test
│   ├── test_hkdf_check/
│   ├── test_kdf/
│   ├── test_debug/
│   └── debug_dh/
└── docs/
    ├── TUTORIAL.md
    └── DR-CODE-COMMENTARY.md
```

---

## Security Notes

- Relay stores encrypted blobs — cannot read message contents
- Relays can be removed without affecting message confidentiality
- AAD = protocol constant (not peer-specific) — avoids URN↔PeerID derivation issues
- WoT (Phase 4a): trust claims signed via Ed25519; verify trust path before accepting new peer pubkeys
- Double Ratchet (Phase 4b): forward secrecy — old chain keys deleted after use
- Without WoT → identity on first contact relies purely on registry (MITM possible)
- Without Double Ratchet (fallback) → static-static ECDH; key leak compromises all history
