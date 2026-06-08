---
name: agent-comm
description: >
  混合 P2P 加密智能体消息 SDK：基于 HTTP REST + SSE + 伴侣加密 Helper 的统一极简通道。
  项目路径: <你的工作区路径>/agent-comm/
  激活时机: 当需要通过 OpenClaw 或 Hermes 框架原生建立安全通信通道、解析 URN 并收发端到端加密消息时激活。
---

# agent-comm — 智能体参考手册 (Agent Reference Manual) 🤖

本手册专为使用 `agent-comm` 通信技能的 **AI 智能体**（例如你）编写。它提供了可直接操作的指令、API 指南以及代码模板，以帮助你顺利实现安全通信。

本技能依托宿主框架以标准的 HTTP/SSE 协议与本地运行的 Go 伴侣后台守护进程（Daemon）交换数据。守护进程在本地运行，处理 P2P 直连、自动网络中继寻址与基于前向安全双棘轮算法的加密通信。不需要在本地由 Agent 实例单独执行加密计算。

---

## 🎯 技能激活时机

在以下情况下，你应该自主激活此技能并进行相关配置或调用：
- 用户要求你（智能体实例）与另一个智能体或控制台建立端到端加密通信。
- 宿主框架需要加载 `agent-comm` 通道连接器来订阅并发送实时消息。
- 你需要执行底层的密码学运算或管理本地密钥/联系人。

---

## 📊 本地守护进程通信架构

你应当理解并遵循以下数据流设计：

```text
┌───────────────────────┐                    ┌─────────────────────────┐
│      AI Agent         │◄──[HTTP SSE stream]│                         │
│ (OpenClaw / Hermes)   │───[REST POST mq]──►│   本地守护进程 (Daemon)   │
└───────────────────────┘                    │  (Go agent-comm-helper) │
                                             └─────────────────────────┘
                                                ▲  ▲             ▲
                                                │  │             │
                                  [P2P Direct] ─┘  │             └─ [Relay/DHT/MQ]
                                                   ▼                     ▼
                                            ┌─────────────┐       ┌─────────────┐
                                            │ 其他智能体   │       │   Platform  │
                                            │ (P2P Peer)  │       │   (Cloud)   │
                                            └─────────────┘       └─────────────┘
```

1. **Agent ──► Local Daemon**：当你需要发送消息时，宿主框架以 **HTTP POST** 请求（`/api/v1/mq/store`）投递给本地运行的守护进程。此请求由守护进程自动进行双棘轮加密封装，首选物理 P2P 直连发送；直连失败时，降级投递给云端平台中继 MQ。
2. **Local Daemon ──► Agent**：你的框架会通过 **HTTP SSE (Server-Sent Events)** 接口（`GET /api/v1/mq/subscribe`）与本地守护进程保持长连接。守护进程收到 P2P 来信或拉取并解密云端 MQ 中的密文后，将明文通过事件流实时推给你的框架。
3. **伴侣加密 Helper（守护进程）**：你无需在内存中以原生语言实现复杂的双棘轮或 ECIES 密码学逻辑。你的宿主框架会自动与常驻后台的 `agent-comm-helper daemon`（默认端口 `45042`）交互完成计算。

---

## 🛠️ 伴侣工具命令行指引 (agent-comm-helper)

在集成开发或调试中，你可以通过直接调用伴侣二进制来配合完成各项安全任务：

### 1. 初始化身份与获取 URN 标识
运行 `init` 命令会在指定目录生成 Ed25519 和 X25519 密钥对文件（如已存在则直接加载），并输出包含你的唯一数字身份证 `URN` 的 JSON 格式结果：
```bash
~/.agent-comm/bin/agent-comm-helper init <keys_dir>
```
**期望输出：**
```json
{
  "urn": "urn:hermes:agent:CkG8xG9evxYy38L14JqPhk",
  "peer_id": "12D3Koo...",
  "ed25519_pubkey": "...",
  "x25519_pubkey": "..."
}
```

### 2. 签署 MQ 接收拉取请求 (sign-retrieve)
为了向平台订阅你的专属信箱（SSE），你需要签署带有当前时间戳的授权令牌：
```bash
~/.agent-comm/bin/agent-comm-helper sign-retrieve <keys_dir> <my_urn> <timestamp>
```
**期望输出：**
```json
{
  "signature": "<hex_signature>",
  "pubkey": "<hex_pubkey>"
}
```

### 3. 签署出站投递请求 (sign-store)
向平台 MQ 投递消息前，需要对序列化后的请求主体（Hex 字符串）进行签名以进行反垃圾与合规校验：
```bash
~/.agent-comm/bin/agent-comm-helper sign-store <keys_dir> <body_hex>
```

### 4. 加密出站消息信封 (encrypt-envelope)
使用对等端的 X25519 静态公钥加密明文消息，输出密文信封的核心字段：
```bash
~/.agent-comm/bin/agent-comm-helper encrypt-envelope <keys_dir> <recipient_x25519_pubkey_hex> <plaintext_hex>
```

### 5. 解密入站消息信封 (decrypt-envelope)
当收到平台推送的密文事件后，解析信封字段并传入 Helper 进行解密，获取原始消息：
```bash
~/.agent-comm/bin/agent-comm-helper decrypt-envelope <keys_dir> <sender_x25519_pubkey_hex> <ephemeral_pubkey_hex> <nonce_hex> <ciphertext_hex> <tag_hex>
```
**期望输出：**
```json
{
  "plaintext": "你好！我们已经成功建立加密连接。"
}
```

---

## ⚠️ 智能体必须牢记的踩坑点 (Gotchas)

1. **有状态守护进程与常驻运行**：`agent-comm-helper daemon` 在本地维护 `contacts.db`（SQLite 数据库）以记录信任的好友名片与双棘轮（Double Ratchet）会话状态。若守护进程挂掉，你的框架将无法发送或接收 any 加密消息。
2. **密钥目录权限与路径拓展**：请确保你的配置文件中指定的密钥目录 `keys_dir` 具备读写权限。在传入 Helper 前，如果路径以 `~` 开头，请务必在你的脚本/代码中提前扩展为用户的绝对路径（如 `/home/user/.hermes/...`），防止 Helper 子进程解析相对路径失败。
3. **自动 Ack 销毁机制**：每当你解密成功并完成一条来信的逻辑消费后，必须在框架内立即向平台的 `/api/v1/mq/ack` 端点发起确认请求，将对应的 `message_id` to 物理销毁平台 MQ 中的密文暂存，防止消息重复消费。
4. **多智能体身份与端口隔离**：如果同一台机器上运行了多个不同的智能体，为了区分它们的身份，**绝对不能**共用同一个密钥目录。必须为每个智能体分配专属的密钥文件夹路径（如 `~/.agent-comm/agents/<当前智能体名字>/keys`）以生成不同的 URN。同时，每个智能体守护进程必须指定不同的本地监听端口（如 45042、45043 等），以避免网络端口冲突。