# agent-comm 教程

> 上手指南、背景知识、代码导读 —— 面向有计算机基础的本科生

---

## 文档导航

```
OVERVIEW.md      → 问题与思路（先读这个）
SPEC.md          → 各 Phase 的技术规格
README.md        → 快速参考（测试命令、目录结构）
SKILL.md         → agent 专用快速参考
──────────────────────────────────────────
docs/TUTORIAL.md ← 你在这里：背景知识 + 上手指南 + Phase 详解
docs/DR-CODE-COMMENTARY.md → DR 代码逐文件注解
```

---

## 1. 导言

这个项目解决一个问题：**两个在不同机器上的 AI agent，如何在没有中心化服务器的前提下，安全地找到对方并加密通信？**

这不是"加密传输"的问题——TLS 可以加密，但依赖 CA，服务器能看到明文。这也不是"用 Signal"的问题——Signal 需要手机号，身份是中心化的。

agent-comm 的目标：
- **没有中心化的消息路由服务器**（用 DHT 分布式查找）
- **没有中心化的消息存储服务器**（用加密 blob + relay）
- **没有中心化的身份系统**（用自证明身份：公钥即身份）
- **端到端加密**，Relay 只能存密文，不知道内容
- **前向保密**（Double Ratchet）

---

## 2. 背景知识

### 2.1 libp2p 是什么

libp2p 是一个 P2P 网络库，最初为 IPFS 项目开发，现在独立通用。它解决的问题是：**进程之间如何通过 IP 网络建立连接并通信**，包括：

- **节点发现**：通过 DHT 找到其他节点的地址
- **NAT 穿透**：两个节点都在私有网络里如何建立直接连接（Relay v2）
- **协议协商**：两个节点如何协商使用哪个加密/路由协议
- **多传输协议**：同时支持 TCP、QUIC、WebRTC 等

在 agent-comm 中，libp2p 提供了底层的"打电话"能力——两个节点通过它建立点对点连接，在此之上我们跑自己的协议。

### 2.2 DHT（分布式哈希表）

DHT 是一个分布式数据库，把 (key, value) 对存在很多节点上，查找时通过分布式算法找到持有对应 key 的节点。

Kademlia 是最常见的 DHT 实现，核心思想是：**节点 ID 和数据的 key 用同样的格式，距离远的节点更可能持有这个 key**。查找时每次迭代都找更近的节点，直到找到。

在 agent-comm 中，DHT 存储 `URN → PeerID + 地址` 的映射。URN 是身份，PeerID 是 libp2p 网络地址。没有中心化的"通讯录服务器"，每个节点都帮忙存一部分。

### 2.3 X25519/ECIES/公钥加密

**X25519**：Curve25519 椭圆曲线 Diffie-Hellman（ECDH）密钥交换。两个人各有一个私钥（随机数）和公钥（私钥 × 基点）。双方用对方的公钥和自己的私钥算出同一个共享secret——即使第三方截获了所有公钥，也无法算出这个共享 secret。

**ECIES**（Elliptic Curve Integrated Encryption Scheme）：在 X25519 共享secret 的基础上，再做一层密钥派生和对称加密：
1. ECDH → shared_secret
2. HKDF（RFC 5869）：从 shared_secret 派生出一个对称密钥 encKey
3. AES-GCM：用 encKey 加密消息，同时提供认证（防篡改）

### 2.4 HKDF（密钥派生函数）

HKDF 是一个从"原始密钥材料"派生出多个"安全密钥"的函数，防止密钥被重复使用导致泄露。

HKDF-SHA256(input, info) → 固定长度的伪随机密钥。"info"参数用来区分不同的派生目的——同样的 input 派生不同的 key 时用不同的 info。

### 2.5 AES-GCM-SIV（认证加密）

AES-GCM 是一个 AEAD（Authenticated Encryption with Associated Data）算法：
- 加密：给定密钥 + 明文 + nonce → 密文
- 认证：给定密文 + nonce + tag，验证没有被篡改
- AAD（Additional Authenticated Data）：不需要加密，但要一起认证的数据

"GCM-SIV"是 GCM 的一个变种，对 nonce-misuse 更容忍。

### 2.6 Double Ratchet（前向保密）

**问题**：ECIES 用了临时密钥对，但这个临时密钥是"每次会话"新建，不是"每条消息"。如果攻击者在某次会话后偷到了你的私钥，所有会话都能被解密。

**解决方案**：Signal Protocol（Double Ratchet）。核心思想：
- 每条消息从一个 chain key 派生 message key
- 发完一条消息后，chain key 就被删除（ratchet step）
- 同时定期做 DH ratchet step，换新的 DH 密钥对
- 这样即使泄露一条消息的密钥，其他消息的密钥依然安全

### 2.7 URN（自证明身份）

URN（Uniform Resource Name）格式：`urn:hermes:agent:<base58(SHA256(pubkey)[:16])>`

原理：Ed25519 公钥的 SHA256 哈希前 16 字节用 base58 编码，就是你的 URN。知道 URN 就等于知道公钥（单向函数），用对应私钥签名就能证明身份。没有 CA，没有手机号，公钥即身份。

