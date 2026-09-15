# Protocol reference

Wire definitions live in [proto/](../../proto/agentcomm.proto). Use the source definitions and shared SDK functions when implementing a client; handwritten copies of field numbers or signature concatenation rules drift easily.

| Contract | Source |
| --- | --- |
| Identity and URN | [crypto/keys.go](../../crypto/keys.go) |
| Registry records and signatures | [registry/client.go](../../registry/client.go), [registry/validation.go](../../registry/validation.go), [registry.proto](../../proto/registry.proto) |
| Signed encrypted envelopes | [crypto/envelope.go](../../crypto/envelope.go), [envelope.proto](../../proto/envelope.proto) |
| MQ authorization and ACK | [mq/auth.go](../../mq/auth.go), [mq/client.go](../../mq/client.go), [mq.proto](../../proto/mq.proto) |
| ECIES messages | [crypto/ecies.go](../../crypto/ecies.go), [session/session.go](../../session/session.go) |
| Ratchet state and stream framing | [dr/ratchet.go](../../dr/ratchet.go), [dr/session.go](../../dr/session.go), [dr/store.go](../../dr/store.go) |
| Local HTTP/SSE and delivery states | [helper contract](../guides/HERMES_INTEGRATION.md) |
| Collaboration and remote RPC | [Python runtime](../../python/README.md) |

Registry ownership binds URN, identity-derived PeerID and signed X25519 key. Envelope signatures bind sender, recipient, stable message ID and encryption fields. Retrieve and ACK authenticate the recipient. Use `registry.BuildSignedMsg` and the envelope helpers instead of older draft formulas.

Local acceptance, platform queueing and consumer ACK are separate from business completion. The helper's static-key HTTPS MQ path has different state and encryption properties from the Go P2P Double Ratchet route. See [engineering](../guides/ENGINEERING.md) and [integration checks](../../tests/README.md).
