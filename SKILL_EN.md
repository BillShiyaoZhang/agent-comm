---
name: agent-comm
description: >
  Hybrid P2P encrypted agent messaging SDK: libp2p + DHT + Double Ratchet + Platform.
  Project: <your_workspace_path>/agent-comm/
  Activate when: You need to set up secure agent-to-agent communication, import/export contact cards, send encrypted messages, or listen for messages via the Go agent-comm SDK.
---

# agent-comm — Agent Reference Manual 🤖

This manual is written for **AI Agents** (like yourself) using the `agent-comm` skill. It provides actionable instructions, API guides, and code templates to help you successfully achieve secure communications.

When integrating, please differentiate between **Standalone Skill (SDK) Local Features** and **Platform-Assisted Features** utilizing the cloud service **[agent-comm-platform](https://github.com/nousresearch/agent-comm-platform)**.

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
  1. **Discovery**: Concurrent query on the local Kademlia DHT (**Standalone Skill**) and the platform's Super Registry (**Requires [agent-comm-platform](https://github.com/nousresearch/agent-comm-platform)**).
  2. **Direct Dial**: Preferred TCP/QUIC connection established directly to target (**Standalone Skill**) executing a local Double Ratchet session.
  3. **Relay Traversal**: Auto-fallback to NAT tunnel forwarding via platform's Relay v2 nodes (**Requires [agent-comm-platform](https://github.com/nousresearch/agent-comm-platform)**).
  4. **MQ Cache**: Auto-fallback to blind-storing Double Ratchet envelopes on the platform's MQ offline queue if the receiver is offline (**Requires [agent-comm-platform](https://github.com/nousresearch/agent-comm-platform)**).

### Goal 4: Listen and Consume Messages (Requires Platform for Offline Sync)
* **Action**: Call [OnMessage](agent/agent.go#L205-L208).
* **Under the Hood**:
  - Registers a protocol handler to receive real-time Double Ratchet streams locally (**Standalone Skill**).
  - Spawns a background polling poller to retrieve envelopes from the platform's MQ offline mailbox, decrypting payloads locally and issuing `Ack` destructions back to the relay server (**Requires [agent-comm-platform](https://github.com/nousresearch/agent-comm-platform)**).

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

## ⚠️ Important Agent Gotchas (Read Before Executing)

1. **Unique Identity Directories**: If you run multiple agent nodes on the same local machine (e.g., during tests), you **must** assign them different `KeysDir` and `DBPath` values in the [Config](agent/config.go) struct. If they share the same directory, their public identity key and PeerID will collide, resulting in network errors.
2. **Explicit Decryption Target Conversion**: Inside incoming message handlers in [agent/handler.go](agent/handler.go), payloads decrypted from raw network streams must be explicitly cast to string format via `string(plaintext)` before being passed to upper-level application layers.
3. **MQ Envelope Acknowledgment (Platform Dependency)**: When receiving offline messages through the background MQ poller in [agent/handler.go](agent/handler.go#L48-L77), ensure that once the envelope is successfully decrypted, you issue `a.MQClient.Ack` using a slice of message IDs (e.g., `[]string{env.MessageId}`) to destroy the message on the platform relay.
4. **No `patch.go` Usage**: Do not search for or use `patch.go` in your execution path. That file has been deprecated; use the high-level API wrapper in [agent/agent.go](agent/agent.go) directly.
