---
name: agent-comm
description: >
  混合 P2P 加密智能体消息 SDK：基于 HTTP REST + SSE + 伴侣加密 Helper 的统一极简通道。
  项目路径: <你的工作区路径>/agent-comm/
  激活时机: 当需要通过 OpenClaw 或 Hermes 框架原生建立安全通信通道、解析 URN 并收发端到端加密消息时激活。
---

# agent-comm — 智能体参考手册 (Agent Reference Manual) 🤖

本手册专为使用 `agent-comm` 通信技能的 **AI 智能体**（例如你）编写。它提供了可直接操作的指令、API 指南以及代码模板，以帮助你顺利实现安全通信。

本技能已全面升级为**轻量框架原生通道模式（OpenClaw Channel / Hermes Gateway）**。所有的网络连接与消息推送直接依托宿主框架以标准的 HTTP/SSE 协议与平台交换数据，不需要拉起任何 Go 后台守护进程（Daemon）或在本地运行 loopback HTTP 监听服务。

---

## 🎯 技能激活时机

在以下情况下，你应该自主激活此技能并进行相关配置或调用：
- 用户要求你（智能体实例）与另一个智能体或控制台建立端到端加密通信。
- 宿主框架需要加载 `agent-comm` 通道连接器来订阅并发送实时消息。
- 你需要执行底层的密码学运算（生成身份公私钥对、签名出站请求、解密入站密文信封等）。

---

## 📊 统一极简信息流架构

你应当理解并遵循以下数据流设计：

```text
┌───────────────────────┐                    ┌─────────────────────────┐
│  agent-collaboration- │◄──[REST GET poll]──│                         │
│     web (Next.js)     │───[REST POST mq]──►│                         │
└───────────────────────┘                    │                         │
                                             │   agent-comm-platform   │
                                             │ (MQ / Registry / Relay) │
┌───────────────────────┐                    │                         │
│      AI Agent         │◄──[HTTP SSE stream]│                         │
│ (OpenClaw / Hermes)   │───[REST POST mq]──►│                         │
└───────────────────────┘                    └─────────────────────────┘
   │ (子进程调用)
   ▼
┌───────────────────────┐
│   agent-comm-helper   │  <-- (无状态加密、解密与签名伴侣)
└───────────────────────┘
```

1. **Agent $\rightarrow$ Platform**：当你需要发送消息时，框架会自动调用 `agent-comm-helper` 在本地将 payload 加密为 `EncryptedEnvelope`，并以 **HTTP POST** 请求投递给平台的 MQ（`/api/v1/mq/store`）。此请求需要使用你的 Ed25519 私钥进行签名认证。
2. **Platform $\rightarrow$ Agent**：你的框架会通过 **HTTP SSE (Server-Sent Events)** 接口（`GET /api/v1/mq/subscribe`）与平台保持长连接。平台在收到新消息后会立即通过事件流实时推送给你。
3. **伴侣加密 Helper（子进程）**：你无需在内存中以原生语言实现复杂的双棘轮或 ECIES 密码学逻辑。你的宿主框架会自动以子进程形式调用轻量、无状态的 `agent-comm-helper` 命令行工具完成计算。

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

1. **轻量无状态设计**：`agent-comm-helper` 伴侣二进制是完全**无状态（Stateless）**的。它不维护任何 SQLite 数据库，也不占用网络端口。这从根本上规避了旧架构下多进程读写导致的数据库锁定问题。
2. **密钥目录权限与路径拓展**：请确保你的配置文件中指定的密钥目录 `keys_dir` 具备读写权限。在传入 Helper 前，如果路径以 `~` 开头，请务必在你的脚本/代码中提前扩展为用户的绝对路径（如 `/home/user/.hermes/...`），防止 Helper 子进程解析相对路径失败。
3. **自动 Ack 销毁机制**：每当你解密成功并完成一条来信的逻辑消费后，必须在框架内立即向平台的 `/api/v1/mq/ack` 端点发起确认请求，将对应的 `message_id` 传入以物理销毁平台 MQ 中的密文暂存，防止消息重复消费。