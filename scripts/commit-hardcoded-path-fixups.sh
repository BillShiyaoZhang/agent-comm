#!/usr/bin/env bash
# Commit script for the hardcoded-path/URN-prefix/platform-domain fixes.
#
# Run this from the repository root on a host that has working access to
# `.git/` (the Windows bind-mount used inside the agent container does not
# expose the submodule's git directory, so this script cannot be executed
# from within that environment).
#
# Each commit below groups a single logical change so reviewers can read
# history in isolation. The script does not push — review and push manually
# after running.

set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

# ---------------------------------------------------------------------------
# Commit 1: URN prefix + default keys dir
#   - DefaultURNPrefix -> "urn:agent-comm:agent"
#   - IdentityKeyPair now carries URNPrefix
#   - LoadOrCreateIdentity keeps the old signature for backward compat and
#     exposes LoadOrCreateIdentityWithOptions for new callers
#   - DefaultKeysDir() now returns ~/.agent-comm/keys
#   - agent/config.go gains URNPrefix; agent/agent.go plumbs it through
#   - agent/contact_card.go and agent/handler.go use the new prefix
#   - registry/client.go URNFromEd25519PK uses crypto.DefaultURNPrefix
# ---------------------------------------------------------------------------
git add \
    crypto/keys.go \
    agent/config.go \
    agent/contact_card.go \
    agent/handler.go \
    agent/integration_test.go \
    registry/client.go
git commit -m "feat(crypto): unify URN prefix to urn:agent-comm:agent and default keys dir to ~/.agent-comm/keys

- Introduce crypto.DefaultURNPrefix = \"urn:agent-comm:agent\".
- Add IdentityOptions + LoadOrCreateIdentityWithOptions; the original
  LoadOrCreateIdentity signature is preserved.
- DefaultKeysDir() now returns ~/.agent-comm/keys (Windows:
  %APPDATA%\\agent-comm\\keys) instead of the framework-specific
  ~/.hermes/agent-comm/contacts path.
- Plumb URNPrefix through agent.Config -> crypto.IdentityOptions.
- Sync URNFromEd25519PK and the responder fallback in agent/handler.go.
- Update registry/client.go to use the shared prefix constant.

BREAKING CHANGE: existing agents whose keys live under
~/.hermes/agent-comm/contacts must be re-initialised (or moved) to keep
their URNs discoverable. Old URNs registered under the
\"urn:hermes:agent:\" namespace no longer match the new prefix."

# ---------------------------------------------------------------------------
# Commit 2: PlatformConfig + DNSCache
# ---------------------------------------------------------------------------
git add \
    agent/platform.go \
    agent/dns_cache.go \
    agent/platform_dns_test.go
git commit -m "feat(agent): introduce PlatformConfig and DNSCache with TTL + IP fallback

- PlatformConfig centralises the public Platform defaults (HTTPS URL,
  P2P domain/port/peer ID) and reads AGENT_PLATFORM_* env vars to
  override them.
- DNSCache memoises net.DefaultResolver.LookupHost results on disk
  (os.UserCacheDir()/agent-comm/dns_cache.json) with a 1h default
  TTL. On DNS failure it serves the stale entry so callers stay
  connected.
- Pure-function extractHost avoids pulling net/url into the package
  just for one URL parse.
- Adds 7 unit tests covering miss/refresh, TTL expiry, stale fallback
  on DNS outage, disk persistence, URL parsing and config env
  overrides."

# ---------------------------------------------------------------------------
# Commit 3: Helper daemon uses the new config + DNS cache for bootstrap
# ---------------------------------------------------------------------------
git add cmd/helper/daemon.go
git commit -m "refactor(helper): resolve Platform bootstrap via DNS + cached IPs

- Drop the IP-only fallback table; fall back to the well-known
  DefaultPlatformPeerID whenever the host matches DefaultPlatformDomain
  or the legacy \"agent-communication.online\" hostname.
- Build multiaddrs as both /dns4/<host>/... and /ip4/<cached>/... so
  libp2p can dial even when DNS is unreachable.
- Use the new agent.DNSCache to populate the IP fallbacks and refresh
  cached resolutions opportunistically.
- Pull transport-specific port from agent.DefaultPlatformP2PPort
  instead of inlining 45041."

# ---------------------------------------------------------------------------
# Commit 4: Replace IP literal with HTTPS + domain across runtime/docs
# ---------------------------------------------------------------------------
git add \
    agent/platform_real_test.go \
    cmd/platform_test/main.go \
    cmd/test_session/main.go \
    tools/register_agent.py \
    scratch/test_integration.py \
    connectors/cli/bin/cli.js \
    README.md \
    README_EN.md \
    OVERVIEW.md \
    OVERVIEW_EN.md
