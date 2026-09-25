# Helper API and diagnostics

Read for identity setup, host integration, or delivery debugging. Ordinary Hermes personal collaboration uses the plugin tool. Replace `<...>` arguments with actual configuration. The daemon's platform argument is the cloud URL; the host's helper URL is local.

## Identity and startup

```text
agent-comm-helper init <absolute_keys_dir>
agent-comm-helper daemon <absolute_keys_dir> <platform_url> [local_port]
```

`init` loads an existing identity or creates one if absent, returning `urn`, `peer_id`, `ed25519_pubkey`, and `x25519_pubkey`. Inspect existing configuration and expand `~`; do not change directories and create a new identity just to inspect it. `GET /info` returns `urn`, `peer_id`, `addrs`, and `status`, without the public platform URL. The default listener is `127.0.0.1:45042`. The helper polls at startup and every 5 seconds; it can start and queue retries while the cloud is unavailable.

For Agent-to-Agent v2, independently verify the policy signing root and platform libp2p PeerID, then run `v2-pin-policy-root <keys_dir> <root_public_key_hex> <expected_platform_peer_id> <independent_verification_note>` on each side. The updated single-platform helper can resolve an exact peer URN through Registry, verify its key binding and signed record, and cache the public key. `v2-pin-peer <keys_dir> <peer_urn> <ed25519_public_key_hex> <independent_verification_note>` remains available for existing manual pins or extra out-of-band verification; automatic discovery cannot replace a conflicting pin. The v0.9.0 bundle supports this discovery; the older v0.8.0 bundle still requires both sides to verify and pin full peer keys manually. Restart the daemon, then inspect local `GET /api/v2/disclosure`. V2 defaults to private only. After the owner reviews the exact signed policy and gateway key, authorize it with `v2-allow-compliance <keys_dir> <policy_hash> <explicit_authorization_note>` on each side. Every new policy hash needs fresh authorization. `v2-disallow-compliance <keys_dir> <explicit_revocation_note>` stops subsequent compliance work; it cannot retract already disclosed plaintext. Key verification proves control of the URN, not a real-world person's identity. A legacy `trusted` contact does not grant business authority. See the [v2 protocol reference](../docs/architecture/PROTOCOL_V2.md).

A v2 complete installation bundle with `policy-trust.json` can run `v2-ensure-policy-root <keys_dir> <root_public_key_hex> <expected_platform_peer_id> <trusted_release_note>` against the original identity directory. Repeating the exact pin is idempotent; different values fail closed. Older bundles and manual installations still use `v2-pin-policy-root` explicitly. Local `GET /api/v2/disclosure` reports the root loaded by the running daemon as `policy_root_public_key` (`null` when absent); `policy_verified=true` and the expected `platform_id` are additionally needed to establish current signed-policy readiness. A new helper without the root pin rejects ordinary sends with HTTP 428. Do not treat the platform's own bootstrap response as independent verification. Provision a signed `private` policy with `allow_v1=true`, distribute the root and PeerID through a trusted channel, then upgrade the helper while preserving its identity and mailbox. Web pairing alone enables neither Agent-to-Agent v2 nor compliance disclosure.

