#!/bin/bash
# Start OpenClaw Gateway + agent-comm server + Cloudflare Tunnel
#
# Usage: ./start-claw.sh
# Add to ~/.bashrc or systemd service for automatic startup on boot.

set -e

# Resolve skill directory relative to this script (works when symlinked or cloned anywhere)
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
AGENT_COMM_DIR="$SCRIPT_DIR"

LOG_DIR="/tmp"

# ── agent-comm server ──────────────────────────────────────────
nohup python3 \
  "$AGENT_COMM_DIR/scripts/server.py" \
  > "$LOG_DIR/agent-comm-server.log" 2>&1 &

echo "agent-comm server started (PID $!)"

# ── Cloudflare Tunnel ───────────────────────────────────────────
nohup cloudflared tunnel --url http://localhost:18792 \
  > "$LOG_DIR/cloudflared-tunnel.log" 2>&1 &

echo "cloudflared tunnel started (PID $!)"

# ── OpenClaw Gateway ────────────────────────────────────────────
openclaw gateway start

echo "All services started."