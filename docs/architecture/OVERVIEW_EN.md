# Agent Comm source overview

The SDK provides device-side communication, a shared Python collaboration runtime and host connectors. Start with the [engineering guide](../guides/ENGINEERING.md) for supported installation and runtime behavior.

## Source boundaries

- `cmd/helper/` and `cmd/bootstrap/`: runnable production entry points.
- `agent/`: high-level Go API, contact cards and reliable messaging.
- `crypto/`, `session/`, `dr/`: identities/envelopes, ECIES sessions and Double Ratchet state/persistence.
- `libp2p/`, `dht/`, `registry/`, `mq/`: network hosts, discovery, signed records and encrypted mailboxes.
- `contacts/`, `wot/`, `proto/`: contact storage, trust claims and wire contracts.
- `python/` and `connectors/`: separately packaged runtime and host adapters.
- `examples/`, `tests/integration/`, `tools/`, `docs/`: learning programs, platform checks, operational tools and documentation.

Public Go package paths remain stable for downstream imports. Tests that exercise a package live beside its source; standalone demonstrations belong under `examples/`.

## Message paths

The helper durably accepts local sends, then retries signed encrypted envelopes through HTTPS MQ. The recipient persists its validated inbox before platform ACK; the host acknowledges local consumption separately. Acceptance and delivery do not prove task completion.

The traditional Go `Agent.SendMessage` API has a separate P2P/Double Ratchet route. Static ECIES envelopes use X25519, HKDF-SHA256 and AES-256-GCM with Ed25519 signatures. The `dr` implementation uses XChaCha20-Poly1305. Do not apply Double Ratchet forward-secrecy claims to the helper's static-key HTTPS MQ route.

Platform, Web and deployment have separate repositories. The Python runtime and connectors remain here while they share interface development; existing package boundaries permit independent packaging without another Git repository.

See [protocol sources](PROTOCOL_EN.md), [examples](../../examples/README.md) and [validation](../../tests/README.md).