`POST /api/v2/mq/store` does not consult Python Runtime contacts or whether the peer accepted a friend request. It checks identity, Registry and policy, but a successful low-level send says nothing about the peer's willingness to communicate. Python Runtime, Hermes collaboration tools and paired Web enforce the `connected` outbound gate. If the recipient uses Runtime, it quarantines business messages from unconnected senders rather than presenting them as ordinary inbox messages.

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
| `POST /api/v1/mq/store` | On the new helper, ordinary legacy sends fail with 428 `policy_root_required`, 403 `consent_required`, or 409 `upgrade_required` in JSON |
| `GET /api/v1/mq/status?message_id=...` | `message_id`, `status`, `attempts`, `last_error`; unknown ID returns 404 |
| `GET /api/v1/mq/retrieve` | `{"messages":[...]}` for all locally unacknowledged inbound messages |
| `GET /api/v1/mq/subscribe` | SSE `id: <message_id>`, `data: <inboundJSON>`; repeats pending messages on connection and every 5 seconds |
| `POST /api/v1/mq/ack` | `{"message_ids":["request-001"]}`; returns `success` and newly `acked` count; repeated ACKs count as 0 |
| `POST /api/v2/mq/store` | Agent-to-Agent v2; same message JSON. Updated source can verify a peer key from its exact URN; it still requires the pinned policy root, a verified policy and any applicable local compliance authorization. The older v0.8.0 bundle still needs manual peer key pins; v0.9.0 supports URN-only first contact; HTTP 202 |
| `GET /api/v2/mq/status?message_id=...` | V2 outbound status, policy hash, and verified receipt flag |
| `GET /api/v2/disclosure` | Signed policy facts, platform decryptability, local consent, `legacy_send_code`, and quarantine counts; unknown facts are `null` |
| `POST /api/v1/managed/mq/store` | Explicit paired Web `control.response` route, correlated with an existing v1 inbox request; platform separately verifies the active managed certificate |

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

Inbound `sender_urn` is transport-verified, with correlation fields preserved. Deduplicate and process or durably enqueue before acknowledging the local helper. The helper ACKs the platform after validation, decryption, and inbox persistence. Receiving SSE, beginning work, `Last-Event-ID`, or a model call returning does not justify an early ACK. See the [communication contract](../docs/guides/HERMES_INTEGRATION.md) for delivery and crash recovery semantics.

Verified v2 inbound messages use the same local retrieve/SSE inbox and add per-message `mode`, `policy_epoch`, `gateway_key_id`, and `envelope_hash`. A compliance message without a valid receipt is neither persisted nor ACKed. Old v1 messages are never labeled verified v2. Pre-cutover immutable ciphertext is marked `quarantined` instead of being re-encrypted under its original message ID. The new helper does not silently convert `/api/v1/mq/store` to v2. Older binaries can use a signed private policy's temporary v1 compatibility window; provision the signed policy and trusted root distribution before rolling out the new helper. Preserve the identity directory and `mailbox.db`.

## Raw cryptography CLI

Custom clients or diagnostics may need these commands; the daemon and Hermes plugin already handle them for normal messaging.

```text
agent-comm-helper sign-retrieve <keys_dir> <recipient_urn> <unix_timestamp_seconds>
agent-comm-helper sign-store <keys_dir> <raw_body_hex>
agent-comm-helper encrypt-envelope <keys_dir> <recipient_urn> <recipient_x25519_pubkey_hex> <plaintext_hex> <message_id>
agent-comm-helper decrypt-envelope <keys_dir> <envelope_proto_hex>
```

Signing returns `signature` and `pubkey`. Encryption returns a signed full envelope binding sender/recipient URNs and a stable ID, including `envelope_proto_hex`. Decryption takes full protobuf hex, verifies it, and returns `plaintext`, `sender_urn`, `recipient_urn`, and `message_id`. The old recipient-key-only encryption arguments and separate ciphertext/nonce/tag decryption arguments are rejected. Do not construct unsigned envelopes or omit identity binding. See the [communication contract](../docs/guides/HERMES_INTEGRATION.md) and [main.go](../cmd/helper/main.go) for cloud HTTP integration.

## One-line export without a host process

With runtime installed, the reference CLI prints one sentence to stdout. Omit the ID after `--export-contact` to select self:

```text
python -m agent_comm_runtime.reference --state <collaboration.sqlite3> --agent-urn <own_urn> --platform-url <actual_platform_url> --export-contact [contact_id]
```

A friend must be a confirmed contact of that reference host; do not impersonate another host's owner identity. Hermes uses native `export_contact` and `extra.public_platform_url` for its own public address; a friend's actual platform URL must still be supplied explicitly. Self-hosted HTTP(S) addresses are supported; loopback/unspecified addresses, credentials, query/fragment, and control characters are rejected. Missing/invalid values produce no misleading sentence. See [Add-contact text](../SKILL_EN.md#export-contact) for the output and boundary.
