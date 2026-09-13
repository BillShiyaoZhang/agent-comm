# Agent Comm — Secure Agent Messaging

Agent Comm provides local identities, end-to-end encryption, a P2P SDK, and a local helper for Hermes/OpenClaw connectors.

## Hermes installation and upgrade

Read the [integration contract and handoff checklist](docs/HERMES_INTEGRATION.md) and [Hermes plugin instructions](connectors/hermes-platform/README.md). Resolve the active Hermes profile instead of assuming `~/.hermes`.

Upgrade platform, SDK/helper, and connector together: envelopes now require signatures and platform ACK requires authentication. Preserve identity keys, helper `mailbox.db`, and plugin receipt databases. Server instructions are in the platform repository's `HERMES_UPGRADE.md`.

```sh
go build -o agent-comm-helper ./cmd/helper
./agent-comm-helper init /absolute/path/to/agent/keys
./agent-comm-helper daemon /absolute/path/to/agent/keys https://YOUR_PLATFORM 45042
```

Give each identity its own directory and loopback port. The connector's `platform_url` is `http://127.0.0.1:45042`. Check `/info`, a real established SSE connection, and a two-identity round trip before declaring setup complete.

## Local helper data flow

```text
Hermes / OpenClaw
  ↕ local HTTP, SSE, consumer ACK
helper: local keys, signing/encryption, SQLite inbox/outbox
  ↕ HTTPS Registry / MQ (five-second reconciliation)
agent-comm-platform: identity registry and durable encrypted mailbox
```

- `POST /api/v1/mq/store` returns HTTP 202 with a stable `message_id`: local durable acceptance. The worker retries the same signed ciphertext. `platform_queued` means the platform accepted it, not that the recipient completed a task.
- Incoming envelopes are authenticated and persisted in the helper inbox before platform ACK. SSE and `GET /api/v1/mq/retrieve` replay unconsumed messages. The connector ACKs local consumption after processing completes.
- Reliable helper sends use MQ. The SDK's traditional `SendMessage` still offers P2P/DR; signed direct envelopes also use the durable receive callback. HTTPS MQ uses static X25519 + AES-GCM and Ed25519 signatures; it does not provide Double Ratchet forward secrecy.
- The plaintext helper API binds only to loopback and rejects cross-origin browser requests. Browser UIs need a separately authenticated bridge. Messaging identity, tool execution, and Gateway control are separate permissions.

### 3. Persistent Daemon Service (Supervisor & Keep-Alive)

To ensure your agent is ready to receive and respond to secure calls 7x24, it is highly recommended to configure the daemon (`agent-comm-helper`) as a system service with keep-alive capability:

* **macOS (`launchd` Service)**:
  Create the plist file at `~/Library/LaunchAgents/com.billshiyaozhang.agent-comm-helper.plist` (replace `YOUR_USER` with your macOS username):
  ```xml
  <?xml version="1.0" encoding="UTF-8"?>
  <!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
  <plist version="1.0">
  <dict>
      <key>Label</key>
      <string>com.billshiyaozhang.agent-comm-helper</string>
      <key>ProgramArguments</key>
      <array>
<string>/Users/YOUR_USER/.agent-comm/bin/agent-comm-helper</string>
      <string>daemon</string>
      <string>/Users/YOUR_USER/.agent-comm/keys</string>
      <string>https://agent-communication.online</string>
      <string>45042</string>
      </array>
      <key>RunAtLoad</key>
      <true/>
      <key>KeepAlive</key>
      <true/>
      <key>StandardOutPath</key>
      <string>/Users/YOUR_USER/.agent-comm/logs/daemon.out.log</string>
      <key>StandardErrorPath</key>
      <string>/Users/YOUR_USER/.agent-comm/logs/daemon.err.log</string>
  </dict>
  </plist>
  ```
  Load and start the service:
  ```bash
  launchctl load -w ~/Library/LaunchAgents/com.billshiyaozhang.agent-comm-helper.plist
  ```

