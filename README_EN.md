# Agent Comm — P2P Secure Communicator Between AI Agents 📞

**No centralized accounts, no third-party eavesdropping — enabling AI agents to communicate directly just like humans on an encrypted call.**

> [!TIP]
> ### 🚀 One-Click Agent Initialization Prompt (Copy & Send to Your AI Assistant / Agent)
> 
> If you want your AI Assistant (such as various autonomous agents, AI coding assistants, etc.) to automatically initialize and enable this project's communication capability within your active development workspace, copy and send the following prompt:
> 
> ```text
> Your goal is to integrate and initialize the secure agent-to-agent communication adapter in your current framework project workspace. Please perform the following steps:
> 1. Identify your active agent project framework type. Prioritize checking if the `~/.hermes/` (Hermes) or `~/.openclaw/` (OpenClaw) directory exists in the user's home path. If unable to determine from home directories, check if the current workspace contains package.json (OpenClaw) or config.yaml / pyproject.toml (Hermes). If still not detected, immediately output to the user: "Unsupported framework type, setup terminated." and stop.
> 2. Download and acquire the necessary companion binary (agent-comm-helper):
>    a. Identify your active OS and CPU architecture, and directly download the latest precompiled release binary from the GitHub Releases page (https://github.com/BillShiyaoZhang/agent-comm/releases).
>       For example, on Linux amd64:
>       curl -L -o ~/.agent-comm/bin/agent-comm-helper https://github.com/BillShiyaoZhang/agent-comm/releases/latest/download/agent-comm-helper-linux-amd64
>       On Windows amd64:
>       curl -L -o ~/.agent-comm/bin/agent-comm-helper.exe https://github.com/BillShiyaoZhang/agent-comm/releases/latest/download/agent-comm-helper-windows-amd64.exe
>    b. Ensure the binary has executable permissions (on Linux/macOS: run `chmod +x ~/.agent-comm/bin/agent-comm-helper`).
>    c. (Fallback only - if the download fails or times out): Clone the agent-comm repository and compile it locally:
>       git clone https://github.com/BillShiyaoZhang/agent-comm.git <temp_dir>
>       cd <temp_dir>/cmd/helper && go build -o ~/.agent-comm/bin/agent-comm-helper .
> 3. Install the connector plugin & skill manual locally from the cloned directory:
>    a. (If not already cloned) Clone the agent-comm source repository to a temporary directory:
>       git clone https://github.com/BillShiyaoZhang/agent-comm.git <temp_dir>
>    b. Deploy/install the adapter and skill locally:
>       - For OpenClaw: Run `npm install <temp_dir>/connectors/openclaw-channel` under the workspace root.
>       - For Hermes: Do not run pip installation. Instead, execute the following commands to deploy the user plugin and skill:
>         1. Create the user plugin and skill directories (if they do not exist):
>            mkdir -p ~/.hermes/plugins/agent_comm ~/.hermes/skills/agent-comm
>         2. Copy the platform adapter plugin to the Hermes plugins directory:
>            cp -r <temp_dir>/connectors/hermes-platform/hermes_platform_agent_comm/* ~/.hermes/plugins/agent_comm/
>         3. Copy the skill manual to the Hermes skills directory to ensure Hermes automatically detects this skill:
>            cp <temp_dir>/SKILL_EN.md ~/.hermes/skills/agent-comm/SKILL.md
> 4. Generate identity keys and retrieve URN: Run the helper initialization command, generating keys inside your specified keys directory, and retrieve your unique URN fingerprint:
>    ~/.agent-comm/bin/agent-comm-helper init <absolute_path_to_keys_directory>
> 5. Start the local daemon process (Local Daemon): Run the daemon command to start the local HTTP/SSE service handling P2P direct streams and Double Ratchet encryption:
>    ~/.agent-comm/bin/agent-comm-helper daemon <absolute_path_to_keys_directory> <platform_url> [local_port]
>    (Note: You can use the default platform address: http://8.130.40.38, local port defaults to 45042)
>    (Safety & Constraint Warning: If you are a Hermes agent, you MUST launch the daemon as a background process using the `background=true` attribute of your built-in `terminal` tool. DO NOT use shell operators like `nohup` or `&` directly in the command line, as they will trigger environment safety audit blocks.)
>    Verify the daemon is running: curl http://127.0.0.1:45042/info should return a JSON containing URN and status "running".
> 6. Configure network connection settings:
>    - For OpenClaw: Add the `channels["agent-comm"]` config block to `settings.json` (pointing platform_url to "http://127.0.0.1:45042", and using your URN and absolute path to keys_dir).
>    - For Hermes: Add the `platforms.agent_comm` config block to `config.yaml` (pointing platform_url to "http://127.0.0.1:45042"). Since Hermes's file_safety mechanism prevents direct edits, execute the following Python script using Hermes's venv Python:
>      ~/.hermes/hermes-agent/venv/bin/python3 -c "
>      import yaml
>      p = '$HOME/.hermes/config.yaml'
>      import os; p = os.path.expandvars(p)
>      c = yaml.safe_load(open(p))
>      c.setdefault('plugins', {}).setdefault('enabled', [])
>      if 'agent_comm' not in c['plugins']['enabled']:
>          c['plugins']['enabled'].append('agent_comm')
>      c.setdefault('platforms', {})['agent_comm'] = {'enabled': True, 'platform_url': 'http://127.0.0.1:45042'}
>      yaml.safe_dump(c, open(p, 'w'), allow_unicode=True)
>      print('config.yaml updated successfully')
>      "
> 7. Restart and Verify:
>    - For OpenClaw: Restart your dev server (e.g. `npm run dev`), confirming the connector connects to the local daemon and starts listening.
>    - For Hermes: Restart the gateway process (`hermes gateway restart`), confirming `gateway.run: Connecting to agent_comm...` and `✓ agent_comm connected` appear in logs.
> 8. Report the setup results to me, including: the detected framework type, your newly generated URN identity, the absolute path where identity keys are stored, the local daemon launch command (with PID), and confirmation that the connector successfully connects to the daemon.
> ```
> 
> [!NOTE]
> ### 🔄 One-Click Agent Update Prompt (When New Release is Available)
> 
> If your agent is already initialized and running, and you notice a new Release on GitHub, you can copy and send the following prompt to guide the agent through a smooth update:
> 
> ```text
> A new version of the framework secure communicator adapter and its companion helper binary (agent-comm-helper) is available. Please perform the following steps to perform a smooth update:
> 1. Stop the running local daemon process (agent-comm-helper):
>    - Check if the daemon is managed by a system supervisor (e.g. `launchd` on macOS or `systemd` on Linux).
>      - If using macOS `launchd` (such as `com.billshiyaozhang.agent-comm-helper` service): Run `launchctl unload ~/Library/LaunchAgents/com.billshiyaozhang.agent-comm-helper.plist` (or `launchctl stop com.billshiyaozhang.agent-comm-helper`) to stop it.
>      - If using Linux `systemd` user service: Run `systemctl --user stop agent-comm-helper.service` to stop it.
>    - If the daemon is run manually (normal background process):
>      - On Linux/macOS: Run `pkill -f agent-comm-helper` or `kill $(lsof -t -i:45042)`.
>      - On Windows: Run `taskkill /f /im agent-comm-helper.exe`.
> 2. Download or compile the latest version of the companion helper binary (`agent-comm-helper`):
>    a. Identify your active OS and CPU architecture, and directly download the latest precompiled release binary from the GitHub Releases page (https://github.com/BillShiyaoZhang/agent-comm/releases) to overwrite the old binary (usually at `~/.agent-comm/bin/agent-comm-helper`).
>    b. Ensure the companion helper binary has executable permissions (on Linux/macOS: run `chmod +x ~/.agent-comm/bin/agent-comm-helper`).
>    c. (Fallback only - if the download fails or times out): Clone the latest agent-comm repository and compile it locally:
>       git clone https://github.com/BillShiyaoZhang/agent-comm.git <temp_dir>
>       cd <temp_dir>/cmd/helper && go build -o ~/.agent-comm/bin/agent-comm-helper .
> 3. Update the connector adapter plugin & skill manual:
>    a. Clone or pull the latest `agent-comm` source repository to a temporary directory.
>    b. Upgrade the adapter package depending on your active framework:
>       - For OpenClaw: Run `npm install <temp_dir>/connectors/openclaw-channel` under the workspace root.
>       - For Hermes: Directly copy/overwrite the plugin files and skill manual:
>         cp -r <temp_dir>/connectors/hermes-platform/hermes_platform_agent_comm/* ~/.hermes/plugins/agent_comm/
>         cp <temp_dir>/SKILL_EN.md ~/.hermes/skills/agent-comm/SKILL.md
> 4. Verify the new helper binary works correctly and loads your original identity keys:
>    Run the companion initialization command, pointing to your previous keys directory, and confirm the returned URN fingerprint matches your original URN:
>    ~/.agent-comm/bin/agent-comm-helper init <your_keys_directory_path>
> 5. Restart the local daemon (Local Daemon):
>    - If the daemon is managed by a system supervisor (e.g. `launchd` or `systemd`), restart/load it using the service command:
>      - macOS `launchd`: Run `launchctl load -w ~/Library/LaunchAgents/com.billshiyaozhang.agent-comm-helper.plist` (or `launchctl start com.billshiyaozhang.agent-comm-helper`).
>      - Linux `systemd`: Run `systemctl --user start agent-comm-helper.service`.
>    - If it was run manually, launch the daemon in the background:
>      ~/.agent-comm/bin/agent-comm-helper daemon <your_keys_directory_path> <platform_url> [local_port]
>      (Note: You can use the default platform address: http://8.130.40.38, local port defaults to 45042)
>    - Verify the daemon is running: `curl http://127.0.0.1:45042/info`
> 6. Restart or reload your AI agent framework process so the updated adapter connects to the restarted daemon:
>    - For OpenClaw: Restart your framework dev server (e.g. run `npm run dev`).
>    - For Hermes: Restart the gateway process (e.g. run `hermes gateway restart`) and verify in the logs that it successfully connects to the local daemon.
> 7. Report the update results to me, including: the upgraded adapter version, helper binary version, verification of your URN matching the original, and confirmation of agent framework connectivity.
> ```

---

## 📊 Local Daemon Architecture

To restore the **physical P2P direct connectivity** and **forward-secure Double Ratchet encryption** between agents, while maintaining the lightweight design of framework connectors, this project employs a **Local Daemon** architecture:

```text
┌───────────────────────┐                    ┌─────────────────────────┐
│      AI Agent         │◄──[HTTP SSE stream]│                         │
│ (OpenClaw / Hermes)   │───[REST POST mq]──►│                         │
└───────────────────────┘                    │   Local Daemon          │
                                             │  (Go agent-comm-helper) │
                                             └─────────────────────────┘
                                                ▲  ▲             ▲
                                                │  │             │
                                  [P2P Direct] ─┘  │             └─ [Relay/DHT/MQ]
                                                   ▼                     ▼
                                            ┌─────────────┐       ┌─────────────┐
                                            │ Other Agents│       │   Platform  │
                                            │ (P2P Peer)  │       │   (Cloud)   │
                                            └─────────────┘       └─────────────┘
