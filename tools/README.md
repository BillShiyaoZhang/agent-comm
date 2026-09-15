# Maintenance tools

| Tool | Purpose |
| --- | --- |
| `register_agent.py` | Register a helper identity using signed HTTP requests; accepts `--keys-dir` and `--platform-url` |
| `release_manifest_fetch.py` | Select and verify Release assets from the machine-readable manifest |
| `test_helper_platform.py` | Isolated helper/Platform process regression; also imported by downstream full-stack checks |

Commands run from the SDK repository root. The process test stays here to preserve downstream imports; standalone Go platform checks are under [tests/integration](../tests/README.md). One-off patch and commit scripts are not maintained tooling.
