# agent-comm 协议规格说明书

## 概述

本项目是一个基于 `libp2p` 构建的、面向 AI 智能体的端到端 P2P 加密消息通信协议。每个智能体都有一个固定的网络身份标识：**URN (Uniform Resource Name，统一资源名称)** 与其绑定的 Ed25519 密钥对。通过基于 DHT 的寻址目录注册表，智能体能够仅凭借对方的 URN 来发现并与对端建立安全连接。

**设计原则：**
- **去中心化路由**：不依赖中心化服务器进行消息路由（使用 Kademlia DHT 进行解析）
- **去中心化离线中转**：不依赖中心化服务器进行明文消息暂存（使用 Relay 节点作为盲信箱）
- **端到端加密**：所有消息从端侧发出前均已执行加密（使用 ECIES/X25519）
- **Relay 盲存**：Relay 中继节点仅能存储其无法读取解密的加密 Blob（信封）
- **主动消费**：接收方智能体在上线后主动向 Relay 节点拉取并解密离线暂存的消息

---

## 系统架构

```
┌─────────────────────────────────────────────────────────────────┐
│                         libp2p 主机                             │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌─────────────────┐  │
│  │   DHT    │  │  注册表  │  │   会话   │  │    MQ (中继)    │  │
│  │ Kad-DHT  │  │ URN→Peer │  │  ECIES   │  │  存储/拉取消息  │  │
│  └──────────┘  └──────────┘  └──────────┘  └─────────────────┘  │
│                                                                   │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌─────────────────┐  │
│  │  密码学  │  │ 身份密钥 │  │  协议帧  │  │    双棘轮会话   │  │
│  │ Ed25519  │  │  (文件)  │  │  .proto  │  │     管理器      │  │
│  │  X25519  │  │          │  │          │  │                 │  │
│  └──────────┘  └──────────┘  └──────────┘  └─────────────────┘  │
└─────────────────────────────────────────────────────────────────┘
```

---

## Phase 1 — 传输层 (Transport Layer)

**完成。** 采用 libp2p v0.36+，集成了：

- TCP + QUIC 双协议监听
- Relay v2（内网 NAT 穿透连接中继）
- AutoNAT（网络类型自适应探测）
- gossipsub（发布订阅协议，用于未来多路广播扩展）

---

## Phase 2 — 身份与加密会话 (Identity & Encrypted Sessions)

### 节点身份 (Identity)

每个节点拥有一对持久化于磁盘的 `IdentityKeys`（身份密钥）：
- **Ed25519**：用于构建 `libp2p.Identity()` 并导出稳定的 PeerID。它也是 URN 计算的唯一源。
- **X25519**：用于 ECIES 加密密钥协商。与 Ed25519 身份密钥分离，以实现密钥角色的安全隔离。

URN 格式定义：`urn:agent-comm:agent:<base58(随机16字节公钥哈希)>`

URN 从 Ed25519 公钥直接派生，具备自证明属性（Self-Certifying）。

### 寻址目录服务 (Registry)

基于 libp2p 协议协商实现 URN → PeerID、物理地址（Addrs）、以及静态 X25519 公钥（X25519PubKey）的解析。

**协商协议 ID：** `/hermes/agent-comm/registry/1.0.0`

```
客户端 (Client)                       服务端 (Server)
   │── URNRegistryRequest(register) ──→  │
   │←─ URNRegistryResponse(ok) ─────────  │

客户端 (Client)                       服务端 (Server)
   │── URNRegistryRequest(resolve) ──→   │
   │←─ URNRegistryResponse(found) ───────  │
```

### 加密临时会话 (Encrypted Session)

**协商协议 ID：** `/hermes/agent-comm/session/1.0.0`

基于 Stream 的双向请求/响应流，在网络层使用临时加密信封 `EncryptedEnvelope`：

```
发送方 (Sender)                          接收方 (Recipient)
  │─ ECDH(sender_SK, recipient_PK) ──────→│
  │─ HKDF → ephemeral + encKey              │
  │─ AES-GCM(plaintext, AAD=ProtoAAD)      │
  │─ EncryptedEnvelope{wire} ─────────────→│
  │                                        │─ ECDH(recipient_SK, sender_PK)
  │                                        │─ 重新推导 ephemeral + encKey
  │                                        │─ AES-GCM 解密
  │←─ EncryptedEnvelope{reply} ←────────── │
  │─ 解密回复内容                          │
```

