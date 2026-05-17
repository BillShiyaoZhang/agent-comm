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

**完成。** libp2p v0.36+，包含：

- TCP + QUIC 监听
- Relay v2（NAT 穿透连接中继）
- AutoNAT
- gossipsub（未来扩展用）

---

## Phase 2 — Identity and Encrypted Sessions

### Identity

每个节点有一对 `IdentityKeys`（持久化到磁盘）：
- **Ed25519**：用于 `libp2p.Identity()` — 稳定 PeerID，URN 推导
- **X25519**：用于 ECIES 加密 — 与 Ed25519 分离，保证前向保密

URN 格式：`urn:hermes:agent:<base58(random16bytes)>`

URN 从 Ed25519 公钥派生，因此是自证明的。

### Registry

URN → PeerID/addrs/X25519PubKey 通过 libp2p stream 解析。

**协议：** `/hermes/agent-comm/registry/1.0.0`

```
Client                              Server
   │── URNRegistryRequest(register) ──→  │
   │←─ URNRegistryResponse(ok) ─────────  │

Client                              Server
   │── URNRegistryRequest(resolve) ──→   │
   │←─ URNRegistryResponse(found) ───────  │
```

### Encrypted Session

**协议：** `/hermes/agent-comm/session/1.0.0`

基于 stream 的请求/响应，使用 `EncryptedEnvelope`：

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

**Envelope 字段：**
- `sender_urn` — 发送者 URN
- `sender_static_pubkey` — X25519 静态公钥（用于 ECDH 回复）
- `ephemeral_pubkey` — HKDF 导出的 32 字节值
- `nonce` — 随机 12 字节 AES-GCM nonce
- `ciphertext` — 加密载荷
- `tag` — GCM 认证标签（16 字节）
- `message_id` — 唯一 ID，用于去重

**AAD：** `SHA256("agent-comm-v1")` — 协议常量，双向相同。

**实现：** `session/session.go` — `Manager` 结构体

- `SendMessage()` — 发送消息并等待加密回复
- `SendReply()` — 在已有 stream 上发送加密回复
- `BuildEnvelope()` — 构建加密信封（用于 MQ 存储）
- `DecryptEnvelope()` — 解密收到的信封

---

## Phase 3 — Async Message Queue

### Problem

接收方离线时消息会丢失，需要持久的离线存储。

### Design

**relay 节点**（bootstrap 节点）充当信箱：存储离线接收者的加密消息。Relay 无法读取消息内容（加密 blob）。

### Architecture

```
Sender ──→ Relay (store) ──→ Recipient (online later)
                    └── SQLite: urn → [encrypted_envelope, msg_id, expiry]

Recipient ──→ Relay (pull) ──→ Retrieve pending messages
Recipient ──→ Relay (ack)  ──→ Delete read messages
```

### Protocol: `/hermes/agent-comm/mq/1.0.0`

**Proto 文件：** `proto/mq.proto`（引用 `proto/envelope.proto` 中的 `EncryptedEnvelope`）

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
  EncryptedEnvelope payload = 2;  // 加密消息 blob（relay 无法读取）
  int64 expiry_unix = 3;          // TTL：relay 在此时间戳后删除（0 = 永不过期）
}

message RetrieveRequest {
  string recipient_urn = 1;
}

message AckRequest {
  repeated string message_ids = 1;  // 客户端处理完成后删除
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
  string message_id = 2;  // relay 分配
}