* **Linux (`systemd` User Service)**:
  Create the service file at `~/.config/systemd/user/agent-comm-helper.service`:
  ```ini
  [Unit]
  Description=Agent Comm Helper Daemon
  After=network.target

  [Service]
  ExecStart=%h/.agent-comm/bin/agent-comm-helper daemon %h/.agent-comm/keys https://agent-communication.online 45042
  Restart=always
  RestartSec=5
  StandardOutput=append:%h/.agent-comm/logs/daemon.out.log
  StandardErrorOutput=append:%h/.agent-comm/logs/daemon.err.log

  [Install]
  WantedBy=default.target
  ```
  Reload and start the user service:
  ```bash
  systemctl --user daemon-reload
  systemctl --user enable --now agent-comm-helper.service
  ```

---

## 💡 What Can This Project Do For You?

In the era of Multi-Agent collaboration, AI agents running on different devices and in different network environments often need to exchange data, synchronize schedules, or collaborate on tasks.

**agent-comm** is designed to solve this pain point. It is a **decentralized, end-to-end encrypted** communication suite. It allows two AI agents to establish an exclusive "secure encrypted hotline" without phone numbers or email registration. No middleman can decrypt the data they transfer.

Depending on your configuration, the system can run entirely decentralized or integrate with supplementary services to ensure messaging delivery across restrictive firewalls.

---

## 🧩 Feature Segmentation: Standalone Skill (SDK) vs. Platform-Assisted Mode

### 1. Standalone Skill (SDK) Local Features
Our client-side SDK provides the following core security capabilities without requiring any public server infrastructure:
* **Local Identity Management & Key Generation**: Each Agent generates its own self-certifying **URN (Uniform Resource Name)** fingerprint locally. URNs are kept entirely on-device and do not require registration with any CA authorities.
* **Peer-to-Peer (P2P) Direct Encrypted Streams**: If both Agents possess public IP addresses or are located on the **same Local Area Network (LAN)**, they dial each other directly (via TCP/QUIC) to establish secure streams using the forward-secure Double Ratchet algorithm. **All payload content flows directly between devices without going through any transit servers, ensuring total privacy.**
* **Contact Card Exchange & Local Storage**: Allows agents to export, share, and parse connection metadata (Contact Cards) text blocks, persisting trusted contact relationships inside a local SQLite database.

### 2. Features Requiring [agent-comm-platform](https://github.com/BillShiyaoZhang/agent-comm-platform) Integration
To enable seamless connectivity for agents behind strict NAT configurations (such as cellular networks, enterprise routers) and handle messages when recipients are offline, you can hook the skill up to a cloud platform. The platform provides:
* **Super Registry**: High-concurrency directory service translating URN queries into target IP endpoints and session keys in milliseconds.
* **Relay v2 Intranet Traversal**: Public relay nodes aiding NAT hole-punching and tunneling, allowing blocked terminals to communicate.
* **MQ Offline Mailbox (Envelope Cache)**: If the recipient is powered down or offline, the sender packs messages into "encrypted envelopes" using Double Ratchet locally and blind-stores them on the platform's MQ. The recipient pulls and decrypts envelopes upon reconnecting, firing an Ack to permanently purge the platform's copy.

---

## 🌟 Typical Use Cases

### 1. Standalone LAN Collaboration (Skill Only)
* **Scenario**: You run two agents inside the same office local network.
* **Outcome**: They discover each other locally, dialing direct P2P double-ratchet encrypted sessions without calling any external cloud servers. All business data remains strictly behind your local router.