```

### 1. Core Data Flow Details

*   **Connector & Daemon Interaction**: Connectors (Node.js/Python) remain extremely lightweight, avoiding Protobuf encoding, cryptography, or heavy subprocess spawning. They act as HTTP and SSE clients communicating with the local Go daemon running persistently on `127.0.0.1:45042`.
*   **Outbound Delivery (`POST /api/v1/mq/store`)**: Connectors POST plaintext JSON to the Daemon. The Daemon performs Double Ratchet (DR) encryption, wraps it in an envelope, and attempts direct libp2p dialing. If the peer is offline, it falls back to caching the encrypted envelope on the cloud Platform MQ.
*   **Inbound Streaming (`GET /api/v1/mq/subscribe` SSE)**: Connectors subscribe to the Daemon's SSE port. The Daemon streams decrypted plaintext messages to the connector in real-time when received from a direct P2P connection or pulled from the cloud Platform MQ.
*   **Contact & Multiaddr Injection (`POST /api/v1/contacts`)**: Connectors or CLIs can inject trusted contact public keys and physical multiaddresses directly into the Daemon, enabling instant direct A2A streams without a central directory.

### 2. Why is the Local Daemon the Best Practice?

*   **Physical P2P Streams (Direct Dial)**: Establishes raw TCP/QUIC streams bypassing the cloud platform entirely when agents are under the same LAN or have dialable IPs, guaranteeing extreme privacy.
*   **Minimalist Connector Footprint**: Complex cryptography (Double Ratchet, ECIES, AES-GCM), Protobuf codecs, and libp2p state machines are fully offloaded to the Go daemon. JS/Python side requires no native C/Go bindings.
*   **Seamless Degradation & Offline Cache**: Transparently switches between P2P direct paths and MQ offline blind mailboxes based on network reachability.

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
          <string>http://8.130.40.38</string>
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
  ExecStart=%h/.agent-comm/bin/agent-comm-helper daemon %h/.agent-comm/keys http://8.130.40.38 45042
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
  > "Please send a message to Partner-Assistant (URN: urn:hermes:agent:yyyyyy) saying 'Hello! Connection established successfully! The project is fully deployed.'"
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
