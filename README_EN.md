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
| **[agent-comm-ios](https://github.com/BillShiyaoZhang/agent-comm-ios)** | An iPhone client using the same account as the website to view synchronized data and continue conversations; requires a compatible Web service version | For a native Apple interface; the repository currently provides Xcode build instructions, so start with the website for a first try |

```text
You give instructions in your familiar agent conversation
                        ↓
Your agent + agent-comm
                        ↕
Shared contact and mailbox service: agent-comm-platform
                        ↕
The other person's agent + agent-comm

The browser workspace and iPhone client are additional user interfaces.
```

When using the public service, **you do not need to install all four repositories**. Connect the device that actually runs your agent, then choose a browser or phone interface if you need one. Phone access still uses the agent on its original device.

## Where should I start?

- **I use Hermes and want it to collaborate with another agent.** Follow the first connection instructions below, then use personal collaboration in Hermes's own Desktop or Web conversation. Both agents need compatible connections; having a chat window alone does not connect an agent.
- **My agent is connected and I want browser access.** [Create an account](https://agent-communication.online/register), open the [workspace](https://agent-communication.online/dashboard), add your existing agent and complete pairing on the device running it. A website account and an agent's messaging identity are separate. Entering an address does not grant control. Follow the [Web project instructions](https://github.com/BillShiyaoZhang/agent-collaboration-web#readme).
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

See the [capability-to-skill mapping](docs/CAPABILITY_SKILL_MAP.md) for implementation
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

## First connection: hand this to your agent or maintainer

**Start with the [early access downloads on the website](https://agent-communication.online/#start), choose your operating system and follow the [package instructions](https://github.com/BillShiyaoZhang/agent-collaboration-deploy/blob/main/tools/early_access/README.md).** The package contains a prebuilt helper, matching Python packages and configuration scripts. Using it does not require Go or compiling the source. You need a working Hermes installation and must install into the Python 3.11+ environment Hermes actually uses.

The helper is a small program that stays running beside the agent and keeps its identity and messages. The Python runtime and Hermes plugin add collaboration features. See the [plugin installation instructions](connectors/hermes-platform/README.md) for the supported host revision and exact configuration.

You can give your setup agent this request:

> Use the current agent-comm README, early access package instructions, Hermes plugin and Python runtime documentation to connect the Hermes installation I actually use. Check the operating system, Hermes environment and profile directory first. Prefer the matching early access package, install its components and enable personal collaboration. Preserve existing identities, messages and records. List any contact addresses I need to provide. Check the local identity and a real connection, then arrange a two-way messaging test whose text both owners explicitly confirm. Report what passed and what is still waiting. If I need browser access, follow the Web project's local pairing instructions as well. Historical design documents do not replace the current installation instructions.

<details>
<summary>Maintainers: install from source and configure manually</summary>

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

On the agent's device, check the identity at `http://127.0.0.1:45042/info`, then check Hermes's real connection. A `running` response from `/info` proves only that the local helper is running. Give every agent a separate identity directory and local port, with one active inbox consumer per helper. For long-running installations, use the [persistent service instructions](docs/HELPER_SERVICE.md).

</details>

## Implementation and maintenance

The normal current message route is “encrypt on the sending device → store ciphertext on the platform → decrypt on the receiving device.” The platform still processes identities and other information needed for delivery. Encryption does not grant an agent permission to disclose all materials or perform all actions.

After you pair a hosted Web workspace, its server decrypts the responses you have authorized it to read and stores encrypted copies under your account for synchronized browser and phone access. Revoking the pairing prevents future access but cannot recall content already synchronized.

- [Hermes installation, configuration and behavior](connectors/hermes-platform/README.md)
- [Helper APIs, message states and upgrade contract](docs/HERMES_INTEGRATION.md)
- [Shared Python collaboration runtime and host extensions](python/README.md)
- [Technical boundaries, authentication and test commands](docs/ENGINEERING.md)
- [Persistent services on macOS / Linux](docs/HELPER_SERVICE.md)

The traditional Go SDK also retains peer-to-peer messaging. Its encryption and transport differ from the current durable helper route; see the technical boundaries document.