**信封 (Envelope) 核心字段：**
- `sender_urn` — 发送方的 URN 身份标识
- `sender_static_pubkey` — 发送方静态 X25519 公钥（用作 ECDH 双向回复协商）
- `ephemeral_pubkey` — HKDF 派生出的 32 字节临时公钥
- `nonce` — 随机 12 字节 AES-GCM nonce
- `ciphertext` — 密文载荷
- `tag` — GCM 认证标签（16 字节）
- `message_id` — 唯一消息 ID，用于去重

**附加认证数据 (AAD)：** `SHA256("agent-comm-v1")` — 协议约定的全局静态常量。

**核心实现：** `session/session.go` — `Manager` 结构体
- `SendMessage()` — 建立连接，发送信封并同步阻塞等待对端响应
- `SendReply()` — 在现有 stream 链路中发送回传的加密信封
- `BuildEnvelope()` — 打包并输出加密信封（用于离线盲存）
- `DecryptEnvelope()` — 接收并解密传入的信封

---

## Phase 3 — 异步消息队列 (Async Message Queue / MQ)

### 问题背景
若接收方节点断网或关机，点对点直连将失效，需要提供安全的异步离线存储中转。

### 核心设计
网络中的 **Relay 节点**（通常由 Bootstrap 节点担任）充当信箱。它们负责接收并持久化发往离线收件人的 `EncryptedEnvelope` 信封。由于消息已执行端到端加密，中继节点无法读取信封内容（Relay 盲存）。

### 体系结构
```
发送方 ──→ 中继服务器 (Store) ──→ 接收方 (上线后拉取)
             └── SQLite: 接收方URN → [信封密文, 消息ID, 过期时间]

接收方 ──→ 中继服务器 (Pull)  ──→ 获取积压的离线消息列表
接收方 ──→ 中继服务器 (Ack)   ──→ 确认已读取消息，物理抹除记录
```

### 协议规约：`/hermes/agent-comm/mq/1.0.0`

**数据契约：** `proto/mq.proto`

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
  EncryptedEnvelope payload = 2;  // 密文信封（中继无法查看）
  int64 expiry_unix = 3;          // TTL 过期时间戳（0 = 永不过期）
}

message RetrieveRequest {
  string recipient_urn = 1;
}

message AckRequest {
  repeated string message_ids = 1;  // 客户端消费完毕后发起删除
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
  string message_id = 2;  // 由 Relay 分配
}

message RetrieveResponse {
  repeated EncryptedEnvelope payloads = 1;  // 按时间先后排序
}

message AckResponse {
  bool ok = 1;
  int32 deleted_count = 2;
}

message ErrorResponse {
  string message = 1;
}
```

### 存储机制 (Relay 侧)

**实现：** `mq/server.go` — `Server` 结构体，SQLite 持久化引擎。

```sql
CREATE TABLE messages (
  id         TEXT PRIMARY KEY,
  recipient  TEXT NOT NULL,          -- 接收方 URN
  payload    BLOB NOT NULL,           -- EncryptedEnvelope 二进制字节
  expiry     INTEGER NOT NULL,        -- Unix 时间戳
  stored_at  INTEGER NOT NULL         -- 存储时的 Unix 时间戳
);
CREATE INDEX idx_recipient ON messages(recipient);
CREATE INDEX idx_expiry ON messages(expiry);
```

中继服务器在初始化及之后每隔 5 分钟在后台定期执行物理删除以清除过期信封。

### 客户端路由行为

**实现：** `mq/client.go` — `Client` 结构体。

- `Store(ctx, relay, recipientURN, envelope, ttlDays)` — 投递信封到中继
- `Retrieve(ctx, relay, myURN)` — 获取我未读的离线信封列表
- `Ack(ctx, relay, messageIDs)` — 发送确认，从数据库中抹除

**投递流程：**
1. 并发寻址获取接收方网络信息与公钥。
2. 尝试直接拨号并建立双棘轮会话（`SendMessage`）。
3. 直连成功完成通信。
4. 直连超时或网络不可达时降级为 MQ 存储：
   a. 获取平台中继节点（默认为公共引导节点 URN）。
   b. 发送带有加密载荷的 `MQStoreRequest`。
   c. 中继返回该信封在服务器的 `message_id`。

**消费流程：**
1. 启动或周期性地向中继发送 `MQRetrieveRequest` 抓取密文。
2. 在本地调用 Session 模块尝试解密每一个信封。
3. 对解密成功且成功回调的数据发送 `MQAckRequest` 指令进行彻底销毁。

---

## Phase 4a — 信任网络 (Web of Trust / WoT)

### 待解决问题
当 Bob 首次联系 Alice 时，如何保证 `urn:agent-comm:agent:Alice` 所指向的公钥真正属于 Alice 而不是中间人冒充的？Registry 寻址服务只提供物理寻址，无法确保证明其真实所有权。

### 解决方案
对等节点签署信任声明。例如 Charlie 声明：“我信任 `urn:agent-comm:agent:Alice` (公钥=X，节点ID=12D3...)”。若 Bob 已经信任了 Charlie，那么 Bob 可以通过图的遍历推导出对 Alice 的传递信任。

### 信任声明数据结构

**协议帧：** `proto/wot.proto`

```proto
enum TrustLevel {
  UNKNOWN = 0;
  TRUSTED = 1;
  UNTRUSTED = 2;
}

