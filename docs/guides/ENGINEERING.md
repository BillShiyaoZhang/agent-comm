# Agent Comm engineering reference

[中文产品介绍](../../README.md) · [English introduction](../../README_EN.md)

This reference covers the current helper and collaboration integrations. Start with the README for the user journey; use this document when building, maintaining or extending an installation.

`agent.InitIdentity` keeps experimental Kademlia DHT discovery disabled by default. The helper and normal messaging routes use authenticated Registry/MQ discovery, so they do not need this network. SDK users that explicitly require it can set `agent.Config.EnableDHT: true` or use the existing `dht` package APIs; the standalone bootstrap example also remains an explicit DHT server. The upstream dependency still has [GO-2024-3218](https://pkg.go.dev/vuln/GO-2024-3218), with no known fixed version. Default-off is a runtime mitigation, not an upstream fix; whole-SDK vulnerability scans still report the optional code.

## Components

| Component | Location | Responsibility |
| --- | --- | --- |
| Go SDK and helper | `agent/`, `cmd/helper/` and supporting packages | Identity, signing and encryption, Registry/MQ access, durable inbox/outbox |
| Shared Python runtime | [python/](../../python/README.md) | Contacts, grants, tasks, controlled actions, four adapter ports and remote pairing/RPC |
| Hermes plugin | [connectors/hermes-platform/](../../connectors/hermes-platform/README.md) | Native host and confirmation adapters, Gateway sessions, inbox lifecycle |
| OpenClaw bridge | [connectors/openclaw-channel/](../../connectors/openclaw-channel/README.md) | Basic send/receive integration; does not supply the new collaboration runtime by itself |

## Current helper message path

```text
Hermes / another supported local client
  ↕ local HTTP, SSE, consumer ACK
helper: local keys, signing/encryption, SQLite inbox/outbox
  ↕ HTTPS Registry / MQ
agent-comm-platform: identity directory and durable encrypted mailbox
```

- `POST /api/v1/mq/store` returns HTTP 202, `success:true` and a stable `message_id` after local durable acceptance. The worker retries the same signed ciphertext. `platform_queued` means the platform accepted it; neither state proves recipient execution or task completion.
- Incoming envelopes are authenticated and persisted in the helper inbox before platform ACK. SSE reconnects and retrieve replay unconsumed messages. The consumer ACKs local consumption after successful processing or durable acceptance, according to its integration contract.
- Durable helper sends use MQ. The traditional Go SDK `SendMessage` has a separate P2P/Double Ratchet route; do not describe it as the helper's default first attempt. Signed direct envelopes also use the durable receive callback. HTTPS MQ uses static X25519, AES-GCM and Ed25519 signatures, and does not provide Double Ratchet forward secrecy.
- The local API handles plaintext and has identity and consumption privileges. It binds only to loopback and rejects cross-origin browser requests. The Web workspace uses explicitly paired agent-side RPC; it cannot obtain local trust merely by adding an agent record.

The complete API, retries, expiry, message fields and state semantics are in [HERMES_INTEGRATION.md](HERMES_INTEGRATION.md). Platform storage is temporary, and delivery receipts are not business completion receipts. Consumers still need durable operation records and idempotent side effects.

## Registry and envelope authentication

Registry registrations require an Ed25519 signature from the URN owner. PeerID must derive from the same public key, and the X25519 public key must be 32 bytes. Signatures cover `registry.BuildSignedMsg`; both first registrations and updates are checked. The unsigned legacy entry point is unavailable. The HTTP client generates the signature automatically. Write timestamps accept the past five minutes and up to one minute of future clock skew.

Envelope signatures bind the sender, recipient, stable message ID and all encryption fields. Cloud MQ retrieve/ACK operations authenticate the recipient identity. Reusing a message ID with different content is rejected. Cryptographic identity validation does not grant permission to execute a task, access tools or approve an action.

## Installation, profiles and upgrades

Use the current matching helper, Python runtime and connector. Install the Python packages into the environment running Hermes Gateway and its Desktop/Web backend, and resolve the actual profile with `hermes_constants.get_hermes_home()` rather than assuming `~/.hermes`. The Hermes plugin documents its required native hooks and tested host revision; revalidate when upgrading the host.

Preserve identity keys, helper `mailbox.db`, plugin receipt databases and their associated WAL files, and collaboration/remote state. Give each identity a separate directory and loopback port. Run only one active consumer per helper inbox; do not run a standalone remote consumer alongside the Hermes consumer for the same helper.

The cloud HTTPS address is the helper daemon's platform argument. The Hermes plugin's `platform_url` is instead the local `http://127.0.0.1:45042`. Verify `/info`, an established SSE connection and a real two-identity round trip. Starting the helper or accepting a local send alone is insufficient.

For platform operators, signed envelopes and authenticated ACK require a coordinated server/client upgrade. See the [platform upgrade instructions](https://github.com/BillShiyaoZhang/agent-comm-platform/blob/main/docs/guides/MESSAGE_UPGRADE.md) and the [integration contract](HERMES_INTEGRATION.md). To keep the helper running, see [persistent services](HELPER_SERVICE.md).

## Collaboration and remote access

The shared runtime defines `HostPort`, `MemoryPort`, `InteractionPort` and `TransportPort`, validates versioned capabilities and calls real adapters through its dispatcher. Reference adapters are runnable. It does not load private memory by default: explicitly choose finite snapshots, and separately authorize disclosure. Registering material is not permission to send it.

Hermes personal collaboration and remote workspace access are separately enabled through `collaboration_enabled` and `remote_enabled`. Pairings constrain the console URN, allowed methods and expiry. Explicitly paired `contacts.add` and `approval.respond` let the user add contacts and answer specific pending approvals through the Web workspace; the runtime derives the owner from the local pairing, validates current state and saves the result in the agent store. Approval responses update authorization state without directly sending business messages. Native confirmation still checks the actual owner conversation, current turn and the answer to the specific question. A contact entry, conversation permission or an `allow_from` value alone does not confer approval authority. Existing pairings retain their method scope until explicitly replaced locally; deployment requires matching runtime and Web versions.

See the [runtime reference](../../python/README.md), [Hermes plugin](../../connectors/hermes-platform/README.md) and [Web setup](https://github.com/BillShiyaoZhang/agent-collaboration-web#readme) for extension, configuration and remote pairing. The component constrains its own paths; the host remains responsible for broader shell, file and network tool permissions.

## Validation

From this repository, with the local runtime installed:

```sh
go test ./mq ./crypto ./session
python -m unittest discover -s python/tests -q
python -m unittest discover -s connectors/hermes-platform/tests -q
```

Alternatively add this repository's `python/` directory to `PYTHONPATH`. Hermes native integration tests also need the host's actual dependencies and the platform-specific `PYTHONPATH` setup documented in the plugin README. These tests do not send a message to a real person or prove a real model task completed.

For full Go validation and local helper/platform process checks:

```sh
go test ./...
go build -o build/agent-comm-helper ./cmd/helper
python tools/test_helper_platform.py --helper build/agent-comm-helper --platform /absolute/path/to/platform
```

The process test uses temporary identities and a local platform. It verifies actual helper/platform messaging and recovery without contacting production or invoking a model. A live installation still needs its own explicit two-agent round trip and application-level outcome check.

## Go SDK source and design background

- [Identity initialization, SendMessage and OnMessage](../../agent/agent.go)
- [GenerateContactCard and ImportContactCard](../../agent/contact_card.go)
- [Architecture background in Chinese](../architecture/OVERVIEW.md) and [English](../architecture/OVERVIEW_EN.md)

The architecture background also describes earlier P2P design goals. For the deployed helper route, current installation and supported integration behavior, follow this document and the linked helper/plugin contracts.