---

## 3. 项目架构

```
┌──────────────────────────────────────────────────────────────┐
│  Phase 1: libp2p 传输层                                        │
│  TCP/QUIC + Relay v2（NAT 穿透）+ AutoNAT                     │
├──────────────────────────────────────────────────────────────┤
│  Phase 2: 身份与注册层                                         │
│  Ed25519 身份密钥 → URN (自证明)                              │
│  DHT 存储 URN → PeerID + X25519PK                            │
│  Registry 协议：/hermes/agent-comm/registry/1.0.0            │
├──────────────────────────────────────────────────────────────┤
│  Phase 2: 加密会话层                                          │
│  X25519 ECDH + HKDF + AES-GCM-SIV → ECIES                    │
│  Session 协议：/hermes/agent-comm/session/1.0.0              │
├──────────────────────────────────────────────────────────────┤
│  Phase 4b: Double Ratchet（替代 ECIES 做消息加密）            │
│  DR 协议：/agent/dr/1.0.0                                    │
│  前向保密：每条消息用不同的 message key                       │
├──────────────────────────────────────────────────────────────┤
│  Phase 3: 离线存储层                                          │
│  Relay 节点存储加密 blob，接收方上线后拉取                    │
│  MQ 协议：/hermes/agent-comm/mq/1.0.0                        │
├──────────────────────────────────────────────────────────────┤
│  Phase 5: 存储层（Double Ratchet 状态持久化）                  │
│  SQLite：每个 peer 一个 RatchetState                         │
└──────────────────────────────────────────────────────────────┘
```

---

## 4. Phase 详解

### Phase 1: 传输层（libp2p Host）

**目的**：让两个节点能互相发现并建立网络连接。

**做了什么**：
- `libp2p.NewHost(cfg)` 创建一个 libp2p 节点
- 同时监听 TCP 和 QUIC
- 开启 Relay v2：让两个都在 NAT 后面的节点能通过 relay 中继建立连接
- 开启 AutoNAT：让节点能判断自己是否可达

**核心文件**：`libp2p/host.go`

### Phase 2: 身份与注册（DHT + Registry）

**身份密钥**（`crypto/keys.go`）：
- Ed25519 密钥对：用于签名、PeerID 派生、URN 生成
- X25519 密钥对：用于 ECIES 加密
- 两种密钥分开：身份密钥几乎不用，ECIES 密钥频繁使用，分开减少暴露风险

**URN 系统**：`urn:hermes:agent:<base58>`，从 Ed25519 公钥派生，自证明。

**Registry 协议**（`registry/client.go`、`registry/server.go`）：
- 注册：`URNRegistryRequest{register, urn, peer_id, addrs, x25519_pk}` → `URNRegistryResponse{ok}`
- 解析：`URNRegistryRequest{resolve, urn}` → `URNRegistryResponse{peer_id, addrs, x25519_pk}`

**DHT**（`dht/dht.go`）：只负责节点发现（peer routing），URN 到地址的映射由 Registry 协议处理。

### Phase 3: 异步消息队列（Relay）

**场景**：B 给 A 发消息，但 A 不在线。

**解决方案**：B 把加密消息存到 relay 节点，A 之后上线去拉取。

**为什么 relay 看不到内容**：B 在发往 relay 之前就已经用 ECIES 加密了，relay 只存加密 blob。

**三个操作**：
1. `Store`：B 把加密 envelope 发给 relay，relay 存入 SQLite
2. `Retrieve`：A 上线后从 relay 拉取所有 pending 消息
3. `Ack`：A 解密处理完后，告诉 relay 删除这些消息

### Phase 4b: Double Ratchet

**核心数据结构** `RatchetState`（`dr/ratchet.go`）：
- `rootKey`：根密钥，用于派生 chain key
- `sendChainKey` / `recvChainKey`：发送/接收链密钥
- `DHKeyPair`：当前节点的 DH 密钥对（每轮 ratchet 换新）
- `remoteDHPK`：对方最新的 DH 公钥
- `sendMsgNum` / `recvMsgNum`：消息编号

**消息密钥派生**：
```
chain_key → HKDF("DoubleRatchetMessage") → (message_key, next_chain_key)
```

**DH Ratchet Step**（每收到对方的新 DH 公钥时）：
```
new_DH_KEY = generate_fresh_keypair()
shared_secret = ECDH(new_DH_SK, remote_DH_PK)
root_key, chain_key = HKDF(shared_secret, "DoubleRatchet")
```

**为什么有前向保密**：发完一条消息后，message key 从 chain key 派生，然后 chain key 就更新。泄露 message key 只能解密那一条；旧的消息密钥早已删除。

### Phase 5: 存储层（SQLite 持久化）

**为什么需要**：`RatchetState` 是状态机，节点重启后状态丢失，重启后无法继续和对方的 ratchet 通信。所有 ratchet 状态必须持久化。

**存储内容**：`dr/store.go` —— 每个 peer 的 URN 对应一条序列化后的 `RatchetState`。

**序列化方法**：`dr/ratchet.go` 暴露了 `SerializeRatchetState()` / `DeserializeRatchetState()` 处理未导出字段。

