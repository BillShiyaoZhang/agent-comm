# agent-comm

**P2P encrypted messaging for AI agents — libp2p + Double Ratchet + DHT registry.**

---

## 这个项目要解决什么问题

两个在不同机器上运行的 AI agent，如何安全地直接通信？

"安全"不是指 TLS 加密传输——TLS 依赖 CA，且中心化服务器能看到明文。这个项目要解决的是：

**在没有中心化服务器的前提下，两个 AI agent 如何发现彼此、互相验证、交换只有彼此能解密的消息？**

当前主流方案都有根本性的不匹配：

| 方案 | 问题 |
|------|------|
| 中心化服务器（Webhook / HTTP API） | 单点故障；服务器能读取消息；可被审查 |
| Signal / Matrix 等 IM 平台 | 需要手机号注册；身份是中心化的 |

agent-comm 的目标：
- **去中心化路由**：DHT 分布式查找，没有通讯录服务器
- **去中心化存储**：relay 只存加密 blob，无法读取内容
- **自证明身份**：Ed25519 公钥即身份，没有 CA
- **端到端加密**：只有收方能解密
- **前向保密**：Double Ratchet，每条消息用不同密钥

---

## 核心设计思路

```
┌─────────────────────────────────────────────────────────┐
│  问题 1: 如何找到对方？                                   │
│  答案：DHT (Kademlia) — 分布式存储 URN → PeerID 映射      │
│                                                          │
│  问题 2: 怎么证明"我是我"？                              │
│  答案：自证明身份 — URN = SHA256(pubkey)，公钥即身份      │
│                                                          │
│  问题 3: 通信内容如何只有对方能解密？                     │
│  答案：端到端加密 — ECIES (X25519 ECDH) + Double Ratchet  │
│  ECIES 建立了会话密钥，Double Ratchet 让每条消息用不同密钥 │
│                                                          │
│  问题 4: 对方不在线时消息怎么办？                        │
│  答案：relay 存加密 blob，对方上线后拉取                  │
│  relay 只知道"某人的加密信封"，不知道内容                │
└─────────────────────────────────────────────────────────┘
```

---

## 技术栈

| 层次 | 技术 | 作用 |
|------|------|------|
| 网络传输 | libp2p + Relay v2 | P2P 连接、NAT 穿透 |
| 路由 | Kad-DHT | 分布式 URN → PeerID 查找 |
| 身份 | Ed25519 + URN | 自证明身份 |
| 密钥交换 | X25519 ECDH + HKDF | ECIES 加密 |
| 消息加密 | AES-GCM-SIV + Double Ratchet | 前向保密 |
| 离线存储 | Relay + SQLite | 加密 blob 存储 |

---

## Phase 进度

| Phase | 状态 | 说明 |
|-------|------|------|
| 1 | ✅ | libp2p host + Relay v2 + AutoNAT |
| 2 | ✅ | Ed25519 身份 + DHT registry + ECIES 会话 |
| 3 | ✅ | 异步消息队列（relay 离线存储） |
| 4b | ✅ | Double Ratchet（前向保密） |
| 5 | ✅ | SQLite-backed DRSession 持久化 |
| 6 | ✅ | E2E DR over libp2p streams（双向） |
| 4a WoT | ⚠️ | `wot/` 包存在，未集成 |

---

## 文档体系

```
OVERVIEW.md      ← 概览（问题、思路、各部分协作图）
SPEC.md          ← Phase by Phase 技术规格
README.md        ← 你在这里：为什么要做 + 核心设计
SKILL.md         ← agent 调用参考（gotchas、API、设计决策）
──────────────────────────────────────────────────
docs/
  TUTORIAL.md    ← CS 本科生教程：背景知识 + Phase 详解
  DR-CODE-COMMENTARY.md ← DR 代码逐文件注解
```

---

## Hybrid P2P 架构与高层 SDK 封装

本项目在底层 P2P (DHT + Double Ratchet) 跑通的基础上，新增了**客户端 SDK (Agent Comm Skill)** 的高级封装（位于 `agent/` 目录），并设计了**Hybrid P2P (混合降级通讯)** 架构：

- **自适应网络降级**：通过多路竞速发现（并发查询 Kademlia DHT 及中心 Registry），并按照阶梯退化策略投递消息（① TCP/QUIC 直连拨号 -> ② Relay 中继打洞 -> ③ 离线加密信封盲投 MQ）。
- **SDK Wrapper (`agent.Agent`)**：对外抹平了底层密码学与连接逻辑的复杂性，仅暴露了极其友好的业务级 API。
- **Platform (另立项目)**：包括中心化 Registry 寻址、MQ 高性能大并发盲存集群、离线代持合规网关，作为该去中心化项目的全天候兜底“信箱云”。

### 极速上手 Demo

只需创建一个 Config 并调用三行核心 API 即可拉起包含双棘轮状态引擎的 AI Agent 端点：

```go
// 1. 一键读取身份并开启混合 P2P 网络 (挂载 SQLite DR持久化)
a, _ := agent.InitIdentity(ctx, agent.Config{
    KeysDir: "./demo_keys",
    DBPath: "./demo_dr.db",
    // BootstrapNodes...
})

// 2. 异步接收打洞与离线缓存 MQ 的消息
a.OnMessage(ctx, func(senderURN string, msg string) {
    fmt.Printf("<<< Received from %s: %s\n", senderURN, msg)
})

// 3. 多路寻址竞速发送（支持离线兜底 Double Ratchet 盲存）
a.SendMessage(ctx, targetURN, "Hello!")
```

详见 `cmd/agent_demo/main.go`。

## 测试命令

```bash
cd ~/.hermes/agent-comm

~/.local/go/bin/go run ./cmd/test_host/        # Phase 1 — libp2p host
~/.local/go/bin/go run ./cmd/test_session/     # Phase 2 — ECIES 会话
~/.local/go/bin/go run ./cmd/test_mq/          # Phase 3 — 离线 MQ
~/.local/go/bin/go run ./cmd/test_dr/         # Phase 4b — DR 握手
~/.local/go/bin/go run ./cmd/test_dr_persist/  # Phase 5 — DR 持久化
~/.local/go/bin/go run ./cmd/test_dr_net/      # Phase 6 — 双向 DR over libp2p
```

**Go binary:** `~/.local/go/bin/go` (Go 1.25.10)

---

## 项目结构

```
agent-comm/
├── crypto/          # Ed25519/X25519 密钥 + ECIES
├── libp2p/          # libp2p.Host 构造
├── dht/             # Kad-DHT 封装
├── registry/        # URN 注册/解析（client + server handler）
├── session/         # ECIES 会话管理
├── mq/              # 异步消息队列（client + relay server）
├── dr/              # Double Ratchet（ratchet + session + store）
├── wot/             # Web of Trust ⚠️ partial
├── proto/           # Protobuf 定义
├── contacts/        # 身份密钥（自动生成）
└── cmd/
    ├── bootstrap/  # Bootstrap 节点（DHT Server）
    ├── client/     # 客户端节点
    └── test_*/     # 各 Phase 验证测试
```

---

## 安全属性

- **Ed25519 签名**：自证明 URN，私钥几乎不触网
- **X25519 加密密钥**：与身份密钥分离，减少暴露面
- **Double Ratchet**：泄露一条消息密钥不影响其他（前向保密）
- **Relay blind storage**：relay 只存加密 blob，无法读取内容
- **无 WoT 时**：首次联系依赖 registry，需防范 MITM
