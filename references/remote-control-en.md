# Remote console: pairing and methods

Read for local pairing administration or Web/host integration with the installed `agent-comm-runtime`. It exchanges `control.request` / `control.response` through the helper's encrypted mailbox, without exposing an additional local HTTP listener. Ordinary contacts do not receive console authority.

## Local CLI

Establish the actual profile, verified console URN, permitted methods, and expiry. Do not guess a real profile directory. Argument templates:

```text
agent-comm-runtime remote pair --hermes-profile <actual_profile> --console-urn <console_urn> --allow capabilities --allow contacts.list --allow collaboration.state --allow inbox.list --expires <RFC3339_expiry_with_timezone>
agent-comm-runtime remote pairings --hermes-profile <actual_profile>
agent-comm-runtime remote revoke --hermes-profile <actual_profile> --console-urn <console_urn>
```

`pair` writes a local allowlist and returns `paired_locally`, granting the specified console access to the owner's selected data/methods. The example grants reads only. Use the user's authorized console and scope; adding a communication contact does not implicitly authorize pairing. Add write permissions through explicit re-pairing; upgrades do not expand existing grants. `pairings` lists existing records; `revoke` revokes the console, without recalling disclosed data. Expiry requires seconds and a timezone; use the actual authorized lifetime.

All four subcommands accept `--state <remote.sqlite3>`, defaulting to `<profile>/agent-comm/remote.sqlite3`. For a non-Hermes host, supply `--state` and `--owner-principal` explicitly for pairing. The principal comes from the real host configuration; do not impersonate a different owner.

## Choose one consumer

- **Hermes Gateway**: enable `remote_enabled: true` in agent_comm `extra`, optionally setting `remote_state_path`. The plugin binds the profile's stable owner principal and executes real Hermes conversations. See the [plugin instructions](../connectors/hermes-platform/README.md).
- **Standalone**: when Hermes is not consuming that inbox, run the bridge. It supports paired built-in reads, friend requests, messages, read state and owner approval decisions:

```text
agent-comm-runtime remote serve --hermes-profile <actual_profile> --agent-urn <local_agent_urn> --helper-url http://127.0.0.1:45042 --once
```

`--once` processes one bounded batch and exits; omit it for continuous consumption. `--collaboration-state <collaboration.sqlite3>` overrides the default file under the profile's `agent-comm/` directory. Non-Hermes operation requires `--state` and `--collaboration-state`. Use the local URN returned by helper `/info` and the actual configured helper URL.

Do not run standalone and Hermes consumers for the same helper or add an ordinary collaboration consumer that competes to ACK `control.*`. Standalone persists ordinary inbound messages and the friend protocol, retries authorized outbound messages and refreshes presence. It does not start a model, wake the owner's native conversation, execute `conversation.send`, or register `collaboration.execute`. Built-in writes require their individual local pairing permissions.

## Implemented RPC methods

Availability depends on both the pairing allowlist and the host adapter. First call authorized `capabilities` and check each method's `available` value; names alone do not prove support.

| Method | params | Result/boundary |
| --- | --- | --- |
| `capabilities` | `{}` | Protocol, method availability/reasons, pairing expiry |
| `contacts.list` | `{}` | Runtime contacts, connection state and `presence` for the paired owner, separate from helper key caches |
| `contacts.requests` | `{}` | Incoming/outgoing friend requests with `pending`, `accepted` or `rejected` state |
| `contacts.add` | Required `contact_id`, `aliases` (string array), `urn` | Confirms the user's local contact entry and durably sends a friend request; connection requires peer acceptance |
| `contacts.respond` | Required `request_id`, `decision=accept\|reject`; optional `contact_id`, `aliases` | Handles an incoming request; acceptance saves a contact and establishes communication without granting trust or collaboration authority, then sends the response to synchronize both agents |
| `messages.send` | Required `recipient_urn`, `text`; optional stable `message_id` | Durably submits the user's exact content only to a `connected` contact, reusing the ID after failures; does not imply peer reading |
| `collaboration.state` | Optional `task_id` | Contacts, requests, resources, tasks, actions, pending/completed decisions, inbox, sent messages and proposals; reads confer no approval authority |
| `inbox.list` | Optional `task_id` | Previously persisted message content and `read` / `read_at`, without a fresh helper retrieval |
| `inbox.mark_read` | Required `message_id` | Stores read state on the agent; both clients clear the message reminder on their next sync. Does not accept friends or approve actions |
| `attention.list` | Optional `after`, `limit` (1–100) | Durable attention changes from the same agent; follow `cursor` / `has_more`. Handled items become resolved |
| `approval.respond` | Required `approval_id`, `decision=approve\|deny` | Records a user's explicit decision on a trusted Web approval card in the same Store and invalidates late native answers. Never a model tool for answering on the owner's behalf |
| `collaboration.execute` | Runtime tool arguments with required `action` | Complete Runtime route registered by Hermes; `describe` returns `actions` and `action_fields`. Other fields depend on the action. Not supplied by standalone |
| `conversation.send` | Required `text`; optional `conversation_id` | When Hermes enables it and pairing permits it, returns `submitted`, conversation ID, turn ID; no completed answer is implied |
| `conversation.get` | Required `conversation_id` | Up to 100 recent turns for this console/owner conversation: state, text, response, or error |

For requested remote conversations, explicitly add `--allow conversation.send --allow conversation.get` to that console's pairing. Standalone cannot execute these even when they appear in its allowlist. Both `conversation.send` and `messages.send` accept up to 24000 UTF-8 bytes; each console can have at most 100 unfinished conversation turns.

Web controls require their respective authorized methods, for example `--allow contacts.add --allow contacts.respond --allow messages.send --allow inbox.mark_read --allow approval.respond`, plus `contacts.requests` / `attention.list` for those feeds. To use the complete installed Runtime action set through Web chat or the capability form, add `--allow collaboration.execute`, then call it with:

```json
{"action":"describe"}
```

This is the method's params object, not a complete wire envelope. `action_fields` gives required and optional fields for each action. Native and paired conversations use the same `agent_comm_collaboration` Runtime/Store: `prepare_contact`, `contact_requests`, `prepare_contact_response`, `prepare_message`, `inbox`, `mark_read`, and the existing resource/task/collaboration abilities. Write tools require `collaboration.execute`; read tools still check their corresponding read permission.

When `prepare_*` creates an approval, native `confirm` can show the host question card. Remote `confirm` returns `approval_required` until the user decides in the Web approval card or native conversation, then reads that committed decision. The model must never call `approval.respond` to answer for the owner. Friend/message approvals place the exact authorized content in the durable outbox; scoped collaboration actions retain their `dispatch` workflow. Contact trust, chat text and peer requests cannot bypass these permissions.

RPC uses a stable `request_id` and a deadline no more than 300 seconds away. Retry the same request unchanged; never reuse its ID for different content. Generic execution interrupted between commit and response may return `uncertain` on replay: inspect agent state, sent messages and approvals before deciding what to do next, without automatically repeating it under a new ID. Transport `accepted`, friend `connected`, RPC `submitted` and completed model turns are different states. Clients display the agent's persisted results; expired presence becomes `unknown`, and past platform registration does not establish current online status.

See [remote.py](../python/agent_comm_runtime/remote.py) for custom-client correlation, replay, durable responses, and revocation checks, and [daemon.py](../python/agent_comm_runtime/daemon.py) for CLI arguments. Do not fall back to arbitrary shell or generic text execution for unregistered methods.
