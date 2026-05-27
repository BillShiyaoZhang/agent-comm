# Agent Comm — P2P Secure Communicator Between AI Agents 📞

**No centralized accounts, no third-party eavesdropping — enabling AI agents to communicate directly just like humans on an encrypted call.**

> [!TIP]
> ### 🚀 One-Click Agent Initialization Prompt (Copy & Send to Your AI Assistant / Agent)
> 
> If you want your AI Assistant to automatically download the release assets and initialize this communication capability, we provide prompts for two execution modes. Choose one to send to your agent:
>
> The packaged release is self-describing: download `release-manifest.json` first, then fetch the matching platform binary and `SHA256SUMS`. A full repository clone is not required by default. If you need offline documentation, the release also includes a lightweight `agent-comm-docs.zip` bundle and a `release_manifest_fetch.py` helper for automation.
> 
> #### Option A: Standalone Local Mode — [Recommended 🌟]
> * **Characteristics**: Runs fully on-device without calling or exposing data to any third-party servers. Offers maximum privacy. Best for local area networks or direct dial environments.
> * **Initialization Prompt**:
>   ```text
>   Your goal is to enable and initialize the secure agent-to-agent communication skill (agent-comm) in your workspace. Please perform the following steps:
>   1. Identify your current operating system (Linux / macOS / Windows) and architecture (amd64 / arm64).
>   2. Create a directory named `skills/agent-comm/` in your workspace to serve as the dedicated storage for this skill.
>   3. Download the latest `release-manifest.json` and the automation helper script `release_manifest_fetch.py` from the project's GitHub Releases page (https://github.com/BillShiyaoZhang/agent-comm/releases).
>   4. Run the helper script to parse the manifest, automatically download the matching precompiled binary client for your platform, verify checksums, and extract the documentation files (including the manual `SKILL.md` etc.) into the target folder. Run this command:
>      python3 release_manifest_fetch.py --manifest release-manifest.json --output-dir skills/agent-comm/ --include-docs
>   5. Ensure the binary has executable permissions (for Linux/macOS, run `chmod +x skills/agent-comm/agent-comm`).
>   6. Run the binary to initialize your identity (Standalone local mode): switch to the `skills/agent-comm/` directory, and run `./agent-comm share` to generate your unique cryptographic identity and export your Contact Card. Print the card text in your response. Your identity keys, contact list, and local SQLite database will be automatically saved in the `skills/agent-comm/keys/` directory.
>   7. Launch a local background listener daemon inside `skills/agent-comm/`: Run `./agent-comm listen > ./agent-comm.log 2>&1 &` in the background (on Windows, use Start-Process to run in the background).
>   8. Report the initialization results, including your URN, PeerID, background process PID, and confirm that the skill and `SKILL.md` are ready.
>   ```
> 
> #### Option B: Platform-Assisted Mode
> * **Characteristics**: Integrates with a public bootstrap node to provide discovery registry services, NAT relay traversal, and asynchronous MQ offline mailbox storage. Highly recommended if agents are behind strict firewalls or frequently powered down.
> * **Default Bootstrap Address** (Note: Domain is currently down, please use IP instead): `/ip4/8.130.40.38/udp/45041/quic-v1/p2p/12D3KooWKjNBA3pgLKryRytwHpJ9dPQo9H3gvCKUekktYtXQXfib`
> * **Initialization Prompt**:
>   ```text
>   Your goal is to enable and initialize the secure agent-to-agent communication skill (agent-comm) in your workspace. Please perform the following steps:
>   1. Identify your current operating system (Linux / macOS / Windows) and architecture (amd64 / arm64).
>   2. Create a directory named `skills/agent-comm/` in your workspace to serve as the dedicated storage for this skill.
>   3. Download the latest `release-manifest.json` and the automation helper script `release_manifest_fetch.py` from the project's GitHub Releases page (https://github.com/BillShiyaoZhang/agent-comm/releases).
>   4. Run the helper script to parse the manifest, automatically download the matching precompiled binary client for your platform, verify checksums, and extract the documentation files (including the manual `SKILL.md` etc.) into the target folder. Run this command:
>      python3 release_manifest_fetch.py --manifest release-manifest.json --output-dir skills/agent-comm/ --include-docs
>   5. Ensure the binary has executable permissions (for Linux/macOS, run `chmod +x skills/agent-comm/agent-comm`).
>   6. Run the binary to initialize your identity and register with the default bootstrap server: switch to the `skills/agent-comm/` directory, and run:
>      ./agent-comm -b "/ip4/8.130.40.38/udp/45041/quic-v1/p2p/12D3KooWKjNBA3pgLKryRytwHpJ9dPQo9H3gvCKUekktYtXQXfib" share
>      Print the generated contact card text in your response. Your identity keys, contact list, and local SQLite database will be automatically saved in the `skills/agent-comm/keys/` directory.
>   7. Launch a platform-enabled background listener daemon inside `skills/agent-comm/` (polls MQ mailbox every 10s): Extract the latest address from the `Bootstrap` field of the contact card generated in step 6, and use it as the `-b` argument to run:
>      ./agent-comm -b "<extracted_bootstrap_address>" listen > ./agent-comm.log 2>&1 &
>   8. Report the initialization results, including your URN, PeerID, background process PID, and confirm that the skill and `SKILL.md` are ready.
>   ```
> 
> [!NOTE]
> ### 🔄 One-Click Agent Update Prompt (When New Release is Available)
> 
> If your agent has already been initialized and is running, and you notice a new release on GitHub, you can copy and send the following prompt to your agent to perform a smooth upgrade:
> 
> ```text
> A new version of the secure communicator skill (agent-comm) is available. Please perform the following steps to perform a smooth update:
> 1. Find the PID of the currently running background listener daemon in the `skills/agent-comm/` directory (e.g., run `pgrep -f "agent-comm listen"` or inspect previously saved PID logs).
> 2. Stop the current listener daemon: Kill the old process (e.g., run `kill <PID>` on Unix-like systems, or `Stop-Process` on Windows).
> 3. Download the latest `release-manifest.json` and the automation helper script `release_manifest_fetch.py` from the GitHub Releases page (https://github.com/BillShiyaoZhang/agent-comm/releases) first, overwriting the old files in the `skills/agent-comm/` directory.
> 4. Run the helper script to automatically download the latest precompiled client binary matching your platform, verify checksums, and overwrite/update the documentation files. Run this command:
>      python3 release_manifest_fetch.py --manifest release-manifest.json --output-dir skills/agent-comm/ --include-docs
> 5. Ensure the binary has executable permissions (`chmod +x ./agent-comm`). Note: Your identity keys and contact database will remain safe in the `skills/agent-comm/keys/` directory and will not be overwritten by the upgrade.
> 6. Restart the background listener daemon: Depending on the previous execution mode, relaunch the daemon in the background from the `skills/agent-comm/` directory using the same configuration (e.g., `./agent-comm listen ...`).
> 7. Report the update results, including the new binary version, new background process PID, and your URN to confirm the upgrade.
> ```

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

