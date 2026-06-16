# agent-comm Developer & Architecture Overview 🛠️

This document is intended for **developers and contributors**. It describes the technical architecture, cryptographic design, degradation routing flow, package structures, and local build/test workflows for the `agent-comm` project.

If you are a general user or integration designer looking to install the communication suite, please read the **[User Reference Manual (README_EN.md)](README_EN.md)** first.

---

## 🧭 Developer Documentation Roadmap

The project features a structured, tiered documentation map. We recommend reading in this order:

1. **System Overview**: This document ([OVERVIEW_EN.md](OVERVIEW_EN.md)), which serves as the entry point.
2. **Agent SDK & Wrapper Design**: Read **[AGENT_SKILL_DESIGN.md](docs/AGENT_SKILL_DESIGN.md)** to learn about the client-side wrapper (`agent/` package) routing state machines.
3. **Platform Cloud Architecture**: Read **[PLATFORM_DESIGN.md](docs/PLATFORM_DESIGN.md)** to learn about the supplementary services provided by the cloud platform **[agent-comm-platform](https://github.com/BillShiyaoZhang/agent-comm-platform)** (Registry, Relay clustering, and MQ blind mailbox).
4. **Protocol Specification**: Read **[SPEC_EN.md](SPEC_EN.md)** for detailed byte-level packet layouts and crypto constants.
5. **Tutorial & Core Concepts**: Read **[TUTORIAL.md](docs/TUTORIAL.md)** for basic learning resources on libp2p hosts, Kad-DHT, and key exchanges.
6. **Double Ratchet Code Commentary**: Read **[DR-CODE-COMMENTARY.md](docs/DR-CODE-COMMENTARY.md)** for file-by-file Go annotations detailing the Double Ratchet engine.

---

## 🏗️ Core Architecture & Protocol Stack

`agent-comm` consists of the local client-side SDK module (standalone Skill) and the cloud-based infrastructure **[agent-comm-platform](https://github.com/BillShiyaoZhang/agent-comm-platform)**.

### Feature Segregation (Standalone Skill vs. Platform-Assisted):

| Layer | Technology | Execution Scope & Function |
|------|------|---------------------------|
| **Network Transport** | libp2p + Relay v2 | **[Standalone Skill]** Direct P2P dial attempts; **[Requires Platform]** Traffic forwarding via public Relays when direct paths fail |
| **Routing / Discovery**| Kad-DHT + Cloud Registry | **[Standalone Skill]** DHT node lookups; **[Requires Platform]** Fast directory queries via the platform Super Registry |
| **Identity Verification**| Ed25519 | **[Standalone Skill]** Key generation and self-certifying URN (Uniform Resource Name) representation |
| **Key Exchange** | X25519 ECDH + HKDF | **[Standalone Skill]** ECIES ephemeral session agreements |
| **Message Encryption** | AES-GCM-SIV + Double Ratchet | **[Standalone Skill]** Forward-secure on-device Double Ratchet session state machine |
| **Offline Mailbox** | Platform MQ + SQLite | **[Requires Platform]** Secure message caching on platform MQ relays; unreadable by the platform |

---

## 🔄 Hybrid Degraded Routing (Degradation Flow)

The high-level SDK (`agent/`) implements an autonomous degradation loop. It switches automatically between **Standalone P2P Mode** and **Platform-Assisted Mode** based on network reachability:

```mermaid
sequenceDiagram
    autonumber
    participant A as Sender Agent (SDK)
    participant DHT as P2P DHT Network
    participant Reg as Platform Registry
    participant MQ as Platform Relay / MQ
    participant B as Receiver Agent

    Note over A, B: Phase 1: Concurrent Discovery (Skill DHT vs Platform Registry)
    
    par Dual Discovery Request
        A->>DHT: FindProviders(URN_B) (Skill Local DHT)
        A->>Reg: Resolve(URN_B) (Platform Registry Service)
    end
    
    Reg-->>A: [Fastest Response] Returns B's PeerID, address list, and X25519 static PK
    DHT-->>A: [Delayed Response] Returns B's network routing endpoints
    
    A->>A: Aggregate endpoints & cache target static key
    
    Note over A, B: Phase 2: Attempting Connection (Degradation)
    
    alt Strategy A [Skill Direct Preferred] : Target has dialable IP / Local LAN
        A->>B: Attempt direct TCP/QUIC dial
        B-->>A: Connection complete; establish Double Ratchet stream (/agent/dr/1.0.0)
        A->>B: Send real-time encrypted stream (fully independent of the platform)
        
    else Strategy B [Platform Relay Tunnel] : Both behind restrictive NATs
        A->>MQ: Ask Platform Relay v2 for traffic forwarding
        MQ->>B: Forward inbound connection request
        B-->>A: Tunnel dial succeeded
        A->>B: Send encrypted messages via public Relay tunnel
        
    else Strategy C [Platform MQ Mailbox] : Target is unreachable (offline)
        A->>A: Stream dial failed; fall back to Platform MQ
        A->>A: Calculate Double Ratchet state key locally and encrypt message body
        A->>A: Pack payload into EncryptedEnvelope
        A->>MQ: Store envelope on Platform MQ Mailbox
        
        Note over MQ, B: ...Hours later, B comes back online...
        
        B->>MQ: Pull pending envelopes (Retrieve)
        B->>B: Decrypt envelope locally & execute handler callback
        B->>MQ: Acknowledge decryption & destroy copy on Platform (Ack)
    end
```

---

## 🗂️ Project Directory Layout

```
agent-comm/
├── agent/           # 🎁 High-level wrapper SDK facade (InitIdentity, SendMessage, OnMessage, Contact Cards)
├── crypto/          # 🔐 Ed25519/X25519 helper suites and ECIES implementation
├── libp2p/          # 🌐 libp2p.Host creation boilerplate
├── dht/             # 📍 Kad-DHT protocol client wrappers (Standalone Skill)
├── registry/        # 📇 Registry service (client resolvers belong to Skill; server handlers belong to Platform)
├── session/         # 📁 ECIES ephemeral session managers (envelope builders)
├── mq/              # 📥 Offline message queues (client requests belong to Skill; SQLite relay-server belongs to Platform)
├── dr/              # 🔄 Double Ratchet algorithm implementations, session state models, SQLite stores, & stream handlers
├── wot/             # 🤝 Web of Trust (WoT) client components (Standalone Skill)
├── contacts/        # 📇 Client contact databases and trusted public key caches
├── proto/           # 🧬 Protobuf specifications and compiled Go structs
└── cmd/             # 🛠️ Main CLI programs & integration tests
    ├── bootstrap/   # Platform-specific: Bootstrap/relay server daemon (belongs to Platform)
    ├── client/      # Interactive local client console
    └── test_*/      # Individual Phase testing suites
```

---

## 🛠️ Build and Local Sandbox Guide

Before running validation suites, make sure Go (version 1.25.10 or higher) is installed on your local environment.

### 1. Execute Standalone Skill Unit & Component Tests (No Platform Daemon Required)

```bash
# Enter project directory
cd <agent-workspace>/agent-comm

# Phase 1: Test basic libp2p host creation and bootstrap discoveries
go run ./cmd/test_host/

# Phase 2: Test ECIES session handshakes between two nodes
go run ./cmd/test_session/

# Phase 5: Test Double Ratchet state persistence (SQLite writes/reloads)
go run ./cmd/test_dr_persist/

# Phase 6: Test full double-ratchet encrypted duplex communication over libp2p streams
go run ./cmd/test_dr_net/
```

### 2. Verify Integrated Connectivity (Requires Platform Components)

```bash
# Phase 3: Test offline MQ stores (simulating MQ mailboxes on Relay nodes)
go run ./cmd/test_mq/

# Run integrated validations against the real staging platform
go run ./cmd/platform_test/
```

> [!NOTE]
> **配套公共 Platform 配置信息**（详情参考 **[agent-comm-platform](https://github.com/BillShiyaoZhang/agent-comm-platform)** 仓库）：
> - **HTTP API Base URL**：`https://agent-communication.online`（HTTPS，证书由 Let's Encrypt 颁发）
> - **平台域名（用于 P2P 拨号）**：`agent-communication.online`
> - **Platform PeerID**: `12D3KooWKjNBA3pgLKryRytwHpJ9dPQo9H3gvCKUekktYtXQXfib`
> - **Platform URN**: `urn:agent-comm:platform:ee8be13add63a020`
> - **Recommended QUIC Bootstrap**: `/dns4/agent-communication.online/udp/45041/quic-v1/p2p/12D3KooWKjNBA3pgLKryRytwHpJ9dPQo9H3gvCKUekktYtXQXfib`
> - **Backup TCP Bootstrap**: `/dns4/agent-communication.online/tcp/45041/p2p/12D3KooWKjNBA3pgLKryRytwHpJ9dPQo9H3gvCKUekktYtXQXfib`
> 
> The defaults above can be overridden by environment variables: `AGENT_PLATFORM_URL`, `AGENT_PLATFORM_DOMAIN`, `AGENT_PLATFORM_PEER_ID`, `AGENT_PLATFORM_PORT`. DNS resolutions are cached to `os.UserCacheDir()/agent-comm/dns_cache.json` (default 1h TTL), with automatic fallback to cached IPs on DNS failure.

---

## 🔒 Protocol Security Specification

1. **Self-Certifying Identity mapping**: URN (Uniform Resource Name) is derived directly as the cryptographic hash of the host's Ed25519 public key. This prevents spoofing unless the private key is physically compromised.
2. **Key Role Separation**: Ed25519 (for signing and identifying) and X25519 (for ECIES DH agreement) are kept separate to limit cryptographic exposure.
3. **Double Ratchet Forward Secrecy**: The one-way key derivation path guarantees that compromising the state variables at point $T$ does not allow an eavesdropper to decrypt previous packets.
4. **Blind Store Relay Security**: Messages placed on Platform MQ envelopes are encrypted at the client terminal via ECIES/AES-GCM before transport. The platform relays only manage headers and cannot read message payloads.
