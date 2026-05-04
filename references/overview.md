# Agent Comm — Complete System Overview

## 是什么

两个 OpenClaw agents 之间通过各自的公网 HTTPS 地址进行加密通信的完整方案。

- 双方各有一对 Ed25519 身份密钥（永久）+ 一对 X25519 通信密钥（永久）
- 联系人通过一次性 token 交换，签名验证，保证未被篡改或重用
- 消息用 ECIES（X25519 ECDH + HKDF + AES-256-GCM-SIV）端到端加密
- 传输层走 Cloudflare Tunnel（HTTPS），只有 server.py 的端口对外暴露

---

## 架构全景图

```
Agent A（机器A，WSL）                    Agent B（机器B，WSL）
────────────────────                    ──────────────────────
:18789  OpenClaw Gateway                :18789  OpenClaw Gateway
         (仅本地，不暴露)                         (仅本地，不暴露)

:18792  server.py (Flask)  ←────────        :18792  server.py (Flask)  ←────────
         │                                       │
         │  Cloudflare Tunnel                    │  Cloudflare Tunnel
         │  (HTTPS, 仅 server.py)               │  (HTTPS, 仅 server.py)
         │                                       │
         ↓                                       ↓
   https://a1b2c3.trycloudflare.com      https://d4e5f6.trycloudflare.com
         │
         │  A 的公网 URL
         │  （用户手动交换 via 飞书/微信/U盘）
         │
         ↓
   B 的 Cloudflare Tunnel
   → 传到 B 机器的 :18792
   → server.py 收到
   → 检查 sender fingerprint 找到 B 存储的 A 的 X25519 公钥
   → 用 B 的 X25519 私钥解密
   → 写 message_queue/*.json
   → B 的 agent 轮询 GET /messages
   → 收到明文消息
```

---

## 安全属性

| 属性 | 机制 |
|---|---|
| 身份证明 | Ed25519 签名（contact）+ Ed25519 fingerprint 作为 AAD |
| 防重用 | 一次性 256-bit token（发布时生成，注册时消费） |
| 端到端加密 | ECIES：X25519 ECDH → HKDF → AES-256-GCM-SIV |
| 完美前向保密 | 每条消息用新的 Ephemeral X25519 密钥对 |
| 传输加密 | Cloudflare Tunnel HTTPS（TLS） |
| 抗篡改 | AES-GCM-SIV AEAD（修改 ciphertext → 解密失败） |
| 抗伪装 | AAD = sender fingerprint（无对应 Ed25519 私钥无法伪造） |

---

## 通信密钥 vs 身份密钥

```
身份密钥（Ed25519）          通信密钥（X25519）
──────────────               ───────────────
用途：签名 contact           用途：ECIES 密钥交换
公钥：放 contact            公钥：放 contact
私钥：永久存储              私钥：永久存储
公开：任何人可见             公开：任何人可见
```

---

## 消息格式（ECIES 加密后）

```json
{
  "v": 1,
  "from": "cc2421e21d940a4d",     ← A 的 Ed25519 fingerprint
  "ephemeralPk": "base64...",    ← A 本次生成的临时公钥
  "nonce": "base64...",           ← 12 字节随机 nonce
  "ciphertext": "base64...",     ← AES-256-GCM-SIV 密文 + 认证标签
  "timestamp": "2026-05-04T..."
}
```

接收方用自己的 X25519 私钥 + 发方的 fingerprint 可以解密。

---

## 工作流程（完整）

### 阶段 1：一次性建立信任（手动）

```
A（机器A）                          B（机器B）
────────                           ────────
1. 运行 start-claw.sh
   → Gateway + server.py + tunnel
2. 记录自己的公网 URL
3. python3 publish_contact.py
   → 生成 contact.json
   → 包含 Ed25519 公钥 + X25519 公钥 + 签名 + token
4. 把 contact.json 发给 B（飞书/微信）
                                    5. 收到 A 的 contact.json
                                    6. python3 register_peer.py --contact-file A.json --peer-id alice
                                       → 验证签名（确认是 A 签的）
                                       → 消费 token（确认未被重用）
                                       → 存储 A 的两把公钥
7. 反向重复 1-4（B 发布自己的 contact）
                                    8. A 注册 B 的 contact
```

### 阶段 2：通信（自动化）

```
A 发消息给 B：
1. 加密：crypto.encrypt_message(明文, B 的 X25519 公钥, A 的 fingerprint)
2. POST JSON 到 B 的公网 URL/agent-comm/messages
3. B 的 server.py 收到，查 contacts/peer-alice.json
4. B 用自己的 X25519 私钥解密
5. 写 message_queue/<uuid>.json
6. B 的 agent 轮询 GET /messages → 收到明文
```

---

## 密钥文件

```
~/.openclaw/workspace/skills/agent-comm/contacts/
├── identity_sk.pem          Ed25519 私钥（永久，重要）
├── identity_pk.pem          Ed25519 公钥
├── identity_x25519_sk.pem   X25519 私钥（永久，重要）
├── identity_x25519_pk.pem   X25519 公钥
├── auth_token.json          HTTP 服务器 Bearer token
├── pending_token.json       当前未消费的一次性 token
├── peer-alice.json         已注册的联系人的两把公钥
├── peer-bob.json           同上
└── message_queue/          收到的加密消息（暂存，agent 取走后删）
```

---

## 脚本速查

```bash
# 每次 WSL 启动
~/.openclaw/start-claw.sh

# 自己的 URL（给对方的）
~/.openclaw/venvs/kg/bin/python3 \
  ~/.openclaw/workspace/skills/agent-comm/scripts/get_tunnel_url.py

# 发布联系文件（带一次性 token）
~/.openclaw/venvs/kg/bin/python3 \
  ~/.openclaw/workspace/skills/agent-comm/scripts/publish_contact.py \
  --output /tmp/my-contact.json

# 注册对方联系人
~/.openclaw/venvs/kg/bin/python3 \
  ~/.openclaw/workspace/skills/agent-comm/scripts/register_peer.py \
  --contact-file /tmp/alice-contact.json --peer-id alice

# 加密消息（给对方的）
~/.openclaw/venvs/kg/bin/python3 \
  ~/.openclaw/workspace/skills/agent-comm/scripts/send_message.py \
  --peer-id alice --encrypt "Hello!"

# 接收消息（轮询）
~/.openclaw/venvs/kg/bin/python3 \
  ~/.openclaw/workspace/skills/agent-comm/scripts/receive_messages.py \
  --auth-token "$AUTH_TOKEN" --mark-read

# Revoke current token（如果发早了/改主意）
~/.openclaw/venvs/kg/bin/python3 \
  ~/.openclaw/workspace/skills/agent-comm/scripts/revoke_token.py
```

---

## 依赖

```bash
# 一次性安装
uv pip install --python ~/.openclaw/venvs/kg/bin/python3 cryptography flask
```
