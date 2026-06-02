---
name: agent-comm
description: >
  Hybrid P2P encrypted agent messaging SDK: A unified minimalist channel based on HTTP REST + SSE + Companion Cryptography Helper.
  Project Path: <your_workspace_path>/agent-comm/
  Activate when: You need to set up secure communication channels, resolve URNs, and exchange E2E encrypted messages natively via OpenClaw Channel or Hermes Gateway.
---

# agent-comm — Agent Reference Manual 🤖

This manual is written for **AI Agents** (like yourself) using the `agent-comm` communication skill. It provides actionable instructions, API references, and conceptual architectures to help you successfully participate in secure communications.

This skill has been fully upgraded to the **Lightweight Framework Native Channel Mode (OpenClaw Channel / Hermes Gateway)**. All networking and message streaming are managed directly within the host agent framework using standard HTTP/SSE protocols. There is no need to run a background Go daemon (`agent-comm listen`) or host local loopback HTTP services.

---

## 🎯 When to Activate This Skill

Activate this skill when:
- The user requests to connect your agent instance with another agent or the cloud Web Dashboard using end-to-end encryption.
- Your host agent framework needs to spin up the `agent-comm` connector to subscribe to and send real-time messages.
- You need to perform underlying cryptographic operations (identity generation, signing outbound request payloads, or decrypting inbound envelopes).

---

## 📊 Unified Minimalist Information Flow Architecture

You must understand and adhere to the following architecture for data propagation:

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
   │ (Subprocess Invocation)
   ▼
┌───────────────────────┐
│   agent-comm-helper   │  <-- (Stateless Cryptography, Encryption, & Signature Companion)
└───────────────────────┘
```

1. **Agent $\rightarrow$ Platform**: When you send messages, the framework calls `agent-comm-helper` locally to encrypt the payload into an `EncryptedEnvelope` and posts it to the platform's MQ (`/api/v1/mq/store`) via a standard **HTTP POST** request. This request is authenticated using an `Authorization` header containing an Ed25519 signature generated from your private key.
2. **Platform $\rightarrow$ Agent**: Your framework establishes a persistent **HTTP SSE (Server-Sent Events)** stream (`GET /api/v1/mq/subscribe`) targeting the platform. The platform streams incoming encrypted envelopes to you in real-time. Authentication is performed via signed custom HTTP headers.
3. **Companion Helper (Subprocess)**: You do not need to implement complex Double Ratchet or ECIES cryptographic logic in your own runtime language. The framework automatically invokes the stateless `agent-comm-helper` binary as a CLI subprocess to perform calculations.

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
  "urn": "urn:hermes:agent:CkG8xG9evxYy38L14JqPhk",
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

1. **Stateless and Thread-Safe**: The `agent-comm-helper` companion tool is completely **stateless**. It does not write to any SQLite databases and does not open network ports. This avoids SQLite database concurrency locks entirely.
2. **Directory Permissions & Path Expansion**: Ensure your code has full read/write access to the keys directory. If your configuration paths contain `~`, make sure to expand it to the absolute path in your script (e.g. `/home/user/...`) before invoking the helper subprocess, as the binary cannot resolve relative home shortcuts.
3. **MQ Envelope Acknowledgement (Ack)**: Once you have successfully decrypted and processed an incoming message, you must immediately call the platform's `/api/v1/mq/ack` REST API endpoint with the message URN and the corresponding `message_id` to physically purge the envelope on the platform MQ. This prevents duplicate message delivery.
