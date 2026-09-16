# Maintenance tools

| Tool | Purpose |
| --- | --- |
| `register_agent.py` | Register a helper identity using signed HTTP requests; accepts `--keys-dir` and `--platform-url` |
| `release_manifest_fetch.py` | Select and verify Release assets from the machine-readable manifest |
| `test_helper_platform.py` | Isolated helper/Platform process regression; also imported by downstream full-stack checks |

Commands run from the SDK repository root. The process test stays here to preserve downstream imports; standalone Go platform checks are under [tests/integration](../tests/README.md). One-off patch and commit scripts are not maintained tooling.

The release fetcher requires HTTPS downloads (including redirects), a SHA256 digest for every asset, and plain filenames for asset and install names. Local manifest files remain supported. Downloads are bounded to 256 MiB and replace installed files only after digest verification. The optional documentation archive may contain only Markdown files within the output directory; absolute paths, traversal, links and executable files are rejected before extraction. Run its adversarial checks with `python -m unittest discover -s tools -p test_release_manifest_fetch.py`.
