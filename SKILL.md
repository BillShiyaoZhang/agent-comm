---
name: agent-comm
description: >
  Enable two OpenClaw agents to discover each other and communicate via the OpenClaw
  message bus with cryptographic identity verification, one-time token exchange, and
  end-to-end ECIES encryption. Used when two agents running on different machines
  (or the same machine) need to exchange messages directly. Activated when:
  (1) setting up agent-to-agent communication, (2) exchanging contact information
  between agents, (3) sending messages to a peer agent, (4) managing agent
  contacts/peer registry, or (5) receiving messages from a peer.
---

# Agent Comm

## Overview

This skill enables two OpenClaw agents to communicate across machines with three security properties:
- **Mutual cryptographic identity**: Ed25519 signatures in contact exchange
- **One-time token protection**: Contact JSON can only be registered once, preventing reuse
- **End-to-end message encryption**: ECIES (X25519 ECDH + HKDF + AES-256-GCM-SIV)

**Architecture:**

```
Agent A                           Agent B
────────                          ────────
publish_contact.py               publish_contact.py
  → signed contact.json            → signed contact.json
  (user manually exchanges via any channel)
register_peer.py                  register_peer.py
  → Ed25519 signature verify       → Ed25519 signature verify
  → consume one-time token         → consume one-time token
  → store x25519 public key       → store x25519 public key

crypto.encrypt_message()          server.py (Flask on :18792)
  → X25519 ECDH key exchange        ← receives HTTPS messages
  → AES-256-GCM-SIV encrypt        → decrypt + queue
sessions_send / HTTP POST          GET /agent-comm/messages
```

## Python Version & Environment Setup

**Requires Python 3.10+** (scripts use modern `type | None` union syntax).
Managed via `uv`.

```bash
# Recreate venv with a specific Python version (if needed)
uv venv ~/.openclaw/venvs/kg --python 3.12
uv pip install --python ~/.openclaw/venvs/kg/bin/python3 cryptography flask waitress
```

To change the Python version later:
```bash
uv venv ~/.openclaw/venvs/kg --python <version>   # e.g. 3.12, 3.13
uv pip install --python ~/.openclaw/venvs/kg/bin/python3 cryptography flask waitress
```

## Identity Keypair

Each agent auto-generates two Ed25519 keypairs on first use:

| File | Purpose |
|---|---|
| `identity_sk.pem` | Ed25519 private key — never shared, used to sign contacts |
| `identity_pk.pem` | Ed25519 public key — embedded in contact for signature verification |
| `identity_x25519_sk.pem` | X25519 private key — used for ECIES decryption |
| `identity_x25519_pk.pem` | X25519 public key — embedded in contact for ECIES encryption |

Fingerprint = SHA-256(Ed25519 public key)[:16 hex chars].

## One-Time Token Mechanism

When you publish a contact, a fresh 256-bit random token is generated and embedded in the signed payload. When the peer registers your contact, the token is consumed — the same contact JSON cannot be used again. This prevents forwarded contacts from being reused by intermediaries.

Token TTL: 1 hour. Can be manually revoked via `revoke_token.py`.

## Contact Exchange Flow

### Step 1: A publishes their signed contact (with one-time token + x25519 key)

```bash
~/.openclaw/venvs/kg/bin/python3 \
  ~/.openclaw/workspace/skills/agent-comm/scripts/publish_contact.py \
  --output /tmp/my-contact.json
```

Output: `gatewayUrl`, `agentId`, `publicKey` (Ed25519), `x25519PublicKey`, `fingerprint`, `signature`, `token`.

### Step 2: Users manually exchange contact JSON files

### Step 3: B registers A (verifies Ed25519 signature + consumes token on A's server)

```bash
~/.openclaw/venvs/kg/bin/python3 \
  ~/.openclaw/workspace/skills/agent-comm/scripts/register_peer.py \
  --contact-file /tmp/alice-contact.json \
  --peer-id alice
```

On success: B verifies A's Ed25519 signature (over all identifying fields including `x25519PublicKey` and
`fingerprint`), then calls A's `/agent-comm/consume-token` endpoint to burn the token on A's side.
The same contact JSON cannot be registered by any other party after this point.
Bidirectional exchange required — B also publishes and A registers B.

Use `--no-consume-remote` only for offline testing (skips the remote token burn).

## Message Server

Each agent runs a Flask HTTP server (`server.py`) on `localhost:18792`, exposed via Cloudflare Tunnel as HTTPS. This server receives ECIES-encrypted messages from peers.

**Endpoints:**

| Method | Path | Auth | Description |
|---|---|---|---|
| GET | `/agent-comm/health` | No | Liveness probe |
| GET | `/agent-comm/identity` | No | Returns fingerprint, X25519 pub, auth token |
| POST | `/agent-comm/consume-token` | No* | Consume a one-time registration token (called by registering peer) |
| POST | `/agent-comm/messages` | No** | Submit encrypted message (timestamp-validated, replay-resistant) |
| GET | `/agent-comm/messages` | Bearer token | Poll for messages (with auto-decrypt) |
| GET | `/agent-comm/messages/<id>` | Bearer token | Fetch single message |

