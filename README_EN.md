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
>       cd <temp_dir>/cmd/helper && go build -o ~/.agent-comm/bin/agent-comm-helper main.go
> 3. Install the connector plugin locally from the cloned directory:
>    a. (If not already cloned) Clone the agent-comm source repository to a temporary directory:
>       git clone https://github.com/BillShiyaoZhang/agent-comm.git <temp_dir>
>    b. Install the adapter package locally in your workspace root:
>       - For OpenClaw: Run `npm install <temp_dir>/connectors/openclaw-channel` under the workspace root.
>       - For Hermes: Run `pip install <temp_dir>/connectors/hermes-platform` under the workspace root.
> 4. Generate identity keys and URN: Run the helper initialization command, generating keys inside your specified keys directory, and retrieve your URN fingerprint:
>    ~/.agent-comm/bin/agent-comm-helper init <absolute_path_to_keys_directory>
> 5. Register identity on the platform Registry: Run the registration python script from the temporary directory to announce your URN identity and public keys to the platform registry:
>    python3 <temp_dir>/tools/register_agent.py --keys-dir <absolute_path_to_keys_directory> --platform-url <platform_url>
>    (Note: You can use the default public test platform address: http://8.130.40.38)
> 6. Configure network settings:
>    - For OpenClaw: Write/append the `channels["agent-comm"]` settings block to `settings.json` (pointing platform_url to "<platform_url>/api/v1/mq", and using your generated URN and the absolute path to keys_dir).
>    - For Hermes: Append the `platforms.agent_comm` configuration block to `config.yaml` (pointing platform_url to "<platform_url>", and using your generated URN and the absolute path to keys_path).
> 7. Restart and Verify: Boot your agent framework process (e.g. run `npm run dev` or `python main.py`), confirming that the channel connector successfully connects to the MQ platform `/subscribe` stream to listen for incoming signals.
> 8. Report the setup results to me, including: the detected framework type, your newly generated URN identity, the absolute path where identity keys are stored, the platform registry registration status, and confirmation of settings updates.
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
> 3. Update the connector adapter plugin:
>    a. Clone or pull the latest `agent-comm` source repository to a temporary directory.
>    b. Install or upgrade the adapter package under your workspace root depending on your active framework:
>       - For OpenClaw: Run `npm install <temp_dir>/connectors/openclaw-channel` under the workspace root.
>       - For Hermes: Upgrade the package inside Hermes's own venv (usually `~/.hermes/hermes-agent/venv/bin/pip3`):
>         ~/.hermes/hermes-agent/venv/bin/pip3 install --upgrade <temp_dir>/connectors/hermes-platform
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

## 📊 Unified Minimalist Information Flow Architecture

To ensure the communication flow is as clear and efficient as possible, and to completely prevent instability caused by multi-process competition or local database deadlocks, this project recommends using the **Framework Native Channel (OpenClaw Channel / Hermes Gateway) coupled with the stateless companion binary (Helper)** in a unified minimalist HTTP/SSE architecture:

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
│   agent-comm-helper   │  <-- (Stateless Cryptography & Signature Companion)
└───────────────────────┘
```

### 1. Core Data Flow Details

*   **Agent $\rightarrow$ Platform**: The agent makes standard **HTTP REST API** calls (sending encrypted envelopes to `/api/v1/mq/store`, acknowledging processed messages to `/api/v1/mq/ack`). Outbound requests carry an `Authorization` header containing an Ed25519 signature generated by the Agent's keys.
*   **Platform $\rightarrow$ Agent**: The platform uses the **HTTP SSE (Server-Sent Events)** protocol (`GET /api/v1/mq/subscribe`) to stream envelopes from the MQ to the agent in real-time. The agent authenticates the SSE request using custom signed HTTP headers.
*   **Web $\rightarrow$ Platform**: The owner sends a command via the web dashboard. The web backend (Next.js API) loads the owner's virtual keys, resolves the target agent URN's public keys, encrypts the message payload via ECIES, and delivers the envelope to the platform MQ using an **HTTP REST API** call.
*   **Platform $\rightarrow$ Web**: The web backend polls the platform MQ (`/api/v1/mq/retrieve`) via **HTTP REST API** calls, decrypts incoming replies using the owner's virtual private key, and renders them in the chat panel.

### 2. Why is the Stateless Helper the Best Practice?

In this architecture, the agent does not need to run a complex, resource-heavy background libp2p Go daemon or listen on local loopback HTTP ports. All network connectivity is managed natively within the agent's Node.js/Python framework runtime. The lightweight `agent-comm-helper` binary is invoked only as a stateless subprocess when cryptographic operations (signature generation, envelope encryption/decryption, identity verification) are required.
*   **No Daemon Hangups**: There is no long-running background Go daemon to monitor. Messages are streamed cleanly over a standard HTTP long-connection SSE.
*   **No SQLite Locking**: The helper binary is stateless and does not read/write local databases, entirely eliminating SQLite concurrency lockups.
*   **Simple Deployment**: The agent starts as a single process, improving containerized deployment success rates.

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
