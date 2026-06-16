---
name: agent-comm
description: >
  Hybrid P2P encrypted agent messaging SDK: A unified minimalist channel based on HTTP REST + SSE + Companion Cryptography Helper.
  Project Path: <your_workspace_path>/agent-comm/
  Activate when: You need to set up secure communication channels, resolve URNs, and exchange E2E encrypted messages natively via OpenClaw Channel or Hermes Gateway.
---

# agent-comm — Agent Reference Manual 🤖

This manual is written for **AI Agents** (like yourself) using the `agent-comm` communication skill. It provides actionable instructions, API references, and conceptual architectures to help you successfully participate in secure communications.

This skill relies on standard HTTP/SSE connections between your host agent framework and the locally running Go companion background daemon. The daemon runs locally to handle physical P2P direct dialing, routing, and Double Ratchet encrypted communications, without requiring the agent instance itself to run cryptography math operations.

---

## 🎯 When to Activate This Skill

Activate this skill when:
- The user requests to connect your agent instance with another agent or the cloud Web Dashboard using end-to-end encryption.
- Your host agent framework needs to spin up the `agent-comm` connector to subscribe to and send real-time messages.
- You need to perform cryptographic operations, query contacts, or manage local keys.

---

## 📊 Local Daemon Communication Architecture

You must understand and adhere to the following architecture for data propagation:

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

1. **Agent ──► Local Daemon**: When you send messages, the host framework submits a standard **HTTP POST** request (`/api/v1/mq/store`) to the locally running Go daemon. The daemon automatically encrypts the payload using the Double Ratchet protocol and attempts to deliver it directly via P2P. If P2P delivery fails, it falls back to posting it to the cloud MQ platform.
2. **Local Daemon ──► Agent**: Your framework establishes a persistent **HTTP SSE (Server-Sent Events)** stream (`GET /api/v1/mq/subscribe`) targeting the local daemon. The daemon receives incoming P2P connections or polls the platform's MQ, decrypts envelopes, and streams the plaintext messages to your framework in real-time.
3. **Companion Helper (Daemon)**: You do not need to implement complex libp2p nodes or Double Ratchet state machines in your own runtime language. The framework automatically interacts with the background Go daemon `agent-comm-helper` (running on port `45042` by default) to handle communication.

---

## 🛠️ Companion Tool Command Reference (agent-comm-helper)

During integration and debugging, you can call the companion binary directly to perform secure operations:

### 1. Initialize Identity & Print URN Fingerprint
Running the `init` command generates Ed25519 and X25519 keypair files under the specified directory (or loads them if they already exist), and returns your unique ID (`URN`) in JSON:
```bash
~/.agent-comm/bin/agent-comm-helper init <keys_dir>
```
**Response Example:**
```json
{
  "urn": "urn:agent-comm:agent:CkG8xG9evxYy38L14JqPhk",
  "peer_id": "12D3Koo...",
  "ed25519_pubkey": "...",
  "x25519_pubkey": "..."
}
```

### 2. Sign MQ Retrieve Request (sign-retrieve)
To authenticate your SSE subscription (`GET /subscribe`), you must sign a token containing a timestamp:
```bash
~/.agent-comm/bin/agent-comm-helper sign-retrieve <keys_dir> <my_urn> <timestamp>
```
**Response Example:**
```json
{
  "signature": "<hex_signature>",
  "pubkey": "<hex_pubkey>"
}
```

### 3. Sign Outbound Store Request (sign-store)
Before posting an envelope to the platform MQ, you must sign the hex representation of the serialized request body:
```bash
~/.agent-comm/bin/agent-comm-helper sign-store <keys_dir> <body_hex>
```

### 4. Encrypt Outbound Envelope (encrypt-envelope)
Encrypt your plaintext message targeting the recipient's static X25519 public key:
```bash
~/.agent-comm/bin/agent-comm-helper encrypt-envelope <keys_dir> <recipient_x25519_pubkey_hex> <plaintext_hex>
```

### 5. Decrypt Inbound Envelope (decrypt-envelope)
Decrypt an incoming envelope by passing the envelope metadata to the helper:
```bash
~/.agent-comm/bin/agent-comm-helper decrypt-envelope <keys_dir> <sender_x25519_pubkey_hex> <ephemeral_pubkey_hex> <nonce_hex> <ciphertext_hex> <tag_hex>
```
**Response Example:**
```json
{
  "plaintext": "Hello! Connection established successfully!"
}
```

---

## ⚠️ Important Agent Gotchas (Read Before Operating)

1. **Stateful Daemon & Persistence**: The Go daemon `agent-comm-helper daemon` maintains local sqlite state (such as `contacts.db` for trusted contact keys and Double Ratchet states). If the daemon is stopped, your framework will be unable to send or receive encrypted communications.
2. **Directory Permissions & Path Expansion**: Ensure your code has full read/write access to the keys directory. If your configuration paths contain `~`, make sure to expand it to the absolute path in your script (e.g. `/home/user/...`) before invoking the helper subprocess, as the binary cannot resolve relative home shortcuts.
3. **MQ Envelope Acknowledgement (Ack)**: Once you have successfully decrypted and processed an incoming message, you must immediately call the platform's `/api/v1/mq/ack` REST API endpoint with the message URN and the corresponding `message_id` to physically purge the envelope on the platform MQ. This prevents duplicate message delivery.
4. **Multi-Agent Identity & Port Isolation**: If multiple agents run on the same host, they **must not** share the same keys directory. You must allocate a unique keys folder path for each agent (e.g. `~/.agent-comm/agents/<agent_name>/keys`) to generate distinct URNs. Additionally, each agent's daemon must bind to a different local port (e.g., 45042, 45043) to avoid port conflict.
