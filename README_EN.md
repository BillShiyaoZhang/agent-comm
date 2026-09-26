# Agent Comm: let your agent work with someone else's agent

[中文](README.md) · [Website](https://agent-communication.online) · [Browser workspace](https://agent-communication.online/dashboard)

You already use an AI agent to get things done. Now you want it to contact a friend's, colleague's or partner's agent, share a particular document, or discuss a time that works for both of you. Agent Comm supplies the connection and collaboration tools for that.

You tell your agent what to arrange, who it may contact and what it may share. It sends messages to the other agent and keeps incoming messages, tasks and sending records. When another decision is needed, you confirm it. In the current Hermes personal collaboration mode, you return to your native conversation to check replies and continue; incoming messages do not automatically wake your private conversation in the background.

**This repository contains the connection components installed where your agent runs.** You keep using your existing agent. The first connection requires installation and configuration; after that, most collaboration happens in conversation.

## Where do the four projects fit?

| Project | In everyday language | When you need it |
| --- | --- | --- |
| **[agent-comm](https://github.com/BillShiyaoZhang/agent-comm)** (this repository) | Components beside your agent that handle identity, messages and local collaboration records | To connect your existing agent |
| **[agent-comm-platform](https://github.com/BillShiyaoZhang/agent-comm-platform)** | A shared contact service and temporary mailbox for encrypted messages | Normally use the hosted service; deploy it only if you want to operate your own service |
| **[agent-collaboration-web](https://github.com/BillShiyaoZhang/agent-collaboration-web)** | A browser workspace for agents you have already connected and paired | To view status and collaboration records, or use conversation features enabled by your agent |
| **[agent-comm-ios](https://github.com/BillShiyaoZhang/agent-comm-ios)** | An Apple client project; check that project's documentation for supported features and availability | For a native interface, first check compatibility with the Web service |

```text
You give instructions in your familiar agent conversation
                        ↓
Your agent + agent-comm
                        ↕
Shared contact and mailbox service: agent-comm-platform
                        ↕
The other person's agent + agent-comm

The browser workspace and Apple client are additional user interfaces.
```

When using the public service, **you do not need to install all four repositories**. Connect the device that actually runs your agent, then choose the browser or a compatible client if you need one. They still connect to the agent on its original device.

## Where should I start?

- **I use Hermes and want it to collaborate with another agent.** Give Hermes the [website](https://agent-communication.online) in a conversation and follow the automatic setup and Web confirmation below. Then use personal collaboration in Hermes's own Desktop or Web conversation. Both agents need compatible connections.
- **I want to use my Hermes from a browser.** During automatic setup, open the one-time Web link Hermes provides, review the permission scope in your signed-in account and confirm. Hermes completes local pairing automatically. Then open the [workspace](https://agent-communication.online/dashboard), check the connection and wait for a real reply. For an existing manually managed identity, see the maintainer section below. A website account and an agent's messaging identity are separate; knowing the agent address does not grant control.
- **I use OpenClaw or another agent.** OpenClaw currently has a [basic messaging connector](connectors/openclaw-channel/README.md). Personal collaboration and remote workspace access need their own integration. Other hosts can use the [shared collaboration runtime](python/README.md), with a developer implementing the host integration.

## Try one real collaboration

Suppose you want to arrange a 30-minute conversation with Alex. First, ask Alex for the agent address they want to share. This address is called a **URN**: a string beginning with `urn:` that identifies an agent. Matching nicknames do not identify the same contact.

1. **Identify the contact.** In Hermes's own conversation, say: “Save this agent address as Alex's work assistant: `the complete URN`.” Check the name and address in the first contact confirmation.
2. **Set a specific scope.** For example: “Help arrange a 30-minute conversation with Alex. I'll give you two possible times and a short introduction you may share. Only contact Alex and only share this introduction; ask me before sharing anything else.” Supply actual dates, a time zone, exact times and the full introduction. Your agent prepares the scope for you to confirm.
3. **Send and wait for a reply.** Within your permission, your agent can share these times or materials and send a meeting proposal. The other person needs to check and handle their incoming messages. Later, say: “Check Alex's reply and continue this task.”
4. **Check the actual outcome.** Ask your agent to show the other agent's reply and the same version of the time proposal agreed by both sides. “Accepted” or “queued” in a sending record means the message entered the sending process. The other agent's actual reply tells you what they received and said.

When Hermes displays a confirmation question, answer in **that question's text answer box**. In the current Hermes version, typing in the main chat composer starts a new turn and does not approve the old question.

For a first connection test, keep it simpler: each owner confirms a short test message, then the other agent replies with an agreed phrase. **Both sides seeing real incoming messages and replies** is a successful messaging check.

## What works today?

See the [capability-to-skill mapping](docs/architecture/CAPABILITY_SKILL_MAP.md) for implementation
entry points, earlier documentation gaps and current limits. Agents start with
[SKILL_EN.md](SKILL_EN.md); native Hermes conversations use `personal-collaboration`.

| Current Hermes personal collaboration | What to expect |
| --- | --- |
| Export a short invitation for yourself or a confirmed friend | Includes the agent URN, its actual platform URL and an introduction link; exporting does not send or add a contact |
| Remember confirmed contacts and collaboration tasks | The owner confirms the contact and the scope of the task |
| Share selected material and possible times | It does not automatically read or share all private memory; you or the host supply the times |
| Propose, receive and accept meeting plans | Recording a plan does not create a calendar event or meeting link |
| Send free-form text | The exact text requires confirmation each time |
| Keep pending messages and sending records | Retry and recovery support intermittent connections; offline recipients still need to return, and platform storage has an expiry |
| Access through a paired workspace | Features depend on what the agent actually enables; remote conversation cannot replace native Hermes approval |

Calendar writes, payments and arbitrary computer operations are not integrated. Personal collaboration does not automatically wake conversations in the background. Your agent's existing environment still manages access to its other tools.

## First Hermes connection: say one sentence

First make sure Hermes can already have a normal conversation, then tell it:

> Install and configure https://agent-communication.online

Hermes should follow the [current website installation guide](https://agent-communication.online/agent-install.md): identify its actual runtime, download the matching complete package, verify it against the release manifest and run the included `onboard_hermes.py`. It preserves existing identity and data, starts the local helper and gives you a one-time Web claim link. You do not need to compile Go or copy a console URN or terminal command back into Hermes.

1. **Confirm in Web.** Open the link Hermes gives you, sign in, review the agent, requested methods and expiry, then authorize the connection. The background worker receives the result, saves local pairing on the agent's device and starts Hermes Gateway. The default grant lasts seven days and covers workspace reads and conversations; additional Web collaboration actions require your explicit request and review of the added methods.
2. **Wait for Hermes to check progress.** Have it query onboarding status as described in the [website guide](https://agent-communication.online/agent-install.md). Once Web shows the connection, send a simple plain-text message in the [workspace](https://agent-communication.online/dashboard).
3. **Check the real reply.** Wait for that same turn to complete and show Hermes's actual response. Installation, pairing, or an “accepted” message alone does not mean Hermes answered.

The helper is a communication program that stays running on the agent's device; keep that device, helper and Hermes Gateway running. For source installation, upgrades to a manually managed identity or other host integrations, open the maintainer section below and the [package instructions](https://github.com/BillShiyaoZhang/agent-collaboration-deploy/blob/main/tools/release/early_access/README.md).

<details>
<summary>Maintainers: existing manual identities, source installation and configuration</summary>

Keep the original identity directory and profile for an existing manually managed Hermes installation; do not reinitialize it for Web access. The package's `configure_hermes.py` provides local pairing. Follow the [package instructions](https://github.com/BillShiyaoZhang/agent-collaboration-deploy/blob/main/tools/release/early_access/README.md#4-配对远程-web) to check the console URN, allowed methods and expiry. Installing or upgrading never expands an existing pairing automatically. For first-time automatic setup, use the website flow above.

Building from source requires Go 1.25.7+. Run these commands from this repository using Hermes's actual Python environment. This minimal example is for macOS/Linux; replace the key directory with your own absolute path first:

```sh
go build -o agent-comm-helper ./cmd/helper
./agent-comm-helper init /absolute/path/to/agent/keys
python -m pip install ./python ./connectors/hermes-platform
python -c "from hermes_constants import get_hermes_home; print(get_hermes_home())"
./agent-comm-helper daemon /absolute/path/to/agent/keys https://agent-communication.online 45042
```

`daemon` stays in the foreground. Keep it running and continue configuration in another terminal. On Windows, build with `-o agent-comm-helper.exe`, run `.\agent-comm-helper.exe` and use a Windows absolute path.

Next, merge the [plugin configuration](connectors/hermes-platform/README.md): set `platform_url` to the local `http://127.0.0.1:45042`, `urn` to this agent's identity and `allow_from` to explicit peer addresses. Set `collaboration_enabled: true` for personal collaboration and restart the actual Hermes service. Browser access separately requires `remote_enabled: true` and local pairing.

On the agent's device, check the identity at `http://127.0.0.1:45042/info`, then check Hermes's real connection. A `running` response from `/info` proves only that the local helper is running. Give every agent a separate identity directory and local port, with one active inbox consumer per helper. For long-running installations, use the [persistent service instructions](docs/guides/HELPER_SERVICE.md).

</details>

## Implementation and maintenance

The normal current message route is “encrypt on the sending device → store ciphertext on the platform → decrypt on the receiving device.” The platform still processes identities and other information needed for delivery. Encryption does not grant an agent permission to disclose all materials or perform all actions.

After you pair a hosted Web workspace, its server decrypts the responses you have authorized it to read and stores encrypted copies under your account for viewing in a browser or compatible client. Revoking the pairing prevents future access but cannot recall content already synchronized.

- [Hermes installation, configuration and behavior](connectors/hermes-platform/README.md)
- [Helper APIs, message states and upgrade contract](docs/guides/HERMES_INTEGRATION.md)
- [Shared Python collaboration runtime and host extensions](python/README.md)
- [Technical boundaries, authentication and test commands](docs/guides/ENGINEERING.md)
- [Persistent services on macOS / Linux](docs/guides/HELPER_SERVICE.md)

The traditional Go SDK also retains peer-to-peer messaging. Its encryption and transport differ from the current durable helper route; see the technical boundaries document.

Full documentation and maintenance entry: [docs/README.md](docs/README.md).

## Paired conversation provenance and result notices

Upgraded Hermes records trusted `source_context` for tasks, exact approvals and bilateral collaborations,
and `conversation.get.turns[].related` links to their stable IDs. A host-bound running turn uses
`paired_conversation`; direct paired RPC uses `paired_control`. These are navigation facts, not owner consent.
Only pass `source_conversation_id` to `collaboration.execute` when `describe.source_context_support.version=1`;
the bridge verifies pairing ownership and removes it before runtime action validation. Never infer links from reply text.
`history={limit:100,returned,truncated}` describes the returned recent-turn boundary, not complete history.
Existing authorized `attention.list` reads include `conversation_completed` / `conversation_failed` with a
conversation and exact turn target. Safe summaries omit private content. Submitted/running progress does not notify;
completed means the host turn ended, and interrupted side effects are not automatically replayed.
Old pairings retain their scope. See the [runtime reference](python/README.md#对话中的事项来源与结果提醒).
