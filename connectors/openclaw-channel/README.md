# OpenClaw local-helper bridge

This connector uses the local helper plaintext HTTP/SSE API. Configure a loopback URL such as `http://127.0.0.1:45042`; the cloud platform URL is not compatible with this API.

`sendMessage(recipientUrn, text, metadata?)` now accepts the helper's HTTP 202 response and returns `{success: true, message_id, status}` only after validating its JSON success and ID. `status: accepted` means durable helper outbox admission, not delivery or task completion. Set `metadata.message_id` to the same caller-owned retry key across repeated calls. Network retries inside one call reuse the generated ID. Conversation/task/reply/kind/deadline/hop fields pass through `metadata`.

Incoming `message` events retain `message_id`, `metadata`, `is_bot: true`, and `allow_gateway_control: false`. This repository exposes a generic EventEmitter bridge; those flags do not by themselves configure the host's permissions or session routing. The OpenClaw host must apply its native authorization and bot/control restrictions and map conversation/task IDs to its own independent sessions.

The updated helper replays pending messages. This bridge suppresses repeat emits for an admitted wire ID for the lifetime of the channel instance; no listener or a synchronous listener exception leaves the message eligible for retry. An EventEmitter return value is only an in-memory notification, so the connector **does not automatically ACK on emit**.

After processing succeeds, or after the host has durably accepted a message into its own recoverable queue, it must call the event's `await message.acknowledge()` callback (or `await channel.acknowledge(message.message_id)`). An ACK failure leaves the helper inbox intact; the host can retry that callback. Async handler rejection is not observed by EventEmitter: the host must catch its own errors and retry processing/ACK as appropriate. Without this completion integration, pending messages remain stored in the helper and replay when a new channel instance/process starts. This deliberate migration behavior avoids deleting messages that were only held in RAM.

There is no durable execution ledger in this bridge. A crash after an external side effect but before a successful ACK can cause the message to execute again. Hosts must use task/message IDs for idempotent side effects. Run only one consumer per helper inbox.

Run `npm ci` and `npm test` in this directory. Tests use an ephemeral loopback fake helper and cover HTTP 202/JSON failures, stable send IDs, SSE replay dedup, explicit ACK, failed ACK retry, no-listener replay, restart replay, and SSE reconnect/stop. They do not run an OpenClaw model or a cloud platform.
