# agent-comm — Technical Reference

P2P encrypted messaging for AI agents. Project: `~/.hermes/agent-comm/`

---

## Table of Contents

1. [Session](#session) — ECIES-encrypted peer-to-peer message exchange
2. [Registry](#registry) — URN → PeerID/Addrs resolution
3. [MQ](#mq) — Async message queue via relay nodes
4. [libp2p](#libp2p) — Host creation and network transport
5. [Crypto](#crypto) — Key management and ECIES encryption
6. [Setup Guide](#setup-guide) — Getting started

---

## Session

**File:** `session/session.go`  
**Protocol ID:** `/hermes/agent-comm/session/1.0.0`

### Overview

The session package provides encrypted peer-to-peer message exchange over libp2p streams. It uses ECIES (Elliptic Curve Integrated Encryption Scheme) with X25519 ECDH for key exchange, HKDF-SHA256 for key derivation, and AES-256-GCM for authenticated encryption.

### Manager

```go
type Manager struct {
    host          host.Host
    ecies         *crypto.ECIES
    keys          *crypto.IdentityKeys
    peerX25519PK  map[peer.ID][]byte  // cache of known peer X25519 PKs
}
```

### Key Methods

| Method | Description |
|--------|-------------|
| `NewManager(h host.Host, keys *crypto.IdentityKeys)` | Creates a new session manager |
| `SendMessage(ctx, target, recipientPubKey, plaintext)` | Opens stream, sends encrypted message, waits for encrypted reply |
| `SendReply(stream, recipientStaticPubKey, recipientURN, plaintext)` | Encrypts and sends reply over existing stream |
| `BuildEnvelope(recipientPubKey, plaintext)` | Builds encrypted envelope without sending (for MQ storage) |
| `DecryptEnvelope(env)` | Decrypts an `EncryptedEnvelope` |
| `PublicKey()` | Returns node's X25519 public key |
| `SetPeerX25519PK(p peer.ID, pk []byte)` | Caches peer's X25519 PK for later use |
| `PeerStaticX25519PK(p peer.ID)` | Retrieves cached peer's X25519 PK |
| `Ecies()` | Returns the underlying ECIES instance |

### Encryption Flow

```
Sender:
  1. ECDH(sender_SK, recipient_PK) → shared_secret
  2. HKDF(shared_secret, "agent-comm-ephemeral-v1") → ephemeral (32 bytes, deterministic)
  3. HKDF(shared_secret, ephemeral) → encKey (32 bytes)
  4. AES-GCM(encKey, nonce, payload, AAD=ProtoAAD) → ciphertext+tag

Recipient:
  1. ECDH(recipient_SK, sender_PK) → same shared_secret
  2. Re-derive ephemeral (deterministic, no transmission needed)
  3. Re-derive encKey
  4. AES-GCM-Decrypt(encKey, ciphertext, nonce, tag, AAD=ProtoAAD)
```

### Envelope Fields

```protobuf
message EncryptedEnvelope {
    string sender_urn = 1;
    bytes sender_static_pubkey = 2;   // X25519 static public key (32 bytes)
    bytes ephemeral_pubkey = 3;       // HKDF-derived (32 bytes)
    bytes nonce = 4;                  // AES-GCM nonce (12 bytes)
    bytes ciphertext = 5;
    bytes tag = 6;                    // GCM auth tag (16 bytes)
    string message_id = 7;
}
```

### AAD Constant

```go
const ProtoAAD = "agent-comm-v1"
```

AAD is computed as `SHA256("agent-comm-v1")[:16]`. **Never use PeerID or URN as AAD** — using a protocol-level constant avoids derivation mismatches between parties.

### Gotchas

1. **`mgr` must be created BEFORE `SetPeerX25519PK`**:
   ```go
   mgrB := session.NewManager(hostB, keysB)
   mgrB.SetPeerX25519PK(aPeerID, aX25519PK) // ✅ correct order
   ```

2. **Simplex EOF is normal** — each message is a new stream; `io.EOF` on response read is expected when no reply is needed.

3. **DR responder needs sender's X25519 PK** — `Receive()` looks up sender's PK via `mgr.PeerStaticX25519PK(senderPeerID)`. Cache it before the DR handler fires.

---

## Registry

**Files:** `registry/client.go`, `registry/server.go`  
**Protocol ID:** `/hermes/agent-comm/registry/1.0.0`

### Overview

The registry provides URN → PeerID/Addrs resolution via libp2p streams. When a node joins the network, it registers its URN with a registry server (typically the bootstrap node). Other nodes can then resolve a URN to get the peer's PeerID, listen addresses, and X25519 public key for ECIES encryption.

### Client

```go
type Client struct {
    host host.Host
}
```

#### Methods

| Method | Description |
|--------|-------------|
| `NewClient(h host.Host)` | Creates a new registry client |
| `Resolve(target peer.AddrInfo, urn string)` | Resolves a URN to PeerID, addrs, and X25519 pubkey |
| `Register(target peer.AddrInfo, urn string, addrs []multiaddr.Multiaddr, x25519PubKey []byte)` | Registers this node's URN mapping |

#### Resolve Result

```go
type ResolveResult struct {
    peer.AddrInfo       // ID + Addrs
    X25519PubKey []byte // X25519 public key for ECIES (nil if not registered)
}
```

### Server

```go
type Server struct {
    host  host.Host
    mu    sync.RWMutex
    账册  map[string]RegistryEntry  // in-memory URN → entry mapping
}

type RegistryEntry struct {
    Info        peer.AddrInfo  // PeerID + addrs
    X25519PubKey []byte        // X25519 public key for ECIES
}
```

#### Methods

| Method | Description |
|--------|-------------|
| `NewServer(h host.Host)` | Creates a new registry server |
| `HandleStream(stream)` | Services a registry request over a libp2p stream |
| `HandleRegister(urn, pid, addrs, x25519PubKey)` | Registers a URN mapping locally (used by bootstrap node) |
| `ListURNs()` | Returns all registered URNs |
| `Register()` | Sets the stream handler on the host |

### Protocol Flow

```
Client                              Server
   │── URNRegistryRequest(register) ──→  │
   │←─ URNRegistryResponse(ok) ─────────── │

Client                              Server
   │── URNRegistryRequest(resolve) ───→    │
   │←─ URNRegistryResponse(found) ───────── │
```

### Protocol Buffer Messages

```protobuf
message URNRegistryRequest {
    oneof op {
        RegisterRequest register = 1;
        ResolveRequest resolve = 2;
    }
}

message RegisterRequest {
    string urn = 1;
    string peer_id = 2;
    repeated string addrs = 3;
    bytes x25519_pubkey = 4;
}

message ResolveRequest {
    string urn = 1;
}

message URNRegistryResponse {
    oneof op {
        RegisterResponse register = 1;
        ResolveResponse resolve = 2;
    }
}

message RegisterResponse {
    bool ok = 1;
    string info = 2;  // error info if !ok
}

message ResolveResponse {
    bool found = 1;
    string peer_id = 2;
    repeated string addrs = 3;
    bytes x25519_pubkey = 4;
}
```

---

## MQ

**Files:** `mq/client.go`, `mq/server.go`  
**Protocol ID:** `/hermes/agent-comm/mq/1.0.0`

### Overview

The MQ (Message Queue) package provides async offline message storage via relay nodes. When a recipient is offline, a sender can store an encrypted message blob on a relay. The relay cannot read the content — only the intended recipient can decrypt it with their X25519 private key.

Messages are stored in SQLite on the relay, keyed by recipient URN. Each message has an expiry timestamp; relay auto-deletes expired messages.

### Client

```go
type Client struct {
    host host.Host
}
```

#### Methods

| Method | Description |
|--------|-------------|
| `NewClient(h host.Host)` | Creates a new MQ client |
| `Store(ctx, relay, recipientURN, envelope, ttlDays)` | Stores encrypted envelope on relay; returns message ID |
| `Retrieve(ctx, relay, recipientURN)` | Fetches all pending messages for recipient |
| `Ack(ctx, relay, messageIDs)` | Deletes successfully processed messages from relay |

### Server (Relay)

```go
type Server struct {
    host host.Host
    db   *sql.DB
}
```

#### Constructor

```go
func NewServer(h host.Host, dbPath string) (*Server, error)
```

- Creates SQLite database at `dbPath`
- Registers stream handler on `/hermes/agent-comm/mq/1.0.0`
- Starts background expiry cleanup loop (every 5 minutes)

#### Methods

| Method | Description |
|--------|-------------|
| `Close()` | Closes the SQLite database |

### SQLite Schema

```sql
CREATE TABLE messages (
    id         TEXT PRIMARY KEY,
    recipient  TEXT NOT NULL,     -- URN
    payload    BLOB NOT NULL,     -- EncryptedEnvelope bytes
    expiry     INTEGER NOT NULL,   -- Unix timestamp (0 = never expires)
    stored_at  INTEGER NOT NULL   -- Unix timestamp
);
CREATE INDEX idx_recipient ON messages(recipient);
CREATE INDEX idx_expiry ON messages(expiry);
```

### Protocol Flow

```
Sender ──→ Relay (store) ──→ Recipient (online later)
                     └── SQLite: urn → [encrypted_envelope, msg_id, expiry]

Recipient ──→ Relay (retrieve) ──→ Retrieve pending messages
Recipient ──→ Relay (ack)  ──→ Delete read messages
```

### Protocol Buffer Messages

```protobuf
message MQRequest {
    oneof op {
        StoreRequest store = 1;
        RetrieveRequest retrieve = 2;
        AckRequest ack = 3;
    }
}

message StoreRequest {
    string recipient_urn = 1;
    EncryptedEnvelope payload = 2;  // The encrypted message blob
    int64 expiry_unix = 3;          // TTL, 0 = relay default (7 days)
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
    repeated EncryptedEnvelope payloads = 1;
}

message AckResponse {
    bool ok = 1;
    int32 deleted_count = 2;
}

message ErrorResponse {
    string message = 1;
}
```

### End-to-End Flow

1. **B → A (A online):** Direct session via libp2p stream, ECIES encrypted, reply encrypted
2. **B → A (A offline):** B builds `EncryptedEnvelope`, calls `MQ.Store`, relay stores blob
3. **A comes online:** A calls `MQ.Retrieve`, decrypts each envelope using session.Manager, acks deletion

---

## libp2p

**File:** `libp2p/host.go`

### Overview

The libp2p package handles network transport layer creation and configuration for agent-comm. It wraps `github.com/libp2p/go-libp2p` with sensible defaults for peer-to-peer messaging.

### Config

```go
type Config struct {
    ListenAddrs   []string   // Multiaddr strings to listen on
    EnableRelay   bool       // Enable Circuit Relay
    EnableDHT     bool       // Enable Kad-DHT (Phase 2)
    PrivKeyBytes  []byte     // Ed25519 private key bytes (for persistent identity)
    ProtocolID    string     // Base protocol ID
    ResourceConns int        // Max concurrent connections
}
```

### Default Config

```go
func DefaultConfig() Config {
    return Config{
        ListenAddrs: []string{
            "/ip4/0.0.0.0/tcp/0",
            "/ip4/0.0.0.0/udp/0/quic",
        },
        EnableRelay:   true,
        EnableDHT:     false,
        ProtocolID:    "/hermes/agent-comm/1.0.0",
        ResourceConns: 64,
    }
}
```

### Host Creation

```go
func NewHost(cfg Config) (host.Host, error)
```

- If `PrivKeyBytes` is provided, uses it for identity (enabling persistence)
- Otherwise generates a new random identity
- Enables NAT service detection

### Utility Functions

| Function | Description |
|----------|-------------|
| `ConnectToPeer(ctx, h, addr)` | Establishes connection to peer by address string |
| `GetPeerID(h)` | Returns host's peer ID as base58 string |
| `GetPeerAddrs(h)` | Returns all addresses in `/ip4/x.x.x.x/tcp/y/p2p/PeerID` format |
| `AddrsWithID(h)` | Alias for `GetPeerAddrs` |
| `CloseHost(h)` | Gracefully closes the host |
| `ProtocolIDForTopic(topic)` | Returns full protocol ID for a topic |

### Address Format

Peer addresses use the format: `/ip4/x.x.x.x/tcp/y/p2p/PeerID`

Example:
```
/ip4/192.168.1.100/tcp/5001/p2p/12D3KooWBdmrNJr6jWGW6d4m7bP6xK5YmK3example1Q9Xw1XYvY5
```

### Connection with Timeout

`ConnectToPeer` uses a 30-second connection timeout.

---

## Crypto

**Files:** `crypto/ecies.go`, `crypto/keys.go`

### Overview

The crypto package provides:
- **ECIES encryption:** X25519 ECDH + HKDF-SHA256 + AES-256-GCM
- **Key management:** Ed25519 identity keys + X25519 encryption keys
- **Identity derivation:** URN and PeerID from Ed25519 public key

### ECIES Encryption

**File:** `crypto/ecies.go`

#### Constants

| Constant | Value | Description |
|----------|-------|-------------|
| `NonceSize` | 12 bytes | AES-GCM nonce size |
| `TagSize` | 16 bytes | GCM authentication tag size |
| `KeySize` | 32 bytes | AES-256 key size |

#### Key Generation

```go
func (e *ECIES) GenerateKeyPair() ([]byte, []byte, error)
```

Generates a new X25519 key pair (32-byte private key, 32-byte public key). Private key is clamped for X25519 compatibility.

#### Shared Secret Computation

```go
func (e *ECIES) ComputeSharedSecret(privateKey, publicKey []byte) ([]byte, error)
```

Performs X25519 ECDH. Returns 32-byte shared secret. Checks for low-order point attack (shared secret == 0).

#### Key Derivation

```go
func (e *ECIES) DeriveKeys(sharedSecret, info []byte) ([]byte, error)
```

HKDF-SHA256 key derivation. Derives a 32-byte encryption key from shared secret and info string.

#### Encrypt/Decrypt with Shared Secret

```go
func (e *ECIES) EncryptWithSharedSecret(sharedSecret, plaintext, aad []byte) (
    ephemeral, nonce, ciphertext, tag []byte, error)

func (e *ECIES) DecryptWithSharedSecret(
    sharedSecret, senderEphemeral, nonce, ciphertext, tag, aad []byte,
) ([]byte, error)
```

- **Ephemeral** is derived deterministically from shared secret via `HKDF(sharedSecret, "agent-comm-ephemeral-v1")`
- Recipient can re-derive ephemeral without it being transmitted
- **AAD** (Additional Authenticated Data) provides authenticity guarantee

#### Standard Encrypt/Decrypt

```go
func (e *ECIES) Encrypt(recipientStaticPublicKey, plaintext, aad []byte) (...)

func (e *ECIES) Decrypt(staticPrivateKey, senderEphemeralPublicKey, nonce, ciphertext, tag, aad []byte) ([]byte, error)
```

Generates ephemeral key pair internally. Computes ECDH between ephemeral private and recipient static public key.

#### Helper Functions

```go
func (e *ECIES) DeriveEphemeral(sharedSecret []byte) ([]byte, error)
// Re-derives the same ephemeral that EncryptWithSharedSecret derives

func EncodeToBase64(data []byte) string
func DecodeFromBase64(s string) ([]byte, error)
```

### Identity Keys

**File:** `crypto/keys.go`

#### IdentityKeyPair

```go
type IdentityKeyPair struct {
    PrivateKey ed25519.PrivateKey
    PublicKey  ed25519.PublicKey
}
```

| Method | Description |
|--------|-------------|
| `GenerateIdentityKeyPair()` | Creates new Ed25519 key pair |
| `Fingerprint()` | Returns `base58(SHA256(pubkey)[:16])` |
| `URN()` | Returns `urn:hermes:agent:<fingerprint>` |
| `PeerID()` | Derives libp2p PeerID from Ed25519 public key |
| `Sign(data)` | Signs data with Ed25519 private key |
| `Verify(data, sig)` | Verifies Ed25519 signature |
| `SavePrivatePEM(path)` | Saves private key as PEM file (mode 0600) |
| `SavePublicPEM(path)` | Saves public key as PEM file (mode 0644) |

#### IdentityKeys

```go
type IdentityKeys struct {
    Ed25519    *IdentityKeyPair
    X25519SK   []byte  // X25519 static private key (32 bytes)
    X25519PK   []byte  // X25519 static public key (32 bytes)
    KeysDir    string  // Directory where keys are stored
}
```

| Function | Description |
|----------|-------------|
| `LoadOrCreateIdentity(keysDir)` | Loads existing keys or creates new ones |
| `DefaultKeysDir()` | Returns `~/.hermes/agent-comm/contacts` |
| `EnsureKeysDir()` | Creates keys directory if not exists |

Key file names in `KeysDir`:
- `identity_sk.pem` — Ed25519 private key
- `identity_pk.pem` — Ed25519 public key
- `identity_x25519_sk.pem` — X25519 private key
- `identity_x25519_pk.pem` — X25519 public key

### Key Loading

```go
LoadPrivatePEM(path string) (*IdentityKeyPair, error)
LoadPublicPEM(path string) (ed25519.PublicKey, error)
LoadX25519PrivatePEM(path string) ([]byte, error)
LoadX25519PublicPEM(path string) ([]byte, error)
```

PEM files use `PRIVATE KEY` / `PUBLIC KEY` block type.

### URN Format

```
urn:hermes:agent:<base58(SHA256(ed25519_pubkey)[:16])>
```

Examples:
- Ed25519 pubkey fingerprint: `base58(SHA256(pubkey)[:16])` — 16 bytes → ~22 char base58 string
- URN: `urn:hermes:agent:8jKk7xWk9Y7JpQr3nV6m`

### Token

```go
type Token struct {
    Value     string  // 256-bit random hex string
    ExpiresAt int64   // Unix timestamp
    Used      bool
}

func GenerateToken() (string, error)
```

One-time tokens for contact exchange (prevents contact reuse).

### Message ID Generation

```go
func GenerateMessageID() string
// Returns: "msg_" + uuid_v4
```

---

## Setup Guide

### Prerequisites

- **Go 1.25.10** (binary: `~/.local/go/bin/go`)
- **libp2p v0.36+**
- **WSL/Linux** environment

### Project Location

```
~/.hermes/agent-comm/
```

### 1. Clone / Update

```bash
cd ~/.hermes/agent-comm
git pull  # if already cloned
```

### 2. Dependencies

```bash
cd ~/.hermes/agent-comm
~/.local/go/bin/go mod download
```

Verified dependencies:
```
go-libp2p: v0.36.3
go-libp2p-core: v0.20.1
go-libp2p-kad-dht: v0.39.2
quic-go: v0.45.2 (transitive)
```

### 3. Key Generation

Keys are auto-generated on first run in `~/.hermes/agent-comm/contacts/`:

```
contacts/
├── identity_sk.pem        # Ed25519 private key
├── identity_pk.pem        # Ed25519 public key
├── identity_x25519_sk.pem # X25519 private key
└── identity_x25519_pk.pem # X25519 public key
```

### 4. Build & Run Tests

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

### 5. Run Bootstrap Node (Registry + Relay)

```bash
cd ~/.hermes/agent-comm
~/.local/go/bin/go run ./cmd/bootstrap/
```

The bootstrap node:
- Creates a libp2p host with TCP/QUIC listeners
- Enables Relay v2 and AutoNAT
- Registers itself in the DHT
- Starts the registry server (URN → PeerID resolution)
- Starts the MQ relay server (offline message storage)

### 6. Run Client Node

```bash
cd ~/.hermes/agent-comm
~/.local/go/bin/go run ./cmd/client/
```

The client node:
- Loads or generates identity keys
- Connects to bootstrap node
- Registers its URN with the bootstrap registry
- Can send/receive encrypted messages

### 7. Project Structure

```
agent-comm/
├── crypto/           # Ed25519/X25519 keys + ECIES
│   ├── ecies.go     # ECIES encryption/decryption
│   └── keys.go      # Identity key management
├── libp2p/           # Host construction + utilities
│   └── host.go      # libp2p.Host creation
├── dht/              # Kad-DHT wrapper
│   └── dht.go
├── registry/         # URN registry client + server
│   ├── client.go    # Resolve, Register
│   └── server.go    # URN → PeerID handler
├── session/          # ECIES session manager
│   └── session.go   # Encrypted message exchange
├── mq/               # Async message queue
│   ├── client.go    # Store, Retrieve, Ack
│   └── server.go    # SQLite-backed relay
├── dr/               # Double Ratchet
│   ├── ratchet.go   # RatchetState + DH steps
│   ├── session.go   # DRSession encrypt/decrypt
│   └── store.go     # SQLite persistence
├── wot/              # Web of Trust (partial)
├── proto/            # Protobuf definitions
│   ├── registry.proto
│   ├── mq.proto
│   └── *.pb.go
├── contacts/         # Identity keys (auto-created)
└── cmd/
    ├── bootstrap/   # Bootstrap node (registry + relay)
    ├── client/      # Client node
    └── test_*/      # Phase verification tests
```

### 8. Security Properties

| Property | Mechanism |
|----------|-----------|
| Identity | Ed25519 for signing, self-certifying URN |
| Key separation | X25519 separate from Ed25519 identity key |
| Forward secrecy | Double Ratchet (Phase 4b) |
| E2E encryption | ECIES: X25519 ECDH + HKDF + AES-256-GCM |
| Relay privacy | Relay stores only encrypted blobs (cannot read) |
| Contact exchange | One-time tokens prevent reuse |

### 9. Default Ports

The bootstrap node listens on random TCP/QUIC ports (specified by `/ip4/0.0.0.0/tcp/0`). Client discovers the bootstrap's address via DHT or direct configuration.

### 10. Troubleshooting

**"protocols not supported" error:**
- Ensure both nodes have registered the same protocol ID
- Each node must call `host.SetStreamHandler(ProtoID, handler)`

**DR responder fails to decrypt:**
- Verify `SetPeerX25519PK` was called before DR handler fires
- Check that sender's X25519 PK is correct

**Connection fails:**
- Verify bootstrap node is running and reachable
- Check firewall allows TCP/QUIC on the bootstrap's listen addresses
- Ensure Relay v2 is enabled on both nodes

**Keys not loading:**
- Check `~/.hermes/agent-comm/contacts/` directory exists
- Verify PEM file format (must have `-----BEGIN PRIVATE KEY-----` header)