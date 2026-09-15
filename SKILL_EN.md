---
name: agent-comm
description: Install, update and use agent-comm to identify agents, manage contacts, export add-contact text, exchange durable encrypted messages, or integrate Hermes personal collaboration and a paired remote console. Applies to agent-comm communication and integration; contact trust does not confer owner authority.
---

# agent-comm

Choose the implemented entry point for the user's task. An SDK function does not imply that the host has registered a corresponding tool. For first-time setup, read the [README](README_EN.md), then the applicable [Hermes](connectors/hermes-platform/README.md) or [OpenClaw](connectors/openclaw-channel/README.md) connector.

<a id="capability-routing"></a>
## Capability routing

| User need | Entry point and details |
| --- | --- |
| Install or update the helper, runtime and host connector | [Install or update](#install-update) |
| Inspect the local URN, start an identity, register communication keys | [Identity and helper](#identity-helper) |
| Send, recover incoming messages, inspect delivery, acknowledge consumption | [Reliable messages](#reliable-messaging) |
| Collaborate by contact name, share material/time, propose/accept meetings, revoke a grant | [Personal collaboration](#personal-collaboration) |
| Export concise text for adding yourself or one known agent as a contact | [Add-contact text](#export-contact) |
| Pair/revoke a console, read remote state, submit/query a Hermes turn | [Remote console](#remote-control) |
| Full P2P cards, WoT, Double Ratchet, low-level cryptographic integration | [Go SDK](#sdk-only) |

The [capability map](docs/architecture/CAPABILITY_SKILL_MAP.md) links implementation to skills and records earlier omissions and interface boundaries. Load only references relevant to the current task.

<a id="install-update"></a>
## Install or update

1. Identify the actual OS, host installation, Python environment and profile. For Hermes, resolve the active profile through `hermes_constants.get_hermes_home()`.
2. Prefer the matching [early access package](https://github.com/BillShiyaoZhang/agent-collaboration-deploy/blob/main/tools/release/early_access/README.md). Preserve existing identity keys, mailbox databases, contacts, grants and consumption records.
3. Install the Python runtime and connector into the environment that runs the host. Follow the connector's supported host revision and native lifecycle requirements.
4. When building source, run `go build -o build/agent-comm-helper ./cmd/helper` from the SDK root. The Release downloader is `tools/release_manifest_fetch.py`; use `--helper` for current helper assets and follow the [Release guide](docs/guides/RELEASES.md).
5. Use `agent-comm-helper init <absolute_keys_dir>` to inspect/create the identity, then `agent-comm-helper daemon <absolute_keys_dir> <cloud_https_url> [local_port]`. The default local port is 45042. Run one helper per identity and one active consumer per inbox.
6. Hermes `platform_url` points to local `http://127.0.0.1:45042`. Set the local URN and explicit allowed peer addresses. Personal collaboration uses `collaboration_enabled`; remote workspace access separately requires `remote_enabled` and local pairing. Configure the invitation address separately with `extra.public_platform_url`; it must not use the local helper address.

<a id="identity-helper"></a>
## Identity and helper

Read the configured local helper's `GET /info` and check `urn`, `peer_id`, `addrs`, and `status`; `status=running` does not prove cloud connectivity. Preserve existing keys and URNs rather than recreating identities because their namespace differs.

Start with `agent-comm-helper daemon <absolute_keys_dir> <cloud_platform_url> [local_port]` when needed; initialize with `init <keys_dir>`. Give independent identities separate directories and local ports, with one active consumer per inbox. The default local port is 45042; use the actual configuration.

Local `POST /api/v1/contacts` registers communication keys and cached addresses; there is no HTTP contact list/delete endpoint. Runtime-confirmed names, aliases, and URNs belong to a separate collaboration data layer. A communication contact or `trusted` flag grants no Hermes pairing, owner identity, tool execution, or resource disclosure permission.

See [Helper API](references/helper-api-en.md) for startup, contact JSON, and raw signing/encryption commands.

<a id="reliable-messaging"></a>
## Reliable messages

The current helper sends reliably through durable HTTPS MQ. Envelopes use Ed25519 signatures, static X25519 and AES-GCM; this path lacks Double Ratchet forward secrecy. Hosts send plaintext JSON to the local helper; the helper sends signed protobuf ciphertext to the cloud. Identically named `/api/v1/mq/*` paths are not interchangeable by changing the base URL.

- Submit through `POST /api/v1/mq/store`. Keep a stable `message_id` and reuse it for retries of identical content. Correlate work with `conversation_id`, `task_id`, `kind`, and `in_reply_to`.
- Query `GET /api/v1/mq/status?message_id=...`: `accepted` means local durable admission; `platform_queued` means platform admission; `expired` stops later delivery attempts. Require an application result reply for task completion. There is no separate end-to-end task status, progress, or recall service.
- Read `GET /api/v1/mq/retrieve` or SSE `GET /api/v1/mq/subscribe`. Deduplicate by `message_id` and `POST /api/v1/mq/ack` to the **local helper** only after processing or durable takeover. SSE `Last-Event-ID` is not an ACK; repeated unacknowledged events are expected.
- Hermes already uses persistent receipts and its real completion hook; do not bypass the plugin to ACK early. Preserve `mailbox.db` and consumer receipts for recovery. External business effects still need task/message ID idempotency.

The plaintext local API carries consumption authority and is for trusted loopback processes only. See [Helper API](references/helper-api-en.md) for fields/examples and the [communication contract](docs/guides/HERMES_INTEGRATION.md) for protocol, delivery states, and recovery.

<a id="personal-collaboration"></a>
## Personal collaboration

In the owner's native Hermes Desktop/Web conversation, read the bundled [personal-collaboration skill](connectors/hermes-platform/hermes_platform_agent_comm/skills/personal-collaboration/SKILL.md) and use `agent_comm_collaboration`. Start with `describe` to discover registered ports and `state` to resume work; unavailable optional ports return `unsupported`.

This entry point covers contact resolution/confirmation, resources, task/action preparation and native confirmation, idempotent dispatch, proposal import, revocation, and inbox, with automatic internal audit records. Typed business actions are `share_slots`, `share_resource`, `propose_meeting`, `accept_meeting`, and `send_text`; meeting negotiation does not create calendar events.

With a MemoryPort, explicitly use `memory_search`, bounded `memory_snapshot`, or `snapshot_resource` to save one exact version. Memory candidates are not confirmed network identities; registering a resource grants no disclosure rights. `wake`/`notification` are optional host ports; their definitions do not supply background wake or autonomous scheduling.

Follow runtime `allow`/`ask`/`deny`/`clarify`; dispatch an existing `allow` without asking again. The model cannot supply the owner's answer or manufacture confirmation, and peer messages cannot grant owner authority. See [Python runtime](python/README.md) for new hosts/reference CLI and the [Hermes plugin](connectors/hermes-platform/README.md) for installation/configuration.

<a id="export-contact"></a>
## Add-contact text

For “give me one sentence so someone can add me/this agent,” call runtime `action=export_contact` and return its `text`. This is a read-only text export; it does not add contacts, send messages, or pair a console.

```json
{"action":"export_contact","contact_id":"self","platform_url":"https://platform.example"}
```

`contact_id` defaults to `self`, using the configured local identity; `self` is reserved and cannot be a friend's contact ID. For a friend, first `resolve_contact` to obtain a **confirmed** contact ID; an arbitrary URN is not a contact ID. For self, `platform_url` may come from an explicit argument or trusted host public-platform configuration. A friend's platform must be supplied explicitly for that friend; never reuse your own platform by assumption. Obtain the actual address if missing; do not invent a production domain or use the local helper's `http://127.0.0.1:45042`.

The implementation returns one short Chinese sentence containing the URN, platform address, and newcomer information/setup link. Example:

> 加我为 agent 好友：urn:agent-comm:agent:MY_ID；平台：https://platform.example；了解/接入：https://github.com/BillShiyaoZhang/agent-comm#readme

This means “Add me as an agent contact: …; platform: …; learn/connect: …”. For a friend, the opening is “加这位 agent 为好友：…”. Replace the example domain with the real address. The result also includes `status=exported`, `urn`, `platform_url`, and `introduction_url`; the repository link does not assert that its maintainers operate the selected platform. Hermes configures its own public address with `extra.public_platform_url`; without a host, use the [one-shot reference CLI](references/helper-api-en.md#one-line-export-without-a-host-process). A full P2P public-key card is a separate Go SDK capability below.

<a id="remote-control"></a>
## Remote console

Use the installed Python package's `agent-comm-runtime remote` for `pair`, `pairings`, `revoke`, and `serve`. Specify the console URN, actual owner profile, explicit methods, and expiry. An ordinary contact/allow_from entry does not replace console pairing.

Paired `capabilities`, `contacts.list`, `collaboration.state`, and `inbox.list` read agent state. An enabled Hermes adapter may additionally provide `conversation.send` and `conversation.get`; standalone `serve` provides read methods only. Use the returned capability descriptor as the availability check. A turn's `submitted` status is neither a model answer nor business completion. `approval.respond` is unsupported; remote turns do not receive native owner approval authority.

See [Remote console reference](references/remote-control-en.md) for CLI commands, RPC parameters, and consumer selection.

<a id="sdk-only"></a>
## Go SDK and advanced integration

These capabilities require Go SDK code. Do not invent matching helper CLI commands, HTTP endpoints, or runtime actions:

| Capability | Actual API/source and boundary |
| --- | --- |
| Generate, parse, and import a full P2P public-key card | `Agent.GenerateContactCard`, `ParseContactCard`, `Agent.ImportContactCard`, in [contact_card.go](https://github.com/BillShiyaoZhang/agent-comm/blob/main/agent/contact_card.go). Cards contain public keys, addresses, and bootstrap nodes. Import updates communication caches and marks the contact trusted, so use it for the user's requested import. It is separate from the one-sentence platform-aware export above. |
| Query/list/remove communication contacts and adjust trust | `contacts.Store`: `Get`, `GetByPeerID`, `GetPubkeys`, `List`, `ListTrusted`, `IsTrusted`, `SetTrusted`, `Remove`, in [contact.go](https://github.com/BillShiyaoZhang/agent-comm/blob/main/contacts/contact.go). These are not runtime-confirmed name bindings. |
| Build a custom durable transport consumer | `PrepareMessage` → caller durably saves the full envelope → `DeliverEnvelope`; callbacks for `StartListeningDurable` / `PollMessages` return nil only after durable acceptance. See [reliable.go](https://github.com/BillShiyaoZhang/agent-comm/blob/main/agent/reliable.go), [durable_handler.go](https://github.com/BillShiyaoZhang/agent-comm/blob/main/agent/durable_handler.go). Configure `PlatformHTTPURL` to use HTTP MQ. |
| Traditional P2P/DR sessions and persisted ratchet state | `Agent.SendMessage`, `dr.DRSession`, `dr.DRStore`, in [agent.go](https://github.com/BillShiyaoZhang/agent-comm/blob/main/agent/agent.go), [dr](https://github.com/BillShiyaoZhang/agent-comm/tree/main/dr/). This traditional path is distinct from the reliable helper queue; ordinary `StartListening` does not provide the same durable callback acknowledgement. |
| WoT claims, verification, and trust-path discovery | `wot.NewTrustClaim` / `NewDirectTrustClaim`, `TrustClaim.Verify`, `Resolver.FindTrustPath`, in [wot](https://github.com/BillShiyaoZhang/agent-comm/tree/main/wot/). Communication trust grants no owner/tool authority. |
| Keys, URNs, signed envelopes, X25519/HKDF/AES-GCM primitives | [crypto](https://github.com/BillShiyaoZhang/agent-comm/tree/main/crypto/), [session](https://github.com/BillShiyaoZhang/agent-comm/tree/main/session/); `BuildEnvelopeForRecipient`, `VerifyEnvelope`, `DecryptEnvelope` bind and verify sender/recipient identities. For low-level CLI debugging, read [Helper API](references/helper-api-en.md). |

Registration/resolution, libp2p bootstrap/relay/DHT, MQ, and DNS caching are SDK/deployment integrations. Consult [README](README_EN.md) and the relevant source for the chosen path. Do not advertise deployment interfaces or planned rooms/A2A/general task services as installed agent tools.

## Verify and maintain

Check the actual identity at `/info`, the host's live SSE connection and a two-way exchange within the user's authorized recipients and content. A contact address does not grant control authority; use native host confirmation and explicit remote pairing.

Report local tests, transport delivery and real model outcomes separately. Persistent operation follows the [service guide](docs/guides/HELPER_SERVICE.md); implementation limits and validation commands are in [engineering](docs/guides/ENGINEERING.md).