### Phase 6: 网络传输（E2E DR over libp2p）

**怎么做**：在 libp2p stream 上跑 Double Ratchet 协议。
- A 打开到 B 的 stream：`<长度 prefix><DR 密文>`
- B 的 stream handler 收到后，用 responder session 解密
-  simplex 模式：每条消息一个独立的 stream，不需要回复

---

## 5. 上手指南

### 编译与运行

```bash
cd ~/.hermes/agent-comm

# 编译所有模块
~/.local/go/bin/go build ./...

# 运行各个 Phase 测试
~/.local/go/bin/go run ./cmd/test_host/        # Phase 1
~/.local/go/bin/go run ./cmd/test_session/     # Phase 2
~/.local/go/bin/go run ./cmd/test_mq/          # Phase 3
~/.local/go/bin/go run ./cmd/test_dr/          # Phase 4b
~/.local/go/bin/go run ./cmd/test_dr_persist/  # Phase 5
~/.local/go/bin/go run ./cmd/test_dr_net/      # Phase 6
```

### 目录结构

```
agent-comm/
├── crypto/          # Ed25519/X25519 密钥 + ECIES 加密
├── libp2p/          # libp2p.Host 构造
├── dht/             # Kad-DHT 封装
├── registry/        # URN 注册/解析（client + server handler）
├── session/         # ECIES 会话管理
├── mq/              # 异步消息队列（client + relay server）
├── dr/              # Double Ratchet（ratchet + session + store）
├── wot/             # Web of Trust（⚠️ 未集成）
├── proto/           # Protobuf 定义
├── contacts/        # 身份密钥（自动生成）
└── cmd/
    ├── bootstrap/  # Bootstrap 节点（DHT Server）
    ├── client/      # 客户端节点
    └── test_*/     # 各 Phase 验证测试
```

### Go 依赖版本（已验证）

```
Go: 1.25.10
go-libp2p: v0.36.3
go-libp2p-core: v0.20.1（用 core/ 子路径 import）
go-libp2p-kad-dht: v0.39.2
quic-go: v0.45.2
```

---

## 6. 代码导读

### 核心文件一览

| 文件 | 作用 |
|------|------|
| `crypto/keys.go` | Ed25519/X25519 密钥加载、URN 派生 |
| `crypto/ecies.go` | ECIES 加密/解密、共享 secret 计算 |
| `libp2p/host.go` | libp2p 主机创建（TCP+QUIC+Relay+AutoNAT） |
| `dht/dht.go` | Kad-DHT 封装（节点发现、路由表） |
| `registry/client.go` | URN 解析/注册（通过网络协议） |
| `registry/server.go` | URN 注册服务器 handler |
| `session/session.go` | ECIES 会话管理（SendMessage、BuildEnvelope） |
| `mq/client.go` | MQ Store/Retrieve/Ack 客户端 |
| `mq/server.go` | Relay 服务器（SQLite 存储） |
| `dr/ratchet.go` | Double Ratchet 核心（RatchetState + 密钥派生） |
| `dr/session.go` | DRSession（Initiator/Responder） |
| `dr/store.go` | SQLite 持久化存储 |

### 调用关系

```
发消息流程：
  session.Manager.SendMessage()
    → crypto.Ecies.Encrypt()  ← ECIES 加密
    → registry.Resolve()      ← 查对方 URN → PeerID
    → libp2p stream.Write()   ← 发送加密信封

收消息流程：
  session.Manager.DecryptEnvelope()
    → crypto.Ecies.Decrypt()  ← ECIES 解密

Double Ratchet 替代 ECIES：
  dr.DRSession.SendMessage()   ← DR 加密（Phase 4b）
  dr.DRSession.Receive()      ← DR 解密

离线消息：
  mq.Client.Store()            ← 发到 relay
  mq.Client.Retrieve()         ← 从 relay 拉取
  mq.Client.Ack()             ← 确认删除
```

---

## 7. 常见问题

**Q: 为什么有两个密钥对（Ed25519 和 X25519）？**
Ed25519 用于签名/身份，X25519 用于加密。分开使用意味着签名私钥几乎不触网，泄露风险更低。

**Q: PeerID 和 URN 有什么区别？**
PeerID 是 libp2p 内部的网络标识符（从 Ed25519 公钥派生）。URN 是人类可读的身份标识（从 Ed25519 公钥的哈希派生）。两者都基于同一个 Ed25519 密钥对。

**Q: relay 能看到消息内容吗？**
不能。消息在发给 relay 之前就已经加密了，relay 只知道"某人的加密 blob 要给某人"，不知道内容。

**Q: Double Ratchet 为什么要比 ECIES 复杂这么多？**
ECIES 的前向保密是"每次会话"换密钥，Double Ratchet 是"每条消息"换密钥。代价是状态机变得更复杂，但安全性高得多。

**Q: 重启后 DR 会话怎么恢复？**
Phase 5 的 `dr/store.go` 把每个 peer 的 `RatchetState` 存到 SQLite，重启后 `LoadSession()` 恢复，继续消息流。