message TrustClaim {
  string issuer_urn = 1;          // 签署方 URN (如 Charlie)
  string subject_urn = 2;          // 被声明方 URN (如 Alice)
  string subject_peer_id = 3;      // Subject 的 libp2p 节点 ID
  bytes  subject_x25519_pk = 4;   // Subject 静态公钥
  TrustLevel level = 5;            // 信任关系评级 (TRUSTED / UNTRUSTED / UNKNOWN)
  bytes  issuer_signature = 6;     // 签署方的 Ed25519 签名
  int64  issued_at_unix = 7;       // 签署时间戳
}
```

**签名校验散列：** `SHA256(issuer_urn || subject_urn || subject_peer_id || subject_x25519_pk || level || issued_at)`

---

## Phase 4b — 双棘轮会话 (Double Ratchet / DR)

### 待解决问题
一次性 ECIES 依赖静态 X25519 私钥的长期安全。如果某一端点的静态私钥泄露，历史上的所有密文都将能够被被解密（静态 ECDH 缺乏前向安全性）。

### 解决方案
引入双棘轮算法（Double Ratchet / Signal 协议）。每次发送/接收时都会在状态机内自动演进密钥。一次密钥泄露仅暴露极其局限的消息窗口。

### 代码包设计
```
dr/
├── ratchet.go   # 核心演进：RootKey、ChainKey 派生与 DH 棘轮步进
├── session.go   # DRSession 会话层：融合 ratchet 演进，结合 ECIES 握手做初始密钥商定
└── store.go     # SQLite 状态持久化：记录对等 URN 的最新棘轮演进进度
```

### 密钥派生链 (KDF Chain)

```
初创期：ECDH(我们的临时DH_SK, 对端的静态X25519_PK) → SKR (共享机密)
SKR → HKDF 派生 → 根密钥 (Root Key) + 发送/接收链密钥 0 (Chain Key 0)

发送阶段 (Message Number = 0)：
  发送链密钥 0 → HKDF 派生 → 消息加密密钥 0 (Message Key 0) + 下一级链密钥 1
  用 Message Key 0 加密明文主体 → 输出密文载荷

DH棘轮更新阶段 (收到对端带回的新临时 DH 密钥后)：
  ECDH(本端新临时DH_SK, 对端新临时DH_PK) → 新共享机密 SKR2
  SKR2 → HKDF 派生 → 新根密钥 2 + 新接收链密钥 2
```

---

## Phase 5 — 持久化层 (SQLite DR Session Store)

**完成。** `dr/store.go` 实现了对有状态双棘轮会话进度（`RatchetState`）的本地 SQLite 高速读写。

### 存储结构
```sql
CREATE TABLE dr_sessions (
    peer_urn    TEXT PRIMARY KEY,   -- 目标的 URN
    state       BLOB NOT NULL,      -- 序列化后的 RatchetState 棘轮状态实体
    updated_at  INTEGER NOT NULL    -- 更新时的 Unix 时间戳
);
```

---

## Phase 6 — 网络加密传输 (E2E DR over libp2p)

**完成。** `cmd/test_dr_net/main.go` 跑通了真实网络拓扑下的端到端双棘轮对称握手与消息交换。

- **Simplex（单工模式）**：每一条发送的消息都通过一个独立的、短暂生存的 libp2p 协议 Stream 传递，接收方处理完后写入 EOF。
- **带内密钥自动构建**：利用 `session.Manager` 本地内存缓存对端的 X25519 PK，从而在 Responder 接收到握手头信息时，无缝匹配正确的密钥以初始化棘轮。