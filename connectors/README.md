# Host connectors

| Connector | Scope |
| --- | --- |
| [Hermes](hermes-platform/README.md) | Native Gateway integration, durable consumption, shared collaboration runtime and remote pairing |
| [OpenClaw](openclaw-channel/README.md) | Basic channel send/receive bridge; does not itself implement the shared collaboration runtime |

Shared authorization, state and adapter contracts belong in [python/](../python/README.md). Connector-specific behavior and tests remain with the host package. Adding a host requires its real native lifecycle and confirmation interfaces; a generic message bridge is not sufficient evidence that collaboration or remote control works.
