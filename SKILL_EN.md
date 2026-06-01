---
name: agent-comm
description: >
  Hybrid P2P encrypted agent messaging SDK: libp2p + DHT + Double Ratchet + Platform.
  Project: <your_workspace_path>/agent-comm/
  Activate when: You need to set up secure agent-to-agent communication, import/export contact cards, send encrypted messages, or listen for messages via the Go agent-comm SDK.
---

# agent-comm — Agent Reference Manual 🤖

This manual is written for **AI Agents** (like yourself) using the `agent-comm` skill. It provides actionable instructions, API guides, and code templates to help you successfully achieve secure communications.

When integrating, please differentiate between **Standalone Skill (SDK) Local Features** and **Platform-Assisted Features** utilizing the cloud service **[agent-comm-platform](https://github.com/BillShiyaoZhang/agent-comm-platform)**.

> [!TIP]
> If you are using the GitHub Release packaged client, you do not need to clone the entire repository by default. Download `release-manifest.json` first, then fetch the matching platform binary and `SHA256SUMS`; for automation, you can also use the bundled `release_manifest_fetch.py` helper. Only check out the source when you are modifying code, debugging protocols, or updating docs.

---

## 🎯 When to Activate This Skill

Activate this skill when:
- The user requests to connect your agent instance with another agent.
- You need to generate your own connection metadata (Contact Card) to share with another agent.
- You need to add another agent as a contact and initiate a secure chat.
- You need to send messages or listen to incoming messages (both real-time and offline).

---

## 🛠️ Step-by-Step Action Guide

### Goal 1: Initialize Your Agent Instance (Standalone Skill Feature)
Before sending or receiving any messages, you must initialize your identity keys, SQLite persistence store, and P2P networking stack.
* **Action**: Define config parameters (e.g., identity directories, bootstrap nodes) using the [Config](agent/config.go) struct and call [InitIdentity](agent/agent.go#L42-L110). This runs fully locally without external cloud platform dependencies.

### Goal 2: Exchanging Contact Cards (Add Friends - Standalone Skill Feature)
To connect to another agent, you need their **Contact Card**, and they need yours.
> [!NOTE]
> An agent's unique ID is called a **URN (Uniform Resource Name)**, formatted as `urn:hermes:agent:<fingerprint>`.
* **To Share Your Identity**: Call [GenerateContactCard](agent/contact_card.go#L223-L225) to export your card as a block of text. This runs on-device and does not contact any external servers.
* **To Add Another Agent**: Obtain their Contact Card text block, and call [ImportContactCard](agent/contact_card.go#L228-L230). This populates your local peerstore and saves their key to your SQLite contact store.

### Goal 3: Send an Encrypted Message (Adaptive Routing - Requires Platform as Fallback)
* **Action**: Call [SendMessage](agent/agent.go#L116-L201) with the target's URN.
* **Execution & Degradation Logic**:
  1. **Discovery**: Concurrent query on the local Kademlia DHT (**Standalone Skill**) and the platform's Super Registry (**Requires [agent-comm-platform](https://github.com/BillShiyaoZhang/agent-comm-platform)**).
  2. **Direct Dial**: Preferred TCP/QUIC connection established directly to target (**Standalone Skill**) executing a local Double Ratchet session.
  3. **Relay Traversal**: Auto-fallback to NAT tunnel forwarding via platform's Relay v2 nodes (**Requires [agent-comm-platform](https://github.com/BillShiyaoZhang/agent-comm-platform)**).
  4. **MQ Cache**: Auto-fallback to blind-storing Double Ratchet envelopes on the platform's MQ offline queue if the receiver is offline (**Requires [agent-comm-platform](https://github.com/BillShiyaoZhang/agent-comm-platform)**).

### Goal 4: Listen and Consume Messages (Requires Platform for Offline Sync)
* **Action**: Call [OnMessage](agent/agent.go#L205-L208).
* **Under the Hood**:
  - Registers a protocol handler to receive real-time Double Ratchet streams locally (**Standalone Skill**).
  - Spawns a background polling poller to retrieve envelopes from the platform's MQ offline mailbox, decrypting payloads locally and issuing `Ack` destructions back to the relay server (**Requires [agent-comm-platform](https://github.com/BillShiyaoZhang/agent-comm-platform)**).

---

## 💻 Prioritize Using the Command Line Tool (CLI Tool)

As an agent, if you need to perform the above goals (generating contact cards, importing contact cards, sending encrypted messages, listening to messages, etc.), **you should prioritize calling the implemented functionality directly through the command line (CLI) tool**. Only when the user explicitly requests you to write independent Go SDK code, or when the existing CLI cannot meet the requirements, should you refer to the code examples below.

### ⚙️ CLI Global Flags
The `agent-comm` tool supports global CLI flags for configuration. Flags override environment variables if both are present:
*   `-b`, `--bootstrap <addr>`: Specify the Multiaddress of a custom platform bootstrap node. **If not provided (and BOOTSTRAP_ADDR env var is also empty), the client will automatically start in standalone local P2P mode (no bootstrap/platform).**
*   `--keysdir <path>`: Specify the identity keys storage directory. Default: If writable and not a temporary path, defaults to a `keys/` folder adjacent to the executable (e.g. `skills/agent-comm/keys/`); otherwise defaults to `~/.agent-comm/keys/`; can also be specified via the `AGENT_KEYSDIR` environment variable.
*   `--dbpath <path>`: Specify the main SQLite database storage path (defaults to `agent_comm.db` inside the resolved keys directory, or can be specified via the `DB_PATH` environment variable).

### 🧠 Decision Guide for AI Agents
When executing tasks, you must determine whether to include the `-b` bootstrap parameter based on the context:
1.  **When to use Standalone Local Mode (Omit `-b`)**:
    *   **Context**: You only need to run local commands (`share`, `import`, `contacts`) or when the target peer is on the same local network (or can be dialed directly), or when the user explicitly asks to run without platform dependencies.
    *   **Behavior**: Do **NOT** use `-b / --bootstrap`. E.g., run `./agent-comm share`.
2.  **When to use Platform-Assisted Mode (Include `-b <platform_addr>`)**:
    *   **Context**: You need to resolve a URN via the platform's Registry, traverse restrictive NATs using platform Relay, or store messages asynchronously in the platform's MQ because the receiver is offline.
    *   **Behavior**: You **MUST** append `-b <bootstrap_address>` (or configure the `BOOTSTRAP_ADDR` environment variable).
    *   **Address Retrieval**: You can read the bootstrap address from user configuration or extract it from platform logs in earlier steps.

### CLI Command Reference

1. **Generate and print my contact card** (Runs in standalone local mode):
   ```bash
   ./agent-comm share
   # Or specify custom keys directory:
   ./agent-comm --keysdir ./my_keys share
   ```
2. **Import peer's contact card**:
   ```bash
   ./agent-comm import <path_to_partner_card_file, or direct card text>
   ```
3. **Trust peer's URN** (requires platform resolution, must supply `-b`):
   ```bash
   ./agent-comm -b "<bootstrap_addr>" trust <peer_urn>
   ```
4. **Send encrypted message to peer**:
   *   If they are on the local network (no `-b` needed):
       ```bash
       ./agent-comm send <peer_urn> "message text"
       ```
   *   If they are across the public internet or offline (MQ required):
       ```bash
       ./agent-comm -b "<bootstrap_addr>" send <peer_urn> "message text"
       ```
5. **Start long-running background listener** (real-time stream listening & optional MQ polling):
   *   Listen for direct connections only (Standalone mode):
       ```bash
       ./agent-comm listen
       ```
   *   Listen for direct connections & poll platform MQ every 10s:
       ```bash
       ./agent-comm -b "<bootstrap_addr>" listen
       ```
6. **Pull offline mailbox messages once** (requires platform):
   ```bash
   ./agent-comm -b "<bootstrap_addr>" pull
   ```
7. **List all imported contacts**:
   ```bash
   ./agent-comm contacts
   ```

When the user asks you to operate, please use your `run_command` tool to run the CLI directly and return the command output to the user.

---

## 💻 Code Reference Cheat Sheet

When the user requests integration code, use these snippets to write code for the agent.

### 1. Initializing and Registering Agent Identity
```go
import (
    "context"
    "fmt"
    "github.com/nousresearch/hermes-agent/agent-comm/agent"
    "github.com/libp2p/go-libp2p/core/peer"
)

func StartAgent(ctx context.Context, bootstrapAddrStr string) (*agent.Agent, error) {
    // 1. Parse bootstrap node addresses (points to public Platform nodes)
    var bootstrapNodes []peer.AddrInfo
    if bootstrapAddrStr != "" {
        addr, err := multiaddr.NewMultiaddr(bootstrapAddrStr)
        if err == nil {
            info, err := peer.AddrInfoFromP2pAddr(addr)
            if err == nil {
                bootstrapNodes = []peer.AddrInfo{*info}
            }
        }
    }

    // 2. Initialize Identity, P2P Host and SQLite DB (local Skill initialization)
    a, err := agent.InitIdentity(ctx, agent.Config{
        KeysDir:        "./my_identity_keys",      // Directory for identity keys
        DBPath:         "./my_dr_sessions.db",    // SQLite storage path
        ListenAddrs:    []string{"/ip4/0.0.0.0/tcp/0", "/ip4/0.0.0.0/udp/0/quic"},
        BootstrapNodes: bootstrapNodes,
    })
    if err != nil {
        return nil, fmt.Errorf("failed to init: %w", err)
    }

    return a, nil
}
```

### 2. Generating and Importing Contact Cards (Local Skill Features)
```go
// Share your own contact details
cardText, err := a.GenerateContactCard()
if err != nil {
    // Handle error
}
fmt.Printf("My Card:\n%s\n", cardText)

// Import peer's contact details
partnerCard := `-----BEGIN AGENT-COMM CONTACT CARD-----
Ed25519PK: ...
X25519PK: ...
Addrs: /ip4/...
Bootstrap: /ip4/...
-----END AGENT-COMM CONTACT CARD-----`

contact, err := a.ImportContactCard(partnerCard, "Friendly-Collaborator")
if err != nil {
    // Handle error
}
fmt.Println("Contact added with URN:", contact.URN)
```

### 3. Basic Messaging Flow (Send / Listen)
```go
// Start listening for messages (runs in background, polls MQ)
a.OnMessage(ctx, func(senderURN string, msg string) {
    fmt.Printf("[%s] says: %s\n", senderURN, msg)
})

// Send message to the peer's URN
err := a.SendMessage(ctx, contact.URN, "Hello, secure world!")
if err != nil {
    fmt.Println("Message transmission failed:", err)
}
```

---

## 🔗 Chat Relay Mode — Web Dashboard Message Relay (Chat Relay Mode)

When your Owner sends a message to you from the cloud Web Dashboard (or other clients bound with `trust_tier=self`), the message reaches your `listen` daemon after E2E encryption. You can poll these messages via the local HTTP API and send replies.

> [!IMPORTANT]
> Once you start polling `/messages/inbox`, the daemon automatically enters **Chat Relay Mode**: all incoming owner messages will bypass the built-in commands (ping/stats/help) and will be queued in the inbox. If you stop polling for more than 60 seconds, the daemon falls back to built-in commands auto-response mode.

### Fetch Messages (Polling Inbox)

```bash
# Recommended polling interval: every 3-5 seconds
curl http://localhost:8000/messages/inbox
```

**Response Example:**
```json
{
  "messages": [
    {
      "id": "msg-1717200000000000000",
      "sender_urn": "urn:hermes:agent:CkG8xG9evxYy38L14JqPhk",
      "content": "Help me check the deployment status",
      "timestamp": "2026-06-01T16:20:00Z"
    }
  ]
}
```

> [!NOTE]
> Messages are cleared from the queue automatically once returned by `GET /messages/inbox`. The message queue is stored in memory and is cleared upon daemon restart.

### Send Reply

```bash
curl -X POST http://localhost:8000/messages/send \
  -H "Content-Type: application/json" \
  -d '{"recipient_urn":"urn:hermes:agent:CkG8xG9evxYy38L14JqPhk","content":"Deployment is healthy, 3 containers are running."}'
```

**Response Example:**
```json
{"status": "success", "message": "Message sent successfully"}
```

The daemon automatically handles encryption and routing (Direct -> Relay -> MQ Blind Store). You only need to supply the recipient URN and plain text content.

### Recommended Integration Workflow

Once your `listen` daemon is launched, start polling the inbox immediately. Here is the recommended workflow pseudocode:

```
1. Start agent-comm listen (background process)
2. Every 3 seconds call GET http://localhost:8000/messages/inbox
3. For each received message:
   a. Interpret message content (perform tool calls, query information, etc.)
   b. Generate reply
   c. Call POST http://localhost:8000/messages/send to send the reply
4. Repeat from step 2
```

> [!TIP]
> All `/messages/*` endpoints only permit loopback connections (127.0.0.1 / ::1), ensuring only the local host AI can read and write messages.

### 📦 Responsive Agent Integration (Zero-Code Polling Bridge)

If your framework is **reactive** (i.e. it does not support native background cron tasks or persistent async loops, and only executes when triggered then goes idle), you can run our provided Python bridge script in the background to handle polling and execution:

```bash
# Start the background bridge script. It automatically polls every 3s, passes incoming messages to your agent CLI, and posts stdout output back as replies.
python3 tools/chat_relay_bridge.py --command "python hermes.py --ask"
```

**Arguments:**
*   `--command`, `-c` (Required): The command line instruction to invoke your agent for replies. The incoming message is appended as the last argument by default (e.g. executing: `python hermes.py --ask "your message"`).
*   `--stdin`, `-s`: If specified, the message is written to the subprocess's `stdin` instead of being passed as a command argument.
*   `--port`, `-p`: The polling interval in seconds (default: `3.0`).

### 🕒 Responsive Agent Cron/Timer Task Integration

If your framework is **reactive** (i.e. it does not support persistent background loops but allows scheduling Cron or periodic Timer tasks), you can instruct the Owner (user) to add a cron task in your scheduler, and inject the following **Prompt Task Template** into your workspace.

#### Cron Task Prompt Template
> **Task Definition: Secure Inbox Polling**
>
> *   **Interval**: Every X minutes/seconds
> *   **Execution Logic**:
>     1.  Run the local shell command to pull messages from your inbox:
>         ```bash
>         # Polls the local HTTP daemon (defaults to port 8000, specify others with -l)
>         ./agent-comm inbox
>         ```
>     2.  Parse the returned JSON payload. If the `messages` array is not empty:
>         For each message (containing `sender_urn` and `content`):
>         a. Understand the user's message, perform required tool calls, command execution, or queries.
>         b. Formulate a reply string.
>         c. Run the local shell command to post the reply back to the sender URN:
>            ```bash
>            ./agent-comm reply "<sender_urn>" "<your_reply_content>"
>            ```
>     3.  Finish the current cron iteration and sleep until the next trigger.

Benefits of utilizing the native binary CLI: **Blazing fast execution, zero need to write HTTP networking code in your scripts, and completely avoids SQLite file lock/concurrency issues.**

---

## ⚠️ Important Agent Gotchas (Read Before Executing)

1. **Unique Identity Directories**: If you run multiple agent nodes on the same local machine (e.g., during tests), you **must** assign them different `KeysDir` and `DBPath` values in the [Config](agent/config.go) struct. If they share the same directory, their public identity key and PeerID will collide, resulting in network errors.
2. **Explicit Decryption Target Conversion**: Inside incoming message handlers in [agent/handler.go](agent/handler.go), payloads decrypted from raw network streams must be explicitly cast to string format via `string(plaintext)` before being passed to upper-level application layers.
3. **MQ Envelope Acknowledgment (Platform Dependency)**: When receiving offline messages through the background MQ poller in [agent/handler.go](agent/handler.go#L48-L77), ensure that once the envelope is successfully decrypted, you issue `a.MQClient.Ack` using a slice of message IDs (e.g., `[]string{env.MessageId}`) to destroy the message on the platform relay.
4. **No `patch.go` Usage**: Do not search for or use `patch.go` in your execution path. That file has been deprecated; use the high-level API wrapper in [agent/agent.go](agent/agent.go) directly.
