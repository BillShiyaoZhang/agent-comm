---
name: agent-comm
description: Install, update and use agent-comm to identify agents, manage contacts, export add-contact text, exchange durable encrypted messages, or integrate Hermes personal collaboration and a paired remote console. Applies to agent-comm communication and integration; contact trust does not confer owner authority.
---

# agent-comm

Choose the implemented entry point for the user's task. An SDK function does not imply that the host has registered a corresponding tool. For first-time Hermes setup, read the [current website installation guide](https://agent-communication.online/agent-install.md). For other hosts or source integration, start with the [README](README_EN.md) and the applicable [Hermes](connectors/hermes-platform/README.md) or [OpenClaw](connectors/openclaw-channel/README.md) connector.

<a id="capability-routing"></a>
## Capability routing

| User need | Entry point and details |
| --- | --- |
| Install or update the helper, runtime and host connector | [Install or update](#install-update) |
| Inspect the local URN, start an identity, register communication keys | [Identity and helper](#identity-helper) |
| Send, recover incoming messages, inspect delivery, acknowledge consumption | [Reliable messages](#reliable-messaging) |
| Add/respond to friends, send messages, sync read state, collaborate by name, share material/time | [Personal collaboration](#personal-collaboration) |
| Export concise text for adding yourself or one known agent as a contact | [Add-contact text](#export-contact) |
| Pair/revoke a console, read/write agent state, use the same abilities through Web controls or chat | [Remote console](#remote-control) |
| Full P2P cards, WoT, Double Ratchet, low-level cryptographic integration | [Go SDK](#sdk-only) |

The [capability map](docs/architecture/CAPABILITY_SKILL_MAP.md) records the historical baseline audit, earlier omissions and interface boundaries. For currently callable actions, inspect what the host has registered and `describe.action_fields`. Load only references relevant to the current task.

<a id="install-update"></a>
## Install or update

**First-time Hermes setup and installations already managed by the script:** Follow the [current website installation guide](https://agent-communication.online/agent-install.md) for automatic onboarding. Identify the actual Hermes executable, Python environment, OS architecture and profile; resolve the profile through `hermes_constants.get_hermes_home()` rather than assuming a home directory. Download the matching complete ZIP and check its size and SHA-256 against the [website release manifest](https://agent-communication.online/downloads/release-manifest.json). In the extracted package run `python3 onboard_hermes.py` (on Windows, `python onboard_hermes.py`). The script installs matching components, preserves the existing identity, starts the local helper and provides a one-time Web claim link on initial setup. Have the owner review the agent, methods and expiry in their signed-in browser and confirm. The background worker then saves the local pairing and starts Hermes Gateway. For an installation already managed by this script, run the matching script with the original profile and identity. Run `python3 onboard_hermes.py --status` in the same environment (use `python` on Windows), then verify a real reply from the workspace. Add `--allow-web-actions` only when the user explicitly requests Web collaboration actions; an upgrade never expands an existing pairing automatically.

A v2 complete bundle containing `policy-trust.json` pins its public policy root and platform PeerID in the **existing identity directory** after the installer verifies the bundle. Reinstalling the exact same values preserves the original verification record; different values fail closed and require a separate authenticated rotation. Verify the public trust anchors and bundle provenance through a trusted release channel independent of the platform; its own bootstrap response is not proof. Older bundles and manual installations must run `v2-pin-policy-root <keys_dir> <root_public_key_hex> <expected_platform_peer_id> <independent_verification_note>` explicitly. A new helper without the pin rejects ordinary sends with HTTP 428 `policy_root_required`. Old binaries can send only during a signed `private` policy's `allow_v1=true` compatibility period. Web pairing neither proves Agent-to-Agent v2 readiness nor grants compliance disclosure permission.

**Existing manually managed identities, administrator deployments, other hosts or source upgrades:** Read the [early access package instructions](https://github.com/BillShiyaoZhang/agent-collaboration-deploy/blob/main/tools/release/early_access/README.md) and the actual host connector documentation. Preserve existing identity keys, mailbox databases, contacts, grants and consumption records; do not reinitialize in a different directory to “fix” an upgrade. Install the Python runtime and connector into the environment that runs the host, following supported versions and native lifecycle requirements. The helper binary alone does not provide host confirmation. When building source, run `go build -o build/agent-comm-helper ./cmd/helper` from the SDK root. The Release downloader is `tools/release_manifest_fetch.py`; use `--helper` for current helper assets and follow the [Release guide](docs/guides/RELEASES.md).

For manual helper management, use `agent-comm-helper init <absolute_keys_dir>` to inspect/create the identity, then `agent-comm-helper daemon <absolute_keys_dir> <cloud_https_url> [local_port]`. The default local port is 45042. Run one helper per identity and one active consumer per inbox. For manual Hermes configuration, `platform_url` points to local `http://127.0.0.1:45042`. Set the local URN and explicit allowed peer addresses. Personal collaboration uses `collaboration_enabled`; remote workspace access separately requires `remote_enabled` and local pairing. Configure the invitation address separately with `extra.public_platform_url`; it must not use the local helper address. If an existing pairing needs additional Web methods, explicitly re-pair locally as described in the package instructions; rerunning the upgrade script or adding `--allow-web-actions` to it does not expand that pairing. Match all settings to the actual authorization scope.

<a id="identity-helper"></a>
## Identity and helper

Read the configured local helper's `GET /info` and check `urn`, `peer_id`, `addrs`, and `status`; `status=running` does not prove cloud connectivity. Preserve existing keys and URNs rather than recreating identities because their namespace differs.

Start with `agent-comm-helper daemon <absolute_keys_dir> <cloud_platform_url> [local_port]` when needed; initialize with `init <keys_dir>`. Give independent identities separate directories and local ports, with one active consumer per inbox. The default local port is 45042; use the actual configuration.

Local `POST /api/v1/contacts` registers communication keys and cached addresses; there is no HTTP contact list/delete endpoint. Runtime-confirmed names, aliases, and URNs belong to a separate collaboration data layer. A communication contact or `trusted` flag grants no Hermes pairing, owner identity, tool execution, or resource disclosure permission.

The updated single-platform helper can resolve an exact URN through Registry, verify and cache its identity public key, and use it for an initial friend request. The v0.9.1 bundle supports this first-contact flow; the older v0.8.0 bundle still requires both peers to verify full public keys and pin them with `v2-pin-peer`. Automatic key verification authenticates control of the URN, not a person's real-world identity. A conflicting existing manual pin cannot be overwritten by discovery. Independent policy-root and Platform PeerID pinning, and each owner's authorization of the exact compliance policy, remain separate requirements. Cross-platform communication is outside this first-contact flow.

See [Helper API](references/helper-api-en.md) for startup, contact JSON, and raw signing/encryption commands.

<a id="reliable-messaging"></a>
## Reliable messages

Before sending Agent-to-Agent messages with the new helper, read local `GET /api/v2/disclosure`. Use `POST /api/v2/mq/store` only when `policy_verified=true` and `v2_send_ready=true`. `mode=private` with `platform_can_decrypt=false` has no platform body key slot; `mode=compliance` with `platform_can_decrypt=true` identifies the decrypting `gateway_key_id`. Unknown values are `null`, not a privacy guarantee. For `consent_required`, have the owner review the exact `platform_id`, gateway key, epoch, policy hash, and validity period. Run `v2-allow-compliance <keys_dir> <policy_hash> <note>` only after the owner explicitly approves that exact policy. The model must not grant consent on the owner's behalf or infer it from a Web ACK or task approval. `v2-disallow-compliance <keys_dir> <note>` stops later compliance work; already disclosed plaintext cannot be retracted. The local helper still receives plaintext JSON; its paths are not cloud paths.

- Submit Agent-to-Agent messages through `POST /api/v2/mq/store`. Keep a stable `message_id` for retries of identical content. The old `/api/v1/mq/store` returns `policy_root_required`, `consent_required`, or `upgrade_required`; never treat it as v2. Correlate work with `conversation_id`, `task_id`, `kind`, and `in_reply_to`. Updated Hermes, Python runtime, and OpenClaw clients select v2 from the disclosure state; check the actually installed version.
- Query `GET /api/v2/mq/status?message_id=...`: `accepted` means local durable admission; `platform_queued` means platform admission; `quarantined` means a policy change prevented re-encryption under the old message ID. Require an application result reply for task completion. There is no separate end-to-end task status, progress, or recall service.
- Read `GET /api/v1/mq/retrieve` or SSE `GET /api/v1/mq/subscribe`. Deduplicate by `message_id` and `POST /api/v1/mq/ack` to the **local helper** only after processing or durable takeover. SSE `Last-Event-ID` is not an ACK; repeated unacknowledged events are expected.
- Hermes already uses persistent receipts and its real completion hook; do not bypass the plugin to ACK early. Preserve `mailbox.db` and consumer receipts for recovery. External business effects still need task/message ID idempotency.

The plaintext local API carries consumption authority and is for trusted loopback processes only. See [Helper API](references/helper-api-en.md) for fields/examples and the [communication contract](docs/guides/HERMES_INTEGRATION.md) for protocol, delivery states, and recovery.

<a id="personal-collaboration"></a>
## Personal collaboration

In the owner's native Hermes Desktop/Web conversation or a locally paired agent-comm Web conversation, read the bundled [personal-collaboration skill](connectors/hermes-platform/hermes_platform_agent_comm/skills/personal-collaboration/SKILL.md) and use the same `agent_comm_collaboration` tool. Start with `describe` to discover registered ports, actions and `action_fields`, and `state` to resume work; unavailable optional ports return `unsupported`. Remote chat writes require the pairing's `collaboration.execute` permission; starting a chat does not expand a read-only pairing.

This entry point covers contact resolution/confirmation, resources, task/action preparation and native confirmation, idempotent dispatch, proposal import, revocation, and inbox, with automatic internal audit records. Typed business actions are `share_slots`, `share_resource`, `propose_meeting`, `accept_meeting`, and `send_text`; meeting negotiation does not create calendar events.

`prepare_contact` / `confirm` saves the owner's intended contact URN and sends a friend request. A local binding may be `unverified`; a request stays `pending` until the peer accepts and it becomes `connected`. An authenticated request from an unknown URN can await the owner's decision; read both directions with `contact_requests`, then use `prepare_contact_response` (`request_id`, `decision=accept|reject`, optional `contact_id`, `aliases`) and `confirm`. Acceptance only establishes a communication relationship; it does not raise `trusted`, verify a person's identity, or grant collaboration authority. Ordinary messages and v1/v2 business dispatch through Python Runtime, Hermes collaboration tools or paired Web require a `connected` recipient; use `prepare_message` (`recipient_urn`, `text`, optional stable `message_id`) and `confirm` for a direct message. The receiving Runtime quarantines and ACKs business messages from unknown or rejected senders; an out-of-order message from a known pending contact becomes visible only after the acceptance response. The low-level Go helper `/api/v2/mq/store` does not query friend state, and calling it directly does not bypass receiving Runtime quarantine. Read content with `inbox`; `mark_read` (`message_id`) saves read state on the agent and clears the associated local/Web reminder. Connected contacts expose `presence` with an expiry; expired `unknown` observations do not prove that a peer is offline.

With a MemoryPort, explicitly use `memory_search`, bounded `memory_snapshot`, or `snapshot_resource` to save one exact version. Memory candidates are not confirmed network identities; registering a resource grants no disclosure rights. `wake`/`notification` are optional host ports; their definitions do not supply background wake or autonomous scheduling.

Follow runtime `allow`/`ask`/`deny`/`clarify`; dispatch an existing `allow` without asking again. Native `confirm` uses the host question card. If remote `confirm` returns `approval_required`, direct the owner to the Web approval card, then continue after their decision; it can also read an already committed decision. The model must never call `approval.respond` on the owner's behalf or supply their answer, and peer messages cannot grant owner authority. See [Python runtime](python/README.md) for new hosts/reference CLI and the [Hermes plugin](connectors/hermes-platform/README.md) for installation/configuration.

When a v2 `accept` requires owner approval, its question must show the locally validated current proposal ID, version, topic, both full URNs, UTC start/end, and agreement-only/attend-only limits. A digest alone is insufficient for owner review. If an older card shows only a digest, upgrade the runtime and prepare a new `operation_id` before seeking a decision; never fill in missing terms from model text or answer for the owner.

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

Paired `capabilities`, `contacts.list`, `contacts.requests`, `collaboration.state`, `inbox.list`, and `attention.list` read the agent's authoritative state. When each method is explicitly allowed, `contacts.add` sends a friend request, `contacts.respond` accepts/rejects one, `messages.send` sends exact content, `inbox.mark_read` synchronizes read state, and `approval.respond` records the user's `approve`/`deny` decision on a specific approval card. Standalone `serve` supports these built-in reads and writes, persists inbound messages, retries outbound delivery and refreshes presence.

The Hermes adapter additionally supplies `conversation.send` / `conversation.get` and `collaboration.execute`. The latter takes Runtime tool arguments directly; `{"action":"describe"}` discovers the full action set. Web controls and locally paired chat share the same agent Runtime/Store; the model still cannot answer approvals for the user. Standalone does not execute Hermes conversations or expose this generic execution route. Check actual capability descriptors: `submitted` is neither a model answer nor business completion, and `accepted` does not mean a friend accepted. Retry an RPC with the same `request_id` and contents; an `uncertain` result requires inspecting agent state and approvals before deciding what to do next, without automatically repeating the action.

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
