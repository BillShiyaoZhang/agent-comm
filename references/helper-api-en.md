# Helper API and diagnostics

Read for identity setup, host integration, or delivery debugging. Ordinary Hermes personal collaboration uses the plugin tool. Replace `<...>` arguments with actual configuration. The daemon's platform argument is the cloud URL; the host's helper URL is local.

## Identity and startup

```text
agent-comm-helper init <absolute_keys_dir>
agent-comm-helper daemon <absolute_keys_dir> <platform_url> [local_port]
```

`init` loads an existing identity or creates one if absent, returning `urn`, `peer_id`, `ed25519_pubkey`, and `x25519_pubkey`. Inspect existing configuration and expand `~`; do not change directories and create a new identity just to inspect it. `GET /info` returns `urn`, `peer_id`, `addrs`, and `status`, without the public platform URL. The default listener is `127.0.0.1:45042`. The helper polls at startup and every 5 seconds; it can start and queue retries while the cloud is unavailable.

## Communication contacts

`POST /api/v1/contacts` to the local helper accepts:

| Field | Meaning |
| --- | --- |
| `urn` | Peer URN; compatibility alias `contact_urn` |
| `x25519_pk` | Hex of the 32-byte public key; alias `x25519_public_key` |
| `ed25519_pk` | Ed25519 public key hex; alias `ed25519_public_key` |
| `peer_id` | libp2p peer ID; if omitted, an Ed25519 key must allow derivation |
| `display_name` | Display name; alias `alias` |
| `trusted`, `trust_tier` | Local communication trust; tiers `self`/`family`/`friend` can map to trusted |
| `addrs` | Optional multiaddr strings added to peerstore |

Success returns `{"success":true}`. Obtain consistent URN, keys, and peer ID from a verified source; a successful write does not authenticate the relationship between a person and a network identity. It creates no runtime owner-confirmation record and does not import a full card text. There are no HTTP list/delete/standalone trust-edit endpoints; those operations belong to [Go contacts.Store](../contacts/contact.go).

## Reliable HTTP messages

Paths below are relative to the **local helper URL**. Use `Content-Type: application/json` for JSON requests.

| Method/path | Request or result |
| --- | --- |
| `POST /api/v1/mq/store` | Message JSON below; HTTP 202 with `success`, `message_id`, `status` |
| `GET /api/v1/mq/status?message_id=...` | `message_id`, `status`, `attempts`, `last_error`; unknown ID returns 404 |
| `GET /api/v1/mq/retrieve` | `{"messages":[...]}` for all locally unacknowledged inbound messages |
| `GET /api/v1/mq/subscribe` | SSE `id: <message_id>`, `data: <inboundJSON>`; repeats pending messages on connection and every 5 seconds |
| `POST /api/v1/mq/ack` | `{"message_ids":["request-001"]}`; returns `success` and newly `acked` count; repeated ACKs count as 0 |

```json
{
  "recipient_urn": "urn:agent-comm:agent:PEER_ID",
  "message_id": "request-001",
  "text": "Please check the task input",
  "conversation_id": "conversation-001",
  "task_id": "task-001",
  "kind": "task",
  "in_reply_to": "previous-message-id",
  "deadline": "2027-01-01T00:00:00Z",
  "hop_limit": 8
}
```

Use the actual task deadline; omit optional correlation fields when unnecessary. `text` allows 262144 UTF-8 bytes; conversation/task/kind/in_reply_to each allow 256 bytes. A local message ID is 1–128 ASCII letters/digits or `._:-`; `hop_limit` is 0–64, default 8; `kind` defaults to `message`; deadlines are RFC3339. The same ID and request return the original status; changed content under the same ID returns 409. Omitting message_id generates one, but cross-request retries need a retained stable ID.

Inbound `sender_urn` is transport-verified, with correlation fields preserved. Deduplicate and process or durably enqueue before acknowledging the local helper. The helper ACKs the platform after validation, decryption, and inbox persistence. Receiving SSE, beginning work, `Last-Event-ID`, or a model call returning does not justify an early ACK. See the [communication contract](../docs/HERMES_INTEGRATION.md) for delivery and crash recovery semantics.

## Raw cryptography CLI

Custom clients or diagnostics may need these commands; the daemon and Hermes plugin already handle them for normal messaging.

```text
agent-comm-helper sign-retrieve <keys_dir> <recipient_urn> <unix_timestamp_seconds>
agent-comm-helper sign-store <keys_dir> <raw_body_hex>
agent-comm-helper encrypt-envelope <keys_dir> <recipient_urn> <recipient_x25519_pubkey_hex> <plaintext_hex> <message_id>
agent-comm-helper decrypt-envelope <keys_dir> <envelope_proto_hex>
```

Signing returns `signature` and `pubkey`. Encryption returns a signed full envelope binding sender/recipient URNs and a stable ID, including `envelope_proto_hex`. Decryption takes full protobuf hex, verifies it, and returns `plaintext`, `sender_urn`, `recipient_urn`, and `message_id`. The old recipient-key-only encryption arguments and separate ciphertext/nonce/tag decryption arguments are rejected. Do not construct unsigned envelopes or omit identity binding. See the [communication contract](../docs/HERMES_INTEGRATION.md) and [main.go](../cmd/helper/main.go) for cloud HTTP integration.

## One-line export without a host process

With runtime installed, the reference CLI prints one sentence to stdout. Omit the ID after `--export-contact` to select self:

```text
python -m agent_comm_runtime.reference --state <collaboration.sqlite3> --agent-urn <own_urn> --platform-url <actual_platform_url> --export-contact [contact_id]
```

A friend must be a confirmed contact of that reference host; do not impersonate another host's owner identity. Hermes uses native `export_contact` and `extra.public_platform_url` for its own public address; a friend's actual platform URL must still be supplied explicitly. Self-hosted HTTP(S) addresses are supported; loopback/unspecified addresses, credentials, query/fragment, and control characters are rejected. Missing/invalid values produce no misleading sentence. See [Add-contact text](../SKILL_EN.md#export-contact) for the output and boundary.
