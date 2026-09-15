# Remote console: pairing and methods

Read for local pairing administration or Web/host integration with the installed `agent-comm-runtime`. It exchanges `control.request` / `control.response` through the helper's encrypted mailbox, without exposing an additional local HTTP listener. Ordinary contacts do not receive console authority.

## Local CLI

Establish the actual profile, verified console URN, permitted methods, and expiry. Do not guess a real profile directory. Argument templates:

```text
agent-comm-runtime remote pair --hermes-profile <actual_profile> --console-urn <console_urn> --allow capabilities --allow contacts.list --allow collaboration.state --allow inbox.list --expires <RFC3339_expiry_with_timezone>
agent-comm-runtime remote pairings --hermes-profile <actual_profile>
agent-comm-runtime remote revoke --hermes-profile <actual_profile> --console-urn <console_urn>
```

`pair` writes a local allowlist and returns `paired_locally`, granting the specified console access to the owner's selected data/methods. Use the user's authorized console and scope; adding a communication contact does not implicitly authorize pairing. `pairings` lists existing records; `revoke` revokes the console, without recalling disclosed data. Expiry requires seconds and a timezone; use the actual authorized lifetime.

All four subcommands accept `--state <remote.sqlite3>`, defaulting to `<profile>/agent-comm/remote.sqlite3`. For a non-Hermes host, supply `--state` and `--owner-principal` explicitly for pairing. The principal comes from the real host configuration; do not impersonate a different owner.

## Choose one consumer

- **Hermes Gateway**: enable `remote_enabled: true` in agent_comm `extra`, optionally setting `remote_state_path`. The plugin binds the profile's stable owner principal and executes real Hermes conversations. See the [plugin instructions](../connectors/hermes-platform/README.md).
- **Standalone**: when Hermes is not consuming that inbox, run the read-only bridge:

```text
agent-comm-runtime remote serve --hermes-profile <actual_profile> --agent-urn <local_agent_urn> --helper-url http://127.0.0.1:45042 --once
```

`--once` processes one bounded batch and exits; omit it for continuous consumption. `--collaboration-state <collaboration.sqlite3>` overrides the default file under the profile's `agent-comm/` directory. Non-Hermes operation requires `--state` and `--collaboration-state`. Use the local URN returned by helper `/info` and the actual configured helper URL.

Do not run standalone and Hermes consumers for the same helper or add an ordinary collaboration consumer that competes to ACK `control.*`. Standalone persists ordinary inbound messages but does not start a model, wake the owner's native conversation, or execute `conversation.send`.

## Implemented RPC methods

Availability depends on both the pairing allowlist and the host adapter. First call authorized `capabilities` and check each method's `available` value; names alone do not prove support.

| Method | params | Result/boundary |
| --- | --- | --- |
| `capabilities` | `{}` | Protocol, method availability/reasons, pairing expiry |
| `contacts.list` | `{}` | Runtime contacts for the paired owner's profile, separate from helper key caches |
| `collaboration.state` | Optional `task_id` | Contacts, tasks, actions, pending confirmations, inbox, and proposals; no approval authority |
| `inbox.list` | Optional `task_id` | Previously persisted runtime inbox, without a fresh helper retrieval |
| `conversation.send` | Required `text`; optional `conversation_id` | When Hermes enables it and pairing permits it, returns `submitted`, conversation ID, turn ID; no completed answer is implied |
| `conversation.get` | Required `conversation_id` | Up to 100 recent turns for this console/owner conversation: state, text, response, or error |
| `approval.respond` | Unavailable | Always unsupported; owner confirmation requires native trusted interaction |

For requested remote conversations, explicitly add `--allow conversation.send --allow conversation.get` to that console's pairing. Standalone cannot execute these even when they appear in its allowlist. `conversation.send` accepts up to 24000 UTF-8 bytes; each console can have at most 100 unfinished turns.

RPC uses a stable `request_id` and a deadline no more than 300 seconds away. Retry the same request unchanged; never reuse its ID for different content. Transport `accepted`, RPC `submitted`, and completed model turns are different states. Peer messages, pairing, and remote turns never become the runtime's native owner approval context.

See [remote.py](../python/agent_comm_runtime/remote.py) for custom-client correlation, replay, durable responses, and revocation checks, and [daemon.py](../python/agent_comm_runtime/daemon.py) for CLI arguments. Do not fall back to arbitrary shell or generic text execution for unregistered methods.