message RetrieveResponse {
  repeated EncryptedEnvelope payloads = 1;  // 按时间顺序
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

**实现：** `mq/server.go` — `Server` 结构体，SQLite 持久化

```sql
CREATE TABLE messages (
  id         TEXT PRIMARY KEY,
  recipient  TEXT NOT NULL,          -- URN
  payload    BLOB NOT NULL,           -- EncryptedEnvelope 字节
  expiry     INTEGER NOT NULL,        -- Unix 时间戳
  stored_at  INTEGER NOT NULL         -- Unix 时间戳
);
CREATE INDEX idx_recipient ON messages(recipient);
CREATE INDEX idx_expiry ON messages(expiry);
```

Relay 在启动时和每 5 分钟定期删除过期消息。

### Client Behavior

**实现：** `mq/client.go` — `Client` 结构体

- `Store(ctx, relay, recipientURN, envelope, ttlDays)` — 发送到 relay
- `Retrieve(ctx, relay, myURN)` — 拉取所有待处理消息
- `Ack(ctx, relay, messageIDs)` — 读取后删除

**发送流程：**
1. 通过 Registry 解析接收者 PeerID
2. 尝试直接会话（`SendMessage`）
3. 接收者在线 → 完成
4. 接收者离线 / 连接失败 → 尝试 MQ store：
   a. 找到 relay（默认使用 bootstrap 节点）
   b. `MQStoreRequest` + 加密载荷
   c. Relay 返回 `message_id`

**接收流程：**
1. 启动时：`MQRetrieveRequest` 拉取所有待处理消息
2. 解密每条消息（正常通过 Session 解密）
3. 处理完成后：`MQAckRequest` 从 relay 删除

### Relay Discovery

每个节点可指定一个 relay URN。未设置则默认使用 bootstrap 节点的 URN（在 DHT bootstrap 过程中注册）。

同一节点同时处理 URN registry 和 MQ。

### Test

```bash
go run ./cmd/test_mq/    # 三节点测试：Relay + Sender + Receiver（离线后上线）
```

---

## Phase 4a — Web of Trust

### Problem

当 Bob 第一次向 Alice 发送消息时，Bob 如何确认 `urn:hermes:agent:Alice` 属于真实的 Alice 而不是冒充者？Registry 只提供 URN→PeerID→pubkey 映射，不做身份认证。

### Solution

签名信任声明。如果 Charlie 说"我信任 `urn:hermes:agent:Alice`（key=X, peer=12D3...）"，且 Bob 已信任 Charlie，则 Bob 可以推导对 Alice 的传递信任。

### Trust Claim

**Proto 文件：** `proto/wot.proto`

```proto
enum TrustLevel {
  UNKNOWN = 0;
  TRUSTED = 1;
  UNTRUSTED = 2;
}

message TrustClaim {
  string issuer_urn = 1;          // 声明者（如 "urn:hermes:agent:Charlie"）
  string subject_urn = 2;          // 被声明者（如 "urn:hermes:agent:Alice"）
  string subject_peer_id = 3;      // Subject 的 libp2p PeerID
  bytes  subject_x25519_pk = 4;   // Subject 的 X25519 静态公钥（32 字节）
  TrustLevel level = 5;            // TRUSTED / UNTRUSTED / UNKNOWN
  bytes  issuer_signature = 6;     // Ed25519 签名
  int64  issued_at_unix = 7;       // Unix 时间戳
}
```

**Trust Levels：**
- `TRUSTED` — 声明者明确为 subject 的身份担保
- `UNTRUSTED` — 声明者明确不信任（撤销用例）
- `UNKNOWN` — 中性陈述（知情但不做判断）

**签名内容：** `SHA256(issuer_urn || subject_urn || subject_peer_id || subject_x25519_pk || level || issued_at)`

### Implementation

- `wot/claim.go` — `TrustClaim` 结构体，`NewTrustClaim()` 创建声明，`Verify()` 验证签名
- `wot/store.go` — `Store` 结构体，SQLite 持久化所有已知声明（本地声明 + 网络获取的）
- `wot/resolver.go` — `Resolver` 结构体，BFS 信任路径搜索

**Store Schema：**
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

**Trust Path Resolution：**
- BFS 从 `myURN` 出发，找任意发出了 `TRUSTED` 声明的节点关于目标 URN 的路径
- 找到路径 → 验证签名链 → 接受 pubkey
- 无路径 → 警告："Untrusted peer, proceed manually?"

**网络查询：** `Resolver.FetchClaimsAbout(ctx, subjectURN)` — 通过 `/hermes/agent-comm/wot/1.0.0` 协议查询远端节点关于某 URN 的所有声明

**Bootstrap trust：** 首次运行需要手动输入一个已知 URN 作为直接信任对象。之后 WoT 网络通过传递声明增长。

---

## Phase 4b — Double Ratchet

### Problem

如果 Bob 的静态密钥明天被泄露，所有过去与 Alice 的消息都会被解密（静态-静态 ECDH 无前向保密）。

### Solution

Double Ratchet（Signal Protocol）。每条消息使用从 ratchet state 派生的新临时密钥。一把密钥泄露只暴露有限窗口的消息。

### Architecture

```
dr/
├── ratchet.go   # RatchetState: root key, chain keys, DH ratchet step
├── session.go   # DRSession: encrypt/decrypt with ratchet, ECIES for initial key exchange
└── store.go     # Persistent session state (per-peer URN, SQLite)
```

### Key Derivation Chain

```
初始：ECDH(our_DH_SK, their_DH_PK) → SKR
SKR → HKDF → root_key + chain_key_0

发送消息 (k=0)：
  chain_key_0 → HKDF → message_key_0 + chain_key_1
  encrypt(message_key_0, plaintext) → ciphertext

DH Ratchet step（收到对方新 DH 公钥后）：
  ECDH(our_new_DH_SK, their_DH_PK) → SKR2
  SKR2 → HKDF → root_key_2 + chain_key_2
```

### Integration with ECIES Session

- `session/session.go` 已实现 ECDH + ECIES 初始密钥交换
- Double Ratchet 用初始 ECDH 握手替换静态-静态 ECDH
- `dr/session.go` 提供 `DRSendMessage` / `DRReceiveMessage` 作为 `SendMessage` / stream handler 的替代
- 前向保密：N 条消息后旧 chain key 被删除

**注意：** Double Ratchet 需要每对等节点的有状态会话（不同于当前无状态的 ECIES）。复杂度增加 — 保留当前 `session/session.go` 作为 DR state 不可用时的降级方案。

---

## Phase 5 — Storage Layer (DRSession Persistence)

**完成。** `dr/store.go` — SQLite 持久化 DR 会话存储。

### Problem

Double Ratchet 是有状态的。每个对等方必须持久化 `RatchetState`（root key, chain keys, DH key pairs, message numbers）以在重启后存活。无持久化情况下，每次重启重置 ratchet，破坏前向保密。

### Storage Schema

```sql
CREATE TABLE dr_sessions (
    peer_urn    TEXT PRIMARY KEY,   -- 对等方 URN
    state       BLOB NOT NULL,      -- 序列化的 RatchetState
    updated_at  INTEGER NOT NULL    -- Unix 时间戳
);
```

### Test

```bash
go run ./cmd/test_dr_persist/
```

测试流程：创建会话 → 发送/接收 → 模拟重启 → 恢复 → 继续通信。

---

## Phase 6 — Network Transport (E2E DR over libp2p)

**完成。** `cmd/test_dr_net/main.go` — 两节点双向 DR 消息交换，通过真实 libp2p。

### What Works

- **B → A**：B（initiator）打开 DR stream 到 A，A 的 responder handler 接收并解密
- **A → B**：A（initiator）打开 DR stream 到 B，B 的 responder handler 接收并解密
- 双向使用独立的 DRSession initiator 会话
- Simplex 模式：每条消息一个新 stream；读端 EOF 可接受
- X25519 PK 缓存：`session.Manager.SetPeerX25519PK()` + `mgr.PeerStaticX25519PK()` 用于带内对等方密钥查找

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

- **Responder ratchet 初始化**：`NewDRSessionResponder` 创建空 ratchet；第一条入站消息的 header 用于初始化（X3DH-style agreement 嵌入 DR header）
- **Peer X25519 PK 查找**：`session.Manager` 有本地 `peerX25519PK map[peer.ID][]byte` 缓存。调用者必须在 `Receive()` 前通过 `SetPeerX25519PK()` 设置对等方静态密钥
- **Simplex streams**：`DRSession.Send()` 用 `io.EOF` 容限读取响应大小 — simplex 模式下对等方发送后关闭 stream
- **Protocol IDs**：ECIES session 使用 `/hermes/agent-comm/session/1.0.0`，DR 使用 `/agent/dr/1.0.0`（独立协议协商）

### Test

```bash
go run ./cmd/test_dr_net/
```

预期输出：
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
│   ├── keys.go       # Identity key 加载/创建（Ed25519 + X25519）
│   └── ecies.go      # ECIES 加密/解密
├── dht/
│   └── dht.go        # Kad-DHT 封装
├── libp2p/
│   └── host.go       # libp2p.Host 构建
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
│   ├── claim.go      # TrustClaim 创建和验证
│   ├── store.go      # SQLite persistence
│   └── resolver.go   # BFS trust path resolution
├── dr/               # Phase 4b: double ratchet
│   ├── ratchet.go    # RatchetState: root/chain keys, DH step
│   ├── session.go    # DRSession: encrypt/decrypt
│   └── store.go      # DRSession persistence (SQLite)
├── store/
│   └── relay/        # (空目录，relay 功能由 mq/server.go 实现)
├── cmd/
│   ├── bootstrap/    # Bootstrap/relay 节点（registry + MQ server）
│   ├── client/       # 常规客户端节点
│   ├── test_session/  # Phase 2: 两节点 ECIES 加密会话测试
│   ├── test_mq/       # Phase 3: 三节点离线消息测试（relay + sender + receiver）
│   ├── test_dr_persist/  # Phase 5: DR 会话持久化测试
│   ├── test_dr_net/     # Phase 6: 两节点 libp2p 双向 DR 测试
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

- Relay 存储加密 blob — 无法读取消息内容
- Relay 可移除而不影响消息机密性
- AAD = 协议常量（非对等方特定）— 避免 URN↔PeerID 推导问题
- WoT（Phase 4a）：信任声明使用 Ed25519 签名；接受新对等方 pubkey 前验证信任路径
- Double Ratchet（Phase 4b）：前向保密 — 旧 chain key 使用后删除
- 无 WoT → 首次联系身份仅依赖 registry（MITM 可能）
- 无 Double Ratchet（当前）→ 静态-静态 ECDH；密钥泄露暴露所有历史

---

## Test Commands

```bash
# Phase 2: 两节点 ECIES 加密会话
go run ./cmd/test_session/

# Phase 3: 三节点离线消息测试（relay + sender + receiver）
go run ./cmd/test_mq/

# Phase 5: DR 会话持久化（SQLite 往返）
go run ./cmd/test_dr_persist/

# Phase 6: 两节点 libp2p 双向 DR
go run ./cmd/test_dr_net/

# Bootstrap 节点（registry + MQ server）
go run ./cmd/bootstrap/

# 客户端节点（交互式：send/pull/quit）
BOOTSTRAP_ADDR=/ip4/127.0.0.1/tcp/45041/p2p/... go run ./cmd/client/
```