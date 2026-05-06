#!/usr/bin/env python3
"""
Dry-run version of receive_and_process.py.
Simulates the full message processing flow without:
- Marking messages as read
- Writing to KG
- Sending reply messages
- Sending Feishu notifications

Usage:
  python3 receive_and_process_dryrun.py [--server-url http://127.0.0.1:18792]
"""

import argparse
import json
import os
import sys
import urllib.request
from datetime import datetime, timezone
from pathlib import Path

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from paths import AUTH_TOKEN_FILE, LISTEN_HOST, LISTEN_PORT, CONTACTS_DIR

KG_VENV = "/home/shiyao/.openclaw/venvs/kg/bin/python3"
KG_QUERY = "/home/shiyao/.openclaw/workspace/skills/knowledge-graph/scripts/query_natural.py"


def get_auth_token() -> str:
    if os.path.exists(AUTH_TOKEN_FILE):
        with open(AUTH_TOKEN_FILE) as f:
            return json.load(f).get("token", "")
    print(json.dumps({"error": f"auth token file not found: {AUTH_TOKEN_FILE}"}))
    sys.exit(1)


def get_server_url(args) -> str:
    return args.server_url or f"http://{LISTEN_HOST}:{LISTEN_PORT}"


def validate_schema(decrypted_text: str):
    """Validate decrypted JSON has required fields. Returns (ok, msg_type, error)."""
    try:
        obj = json.loads(decrypted_text)
    except Exception:
        return False, None, "payload is not valid JSON"
    if not isinstance(obj, dict):
        return False, None, "payload must be a JSON object"
    msg_type = obj.get("type", "")
    valid_types = {"notification", "request", "ack"}
    if msg_type not in valid_types:
        return True, msg_type or "unknown", None
    return True, msg_type, None


def resolve_peer(sender_fp: str):
    """Resolve peer ID and display name from contacts."""
    peer_id = None
    display_name = None
    contacts_dir = Path(CONTACTS_DIR)
    if contacts_dir.is_dir():
        for cf in sorted(contacts_dir.glob("peer-*.json")):
            try:
                with open(cf) as f:
                    contact = json.load(f)
                if contact.get("_fingerprint") == sender_fp:
                    peer_id = cf.stem.removeprefix("peer-")
                    display_name = contact.get("displayName") or contact.get("agentId", "")
                    break
            except Exception:
                continue
    return peer_id, display_name


def query_kg_policy():
    """Query KG for routing policy, Bill preferences, and peer policy."""
    import subprocess
    try:
        result = subprocess.run(
            [KG_VENV, KG_QUERY,
             "routing-policy-agent-comm Bill 的 agent-comm 偏好 peer 策略"],
            capture_output=True,
            text=True,
            timeout=30,
        )
        if result.returncode == 0:
            return json.loads(result.stdout)
    except Exception as e:
        print(f"[dryrun] KG query failed: {e}", file=sys.stderr)
    return {}


def simulate_routing(msg_type: str, decrypted: str | None):
    """Simulate routing decision based on KG policy."""
    policy = query_kg_policy()

    routing = {
        "notification": "log-only",
        "request": "kg-query",
        "ack": "skip",
        "unknown": "require-fix",
    }
    handle_type = routing.get(msg_type, "skip")

    return {
        "would_reply": handle_type in ("kg-query", "require-fix"),
        "would_log_kg": handle_type in ("log-only", "kg-query", "require-fix"),
        "handle_type": handle_type,
    }


def main():
    parser = argparse.ArgumentParser(
        description="Dry-run: simulate agent-comm message processing."
    )
    parser.add_argument(
        "--server-url", default=None,
        help=f"Base URL (default: http://{LISTEN_HOST}:{LISTEN_PORT})"
    )
    parser.add_argument(
        "--dry-run-label", default="DRY-RUN",
        help="Label shown in output"
    )
    args = parser.parse_args()

    auth_token = get_auth_token()
    server_url = get_server_url(args)

    # Don't mark_read in dry-run mode
    url = f"{server_url}/agent-comm/messages"
    req = urllib.request.Request(
        url,
        headers={"Authorization": f"Bearer {auth_token}"},
    )

    try:
        with urllib.request.urlopen(req, timeout=10) as resp:
            data = json.loads(resp.read())
    except Exception as e:
        print(json.dumps({"error": str(e)}))
        sys.exit(1)

    messages = data.get("messages", [])
    if not messages:
        print(json.dumps({
            "mode": "dry-run",
            "empty": True,
            "label": args.dry_run_label,
        }))
        sys.exit(0)

    msg = messages[-1]
    enc = msg.get("encrypted", {})
    sender_fp = enc.get("from", "")
    peer_id, display_name = resolve_peer(sender_fp)

    decrypted = msg.get("decrypted")
    decrypt_error = msg.get("decrypt_error")

    result = {
        "mode": "dry-run",
        "label": args.dry_run_label,
        "id": msg.get("id"),
        "receivedAt": msg.get("receivedAt"),
        "from": sender_fp,
        "peerId": peer_id,
        "displayName": display_name,
    }

    if decrypt_error:
        result["decrypt_error"] = decrypt_error
        result["would_handle"] = False
        result["reason"] = "decrypt_error"
    else:
        result["decrypted"] = decrypted
        if decrypted:
            ok, msg_type, schema_err = validate_schema(decrypted)
            result["msg_type"] = msg_type
            if not ok:
                result["schema_error"] = schema_err

            routing = simulate_routing(msg_type, decrypted)
            result["would_handle"] = routing["handle_type"] not in ("skip",)
            result["routing_decision"] = routing
        else:
            result["would_handle"] = False

    print(json.dumps(result, indent=2))
    sys.exit(0)


if __name__ == "__main__":
    import urllib.request
    main()