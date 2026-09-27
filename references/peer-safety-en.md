# Peer blocking and owner content review

[中文](peer-safety.md) · [Remote pairing and methods](remote-control-en.md)

This describes the updated Python Runtime and Hermes connector source. Older installations do not gain these features automatically. Install matching packages, preserve identity/state, and inspect actual `capabilities` responses. Neither read-only pairing nor `collaboration.execute` implicitly grants owner review or blocking permissions.

Store upgrades SQLite schema 1 to 2 in one direction, preserving records and owner/pairing namespaces. Older runtimes reject schema 2 instead of bypassing historical-content review through old projections. Update Gateway and desktop backends together; do not reopen an upgraded database with an older wheel.

Before upgrading, stop all consumers for the helper and back up collaboration, remote and receipt databases with associated WAL files. The schema update commits in one SQLite write transaction; owner review records are subsequently added during projection, which always rejects unapproved content. Reinstalling an older wheel is not a rollback plan: an old database backup lacks newer block/review decisions. Any restore requires an offline assessment that preserves safety state. Never run old/new components against the same database together.

## Persistent blocking

| RPC | Permission | params | Receipt |
| --- | --- | --- | --- |
| `contacts.block` | Separate WRITE | `{urn}` | `{urn,status:"blocked",blocked:true,connection_status:"blocked",safety_revision}` |
| `contacts.unblock` | Separate WRITE | `{urn}` | `{urn,status:"unblocked",blocked:false,connection_status,safety_revision}` |

Supply an actual peer URN; the local Agent cannot be blocked. The persistent ACL is scoped to owner+URN. Helper key-cache removal, `trusted`, and local list hiding are not this ACL.

`contacts.list` and `collaboration.state` include an integer `safety_revision`, initially 0. An actual blocked-state change increments it in the same transaction. Contact entries include `blocked` and use `connection_status:"blocked"` when blocked. Independent `blocked_peers[]` entries contain `{urn,blocked:true,connection_status:"blocked",blocked_at}`, including senders without a saved contact.

RPC replay returns the original receipt and revision. Clients merge safety state only at revisions at least as new as the current value; delayed snapshots/historical receipts cannot overwrite later block/unblock decisions. Explicitly authorize `--allow contacts.block --allow contacts.unblock`; do not expand old pairings on upgrade.

Blocking retires friend requests, inbound business content, queued sends, unfinished collaboration actions, associated approvals and background workers. Inbound traffic is ACKed only after durable quarantine, without starting a private model. Helper admission and its last ACL check share a SQLite write transaction, preventing old queued sends after a completed block. Already accepted messages or external effects cannot be recalled.

Historical messages and unfinished work retain terminal tombstones. Unblocking admits only future fresh content under connection/review rules. It does not restore old messages, approvals, workers, maintenance permissions or queues; fresh owner intent is required. Blocking is not remote pairing revocation, global erasure or a prohibition on the peer submitting to the platform.

## Full preview before local content use

Normal `inbox.list` and `collaboration.state` return metadata only:

```json
{"pending_review":[{"message_id":"actual-id","sender_urn":"actual-sender-URN","kind":"chat.message","received_at":2000000000,"status":"pending"}],"review_policy":{"version":1,"approval_scope":"local_content_use_only"}}
```

Up to 100 pending metadata entries are returned per read; subsequent reads after decisions reveal remaining entries. This contains no body and is not a Web/App display-safety attestation.

| RPC | Permission | params | Result |
| --- | --- | --- | --- |
| `inbox.review_preview` | Separate READ | `{message_id}` | `{message_id,sender_urn,kind,text,received_at,status,fingerprint,text_truncated:false}` |
| `inbox.review` | Separate WRITE | `{message_id,decision:"approve"\|"reject"}` | `{message_id,sender_urn,status:"approved"\|"rejected",fingerprint}` |

`fingerprint` is SHA-256 of the immutable complete wire record's canonical JSON. Changed content, identity or associations cannot reuse the record. Approval requires a successful complete preview from the same actual owner session/paired console. Truncation or response overflow rejects the preview without recording approval eligibility. The UI must explicitly let the owner inspect, verify and decide. Models, service operators and `collaboration.execute` cannot answer for the owner.

Rejection needs no preview. Both decisions are terminal: an identical decision returns the same result; an opposite decision fails. Blocked content cannot be approved. Rejected/blocked history never automatically replays, and approval neither creates a model turn nor sends a message. This permits local content reading/handling, without replacing friendship acceptance, identity verification, task scope, business approval, disclosure or third-party AI consent.

Free text and v2 `invite`, `proposal`, `change_request` default to quarantine. Historical free-text events nested in `sync_response` use the same owner gate. Inbox, proposal projections, operation text, attention details and workers cannot bypass it. Pre-upgrade applied records acquire review gates during projection. Typed receipts/ACK/recovery evidence still advance the protocol state machine; raw wire text stays out of normal inbox/model reads. Fixed friend requests/responses retain their independent acceptance workflow.

## Host and display boundaries

Updated `capabilities` includes the actual declaration:

```json
{"peer_content_safety":{"version":1,"mode":"owner_review","automatic_peer_model_execution":false}}
```

Both Hermes modes persist business input and ACK without creating a direct Gateway model turn. Owners can subsequently handle approved content from an authorized native/paired owner turn. Personal collaboration mode still disables direct sends; the legacy proactive-send path also checks the persistent ACL. A workspace can require this declaration before starting new private assistant conversations. Never synthesize it for an old Agent or low-level helper.

A conversational host must explicitly attest its updated admission path in trusted construction code using `RemoteBridge(..., conversations=True, peer_content_safety=True)`. The updated Hermes adapter supplies this binding. A new Runtime does not make the claim for an old adapter that omits it. Standalone, without a model consumer, can attest its actual boundary; configuration prose or model arguments are not host verification.

Bridge checks current host admission before a new `conversation.send`, request replay and queued-turn claim, independently of cached client capabilities. An unaccepted new request returns `peer_content_safety_required` without creating a turn. Replaying an accepted request after a host downgrade returns `peer_content_safety_changed`: read the existing conversation to reconcile its state, rather than assuming it never executed. Previously submitted turns fail without dispatch; `conversation.get` can still read existing records.

Trusted hosts can use `Store.set_peer_block`, `review_preview`, `review_peer`; owner identity/decisions must originate in trusted UI. These are not model Runtime actions. `describe.owner_content_safety` discloses this separation.

Web operator moderation/reporting and local owner review are separate authorities. `review_policy` does not attest that a Web/App public projection was filtered; an operator's display approval cannot authorize the owner's model consumption. Reporting is outside these four RPC methods.

This ACL/review boundary protects the updated Runtime and connector's supported paths. The low-level Go helper handles identity, encryption and persistence without querying Python owner ACLs. Raw retrieval, arbitrary local shell access, other host plugins and already copied content are outside this boundary. Other hosts must use the same durable gate before any model consumption or normal UI projection. Key-cache removal and keyword lists cannot substitute for blocking.

Tests cover temporary SQLite, independent owners, concurrent transactions, replay, nested recovery and isolated imports of real connector source. Connector tests substitute host/aiohttp seams and do not establish a verified real Hermes installation. Recheck in the actual matching host environment before release.
