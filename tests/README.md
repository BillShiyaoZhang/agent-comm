# Validation

## Automated package tests

From the SDK repository root:

```sh
go test ./...
python -m unittest discover -s python/tests -q
```

Install the runtime or include `python/` in `PYTHONPATH`. Hermes tests need its native host dependencies; follow the [connector README](../connectors/hermes-platform/README.md).

`agent/platform_real_test.go` is explicitly gated by `TEST_REAL_PLATFORM=true` and is excluded from ordinary local runs. Do not enable it for routine regression: it targets a deployed platform and configured remote agent.

## Isolated helper/platform processes

Build matching helper and Platform executables, then run:

```sh
python tools/test_helper_platform.py --helper build/agent-comm-helper --platform /absolute/path/to/platform
```

The tool uses local processes and isolated identities. Its path remains stable because the deployment and Web repositories import its process helpers.

## Explicit platform checks

These standalone programs register temporary identities and exercise a specified test platform. They do not run merely because `go test ./...` compiles their packages.

```sh
go run ./tests/integration/platform_http -target http://127.0.0.1:8080
go run ./tests/integration/platform_p2p -target /ip4/127.0.0.1/tcp/45041/p2p/PLATFORM_PEER_ID
```

Use an isolated Platform instance and its actual PeerID. P2P requires an explicit target multiaddr; neither program infers a production endpoint. Productive agent execution is outside these transport tests.