### 2. Cross-Cloud Asynchronous Collaboration (Skill + Platform)
* **Scenario**: You host a **Writer-Agent** on Alibaba Cloud and an **Illustrator-Agent** on a strict local machine that you power off during the night.
* **Outcome**: Working with [agent-comm-platform](https://github.com/BillShiyaoZhang/agent-comm-platform), they punch NAT holes via the platform's Relay v2 to exchange real-time packets. When the local computer is turned off at night, the Writer-Agent's assets are queued as secure envelopes in the MQ mailbox. The Illustrator-Agent pulls and processes the queue upon rebooting the next morning.

---

## 🚀 Quick Start: Command Your Agent to Start Chatting (Plain English Prompts)

Once your agent is connected using the framework adapter and started, you do not need to write Go cryptography code or manually exchange contact card text files. You can manage connections and command your agent directly through the cloud Web Console using natural language:

### Step 1: Bind and Establish Trust in the Web Console
1. Log into your cloud Web Dashboard (`agent-collaboration-web`).
2. Go to the **Agents** page, click **Add Agent**, select **Bind** mode, enter your agent's URN (available from startup logs or by running `agent-comm-helper init`) and configure its Local URL (e.g., `http://localhost:8000`).
3. On the Agent's detail page, verify the connectivity status is **Online**, then click **Establish Mutual Trust**. The Web Dashboard will automatically push the Owner's virtual identity into the local Agent's contact store, opening up the E2E encrypted control channel.

### Step 2: Add Contacts in the Web Console
1. Go to the **Contacts** page in the Web Console, and click **Add Contact**.
2. Input the target agent's URN and click **Resolve**. The platform's Registry will automatically fetch and verify the contact's public keys.
3. Save the contact with a friendly display alias.

### Step 3: Command Your Agent to Send Secure Messages
* 💬 **You type into the chat panel to command your Agent**:
  > "Please send a message to Partner-Assistant (URN: urn:agent-comm:agent:yyyyyy) saying 'Hello! Connection established successfully! The project is fully deployed.'"
* 🤖 **Agent executes and replies**:
  > "Sure! I have invoked the secure communication channel connector. The message payload is locally encrypted targeting the recipient's public key and successfully stored in the platform's MQ. The recipient will stream and decrypt the envelope in real-time via SSE."

---

## 🌐 Platform Compliance & Auditing Disclaimers

When connecting your agents to a public or self-hosted [agent-comm-platform](https://github.com/BillShiyaoZhang/agent-comm-platform) deployment, please note the security boundaries:

> [!WARNING]
> ### ⚠️ Platform Compliance & Logging Warning
> - **Native Privacy Mode**: By default, the platform serves as a blind relay/mailbox. Message contents are encrypted locally via Double Ratchet before transmission. The platform cannot decrypt or inspect payloads.
> - **Regulatory Compliance Mode (MITM)**: To comply with national/regional regulations regarding network information providers (such as state auditing laws in mainland China), the platform can be configured to run in compliance mode. It deploys a "Gateway MITM Proxy" holding a public gateway key. The sender encrypts messages targeting the gateway, which **automatically decrypts, inspects, and logs communications (performing safety filters and legal archiving)** before re-encrypting and forwarding the payload to the actual recipient.
> 
> **Privacy Advice**: If your application demands absolute privacy, **do not connect to public platform services**. You should configure this Skill to point to private, isolated Bootstrap and Relay nodes owned and operated exclusively by your team, running in Native Privacy Mode.

👉 **Go API References**:
- [InitIdentity (agent/agent.go)](agent/agent.go#L42)
- [SendMessage (agent/agent.go)](agent/agent.go#L116)
- [OnMessage (agent/agent.go)](agent/agent.go#L205)
- [GenerateContactCard (agent/contact_card.go)](agent/contact_card.go#L223)
- [ImportContactCard (agent/contact_card.go)](agent/contact_card.go#L228)

---

## 🛠️ Developer & Engineering Portal

If you are a developer looking to dive deep into the libp2p network implementation, double ratchet state engines, or run local validation scripts, please read:

👉 **[Architecture Design & Developer Overview (OVERVIEW_EN.md)](OVERVIEW_EN.md)**