*No auth: the 256-bit token is the credential. Returns 409 if already consumed or expired.
**POST requires valid ciphertext; messages with a timestamp older than 5 minutes are rejected.

**Start the server:**

```bash
~/.openclaw/venvs/kg/bin/python3 \
  ~/.openclaw/workspace/skills/agent-comm/scripts/server.py &
```

**Or add to `start-claw.sh`** alongside the Gateway + tunnel:

```bash
# Start agent-comm HTTP server
nohup ~/.openclaw/venvs/kg/bin/python3 \
  ~/.openclaw/workspace/skills/agent-comm/scripts/server.py > /tmp/agent-comm-server.log 2>&1 &
```

## Sending Messages

Encrypt with peer's X25519 public key (from registered contact), then POST to peer's `/agent-comm/messages`:

```bash
# Resolve peer's session key
SESSION_KEY=$(~/.openclaw/venvs/kg/bin/python3 \
  ~/.openclaw/workspace/skills/agent-comm/scripts/send_message.py \
  --peer-id alice)

# Encrypt and send
MSG="Hello Alice!"
ENCRYPTED=$(~/.openclaw/venvs/kg/bin/python3 \
  ~/.openclaw/workspace/skills/agent-comm/scripts/send_message.py \
  --peer-id alice --encrypt "$MSG")

# POST to peer's server
curl -X POST "https://alice-tunnel-url.trycloudflare.com/agent-comm/messages" \
  -H "Content-Type: application/json" \
  -d "$ENCRYPTED"
```

## Receiving Messages

Poll the server for new messages:

```bash
AUTH_TOKEN=$(cat ~/.openclaw/workspace/skills/agent-comm/contacts/auth_token.json | \
  python3 -c "import json,sys; print(json.load(sys.stdin)['token'])")

~/.openclaw/venvs/kg/bin/python3 \
  ~/.openclaw/workspace/skills/agent-comm/scripts/receive_messages.py \
  --auth-token "$AUTH_TOKEN" \
  --mark-read
```

Or use the `GET /agent-comm/messages` endpoint directly — messages are auto-decrypted when sender is in contacts.

## Message Encryption Protocol (ECIES)

- **Key exchange**: X25519 ECDH (Curve25519)
- **Key derivation**: HKDF-SHA256 (RFC 5869), info="agent-comm-v1"
- **Encryption**: AES-256-GCM-SIV (AEAD, nonce-misuse resistant)
- **AAD**: Sender Ed25519 fingerprint (binds ciphertext to sender identity)

Each message uses a fresh ephemeral X25519 keypair → **perfect forward secrecy**.

## Scripts Reference

| Script | Purpose |
|---|---|
| `get_tunnel_url.py` | Read current Cloudflare Tunnel URL |
| `identity.py` | Ed25519 + X25519 keypair generation, sign, verify |
| `one_time_token.py` | One-time token generate/consume/revoke |
| `publish_contact.py` | Generate signed contact with fresh token |
| `register_peer.py` | Verify signature + consume token, store peer contact |
| `send_message.py` | Encrypt message for peer, resolve session key |
| `receive_messages.py` | Poll server for messages, auto-decrypt |
| `server.py` | Flask HTTP server for receiving encrypted messages |
| `revoke_token.py` | Revoke current pending token |
| `crypto.py` | ECIES encrypt/decrypt (X25519 + HKDF + AES-256-GCM-SIV) |

## Cautions & Gotchas

### PEM/JSON 内容禁止手动复制粘贴
Contact JSON 包含 PEM 编码的公钥（如 `-----BEGIN PUBLIC KEY-----...-----END PUBLIC KEY-----`），格式非常敏感。手动复制 PEM 文本时极易漏掉 dash（`-----END` 容易写成 `----END`），导致 `cryptography` 库无法解析。

**正确做法：**
- 直接使用文件路径操作（读/写/注册），不要复制粘贴文本内容
- 通过飞书等渠道传输 contact JSON 时，用 `--media` 发送文件附件，而非粘贴文本
- 注册 contact 时，使用 `~/.openclaw/media/inbound/` 下的原始文件路径

**错误做法：**
```bash
# ❌ 手动复制粘贴 PEM 文本到 write 工具 —— 容易漏掉 dash
# ✅ 直接使用文件路径
~/.openclaw/venvs/kg/bin/python3 scripts/register_peer.py \
  --contact-file ~/.openclaw/media/inbound/peer-contact---xxx.json \
  --peer-id alice
```

### 飞书文件传输可能损坏 PEM
飞书文件上传/下载可能会对 JSON 内容做格式处理，导致 PEM 少 dash。遇到签名验证失败时，优先使用 `media/inbound/` 下的原始文件。

## Limitations

- **Manual contact exchange**: Users share contact JSON via any channel before agents can communicate.
- **Temporary tunnels**: Tunnel URLs change on restart without a Cloudflare account + domain.
- **Token TTL**: Published contacts expire after 1 hour.
- **Python version**: 3.10+ required (type union syntax). Use `uv venv` to manage versions.
- **Dependencies**: `cryptography`, `flask`, `waitress` installed via `uv pip`.