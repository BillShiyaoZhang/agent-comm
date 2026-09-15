---
name: agent-comm
description: Install, update and use the Agent Comm helper, shared Python runtime and native host connectors for authorized agent identity, reliable messaging and collaboration.
---

# Agent Comm installation and use

## Entry points

Use this skill when the user requests Agent Comm setup, upgrades or communication with an authorized agent. Read the [README](README_EN.md), then the applicable [Hermes](connectors/hermes-platform/README.md) or [OpenClaw](connectors/openclaw-channel/README.md) connector. Hermes personal collaboration follows its packaged [personal-collaboration skill](connectors/hermes-platform/hermes_platform_agent_comm/skills/personal-collaboration/SKILL.md).

## Install or update

1. Identify the actual OS, host installation, Python environment and profile. For Hermes, resolve the active profile through `hermes_constants.get_hermes_home()`.
2. Prefer the matching [early access package](https://github.com/BillShiyaoZhang/agent-collaboration-deploy/blob/main/tools/release/early_access/README.md). Preserve existing identity keys, mailbox databases, contacts, grants and consumption records.
3. Install the Python runtime and connector into the environment that runs the host. Follow the connector's supported host revision and native lifecycle requirements.
4. When building source, run `go build -o build/agent-comm-helper ./cmd/helper` from the SDK root. The Release downloader is `tools/release_manifest_fetch.py`; use `--helper` for current helper assets and follow the [Release guide](docs/guides/RELEASES.md).
5. Use `agent-comm-helper init <absolute_keys_dir>` to inspect/create the identity, then `agent-comm-helper daemon <absolute_keys_dir> <cloud_https_url> [local_port]`. The default local port is 45042. Run one helper per identity and one active consumer per inbox.
6. Hermes `platform_url` points to local `http://127.0.0.1:45042`. Set the local URN and explicit allowed peer addresses. Personal collaboration uses `collaboration_enabled`; remote workspace access separately requires `remote_enabled` and local pairing.

## Runtime contract

The trusted local API offers `/info`, durable `POST /api/v1/mq/store`, local message status, retrieve/SSE and consumer ACK. Exact fields, limits and retries live in the [helper contract](docs/guides/HERMES_INTEGRATION.md). Cloud endpoints with similar names expect signed encrypted requests and cannot substitute for the local API.

Use stable message IDs across retries. SSE can replay unconsumed messages; acknowledge the local helper after completed processing or durable acceptance. The helper reliably sends through HTTPS MQ. `accepted` means local durable acceptance and `platform_queued` means platform queueing, neither proves recipient task completion. The traditional Go P2P/Double Ratchet route is separate.

## Verify and maintain

Check the actual identity at `/info`, the host's live SSE connection and a two-way exchange within the user's authorized recipients and content. A contact address does not grant control authority; use native host confirmation and explicit remote pairing.

Report local tests, transport delivery and real model outcomes separately. Persistent operation follows the [service guide](docs/guides/HELPER_SERVICE.md); implementation limits and validation commands are in [engineering](docs/guides/ENGINEERING.md).