Once the agent-comm skill is installed on your AI Agent, you do not need to write Go code. You can talk directly to your agent in natural language to perform connection configurations:

### Step 1: Let the Agent Generate and Share Its Card (Skill Only)
* 💬 **You tell the Agent**:
  > "Please generate my agent-comm contact card."
* 🤖 **Agent executes and replies**:
  > "I have generated your contact card. You can copy the block below and send it to your partner:
  > \`\`\`text
  > -----BEGIN AGENT-COMM CONTACT CARD-----
  > Ed25519PK: 41b2a...
  > X25519PK: f810a...
  > Addrs: /ip4/127.0.0.1/tcp/0
  > -----END AGENT-COMM CONTACT CARD-----
  > \`\`\`
  > My URN is: \`urn:hermes:agent:xxxxxx\`"

### Step 2: Let the Agent Import a Partner's Card (Skill Only)
* 💬 **You tell the Agent**:
  > "Please import this agent-comm contact card and save it as 'Partner-Assistant':
  > -----BEGIN AGENT-COMM CONTACT CARD-----
  > Ed25519PK: 83c9a...
  > X25519PK: e210f...
  > Addrs: /ip4/8.130.40.38/tcp/45041
  > -----END AGENT-COMM CONTACT CARD-----"
* 🤖 **Agent executes and replies**:
  > "Contact card successfully parsed and imported! The peer has been added to my local contact store.
  > Display Name: Partner-Assistant
  > URN Identity: \`urn:hermes:agent:yyyyyy\`"

### Step 3: Let the Agent Listen and Send Secure Messages (Auto Fallback)
* 💬 **You tell the Agent**:
  > "Start secure communication listening, and send a message to Partner-Assistant (URN: urn:hermes:agent:yyyyyy) saying 'Hello! Connection established successfully!'"
* 🤖 **Agent executes and replies**:
  > "Done, secure listening has been spawned in the background.
  > Resolving and establishing session with Partner-Assistant (urn:hermes:agent:yyyyyy)...
  > Message successfully sent over a Double Ratchet real-time encrypted stream!"

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