git commit -m "chore: switch default Platform endpoint to https://agent-communication.online

- HTTP API default flips from http://8.130.40.38 to
  https://agent-communication.online, with strict TLS verification.
- P2P multiaddrs now use /dns4/agent-communication.online/... so
  libp2p can re-resolve at dial time. Cached IPs are injected
  alongside via the new DNSCache.
- Default PlatformPeerID/URN move to the agent-comm namespace to
  match the new URN scheme.
- CLI scripts and integration tests read AGENT_PLATFORM_URL /
  AGENT_PLATFORM_PEER_ID env vars when present.
- Document the new endpoint in README, OVERVIEW and the .md files
  that ship with the agent on disk.

Backward compatible: scripts that still pass an explicit IP or old
URL continue to work; the helper now resolves them via the same
code path."

# ---------------------------------------------------------------------------
# Commit 5: Hermes-platform adapter cleanup
# ---------------------------------------------------------------------------
git add \
    connectors/hermes-platform/hermes_platform_agent_comm/platform.py \
    connectors/hermes-platform/tests/test_platform.py
git commit -m "refactor(hermes-adapter): derive debug log path from OS conventions

- Remove the hardcoded /Users/shiyaozhang/Developer/... debug log
  path. The adapter now picks a per-OS state/log directory
  (macOS: ~/Library/Logs/agent-comm/debug.log,
   Linux:  $XDG_STATE_HOME/agent-comm/debug.log,
   Windows: %LOCALAPPDATA%\\agent-comm\\Logs\\debug.log)
  and respects config.extra[\"debug_log_path\"] when set.
- Drop the implicit ~/.hermes/keys/agent-comm/contacts.db
  lookup; the adapter now only consults the keys_path passed via
  config plus the neutral ~/.agent-comm/keys/contacts.db default.
- Update test_platform.py URN literal to the new namespace."

# ---------------------------------------------------------------------------
# Commit 6: CLI keys_dir unification
# ---------------------------------------------------------------------------
git add connectors/cli/bin/cli.js
git commit -m "refactor(cli): unify keys_dir to ~/.agent-comm/keys

- Replace the OpenClaw / Hermes branch with a single default
  ~/.agent-comm/keys. Operators can still override via --keys-dir
  on the command line or AGENT_COMM_KEYS_DIR in the environment.
- settings.json and config.yaml now receive the resolved absolute
  path instead of a literal \"~/.openclaw/...\" / \"~/.hermes/...\"
  string, removing the implicit assumption that the framework
  expands the tilde."

# ---------------------------------------------------------------------------
# Commit 7: Bootstrap node + proto URN namespace tidy
# ---------------------------------------------------------------------------
git add \
    cmd/bootstrap/main.go \
    proto/agentcomm.proto \
    proto/agentcomm.pb.go \
    proto/wot.proto \
    proto/wot.pb.go \
    contacts/contact.go
git commit -m "chore: switch bootstrap DB defaults and proto comments to agent-comm namespace

- cmd/bootstrap/main.go: relay_mq.db and relay_wot.db now default to
  os.UserConfigDir()/agent-comm/ instead of /tmp/. The MQ_DB_PATH
  and WOT_DB_PATH env vars still take precedence.
- proto/*.proto: doc comments reflect the new urn:agent-comm:agent:
  prefix.
- Generated .pb.go and contacts/contact.go updated for consistency."

# ---------------------------------------------------------------------------
# Commit 8: Test code cleanup + delete obsolete scripts
# ---------------------------------------------------------------------------
git add \
    cmd/test_wot/main.go \
    cmd/test_mq/main.go \
    cmd/test_session/main.go \
    cmd/platform_test/main.go \
    cmd/platform_http_test/main.go
# agent/modify_dr.sh and agent/modify_dr2.sh were already removed from disk.
# Use `git add -u` so the deletions get picked up.
git add -u agent/modify_dr.sh agent/modify_dr2.sh 2>/dev/null || true
git commit -m "test: replace /tmp/ fixtures with os.TempDir() and drop custom filepathJoin

- All standalone cmd/test_*/main.go programs now anchor fixtures
  under filepath.Join(os.TempDir(), \"agent_comm_...\") so they work
  on Windows and on hosts where /tmp is read-only or size-limited.
- cmd/platform_http_test/main.go drops the custom filepathJoin
  helper in favour of path/filepath.Join (already in stdlib).
- agent/modify_dr.sh and agent/modify_dr2.sh are removed; the
  current agent.go already implements the double-ratchet initiator
  logic these scripts were patching in manually."

echo "All commits created. Review with: git log --oneline -8"