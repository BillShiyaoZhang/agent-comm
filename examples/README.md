# SDK examples

Run these programs from the SDK repository root. They use the public Go packages; they are learning tools, while automated assertions live in package tests.

| Command | Purpose / requirements |
| --- | --- |
| `go run ./examples/agent` | High-level agent API demonstration |
| `go run ./examples/host` | Start a libp2p host; waits until Ctrl+C |
| `go run ./examples/session` | Local two-node ECIES/Registry exchange; uses fixed local test ports |
| `go run ./examples/mailbox` | Local relay, sender and receiver mailbox flow |
| `go run ./examples/trust` | Web-of-Trust claim demonstration |
| `go run ./examples/ratchet` | Exercise the real `dr` package in memory |
| `go run ./examples/ratchet_persistence` | SQLite ratchet-state round trip |

Inspect configuration before running network examples. These programs may bind local ports and create temporary identities/databases. For repeatable regression and isolated helper/platform processes, use [tests](../tests/README.md).
