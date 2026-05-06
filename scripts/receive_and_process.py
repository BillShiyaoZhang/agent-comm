#!/usr/bin/env python3
"""
Fetch the latest unread message from the agent-comm queue and print it as
clean JSON. Marks the message as read before emitting it.

Exit codes:
  0  → message output (or empty marker)
  1  → server / network error

Usage:
  python3 receive_and_process.py [--server-url http://127.0.0.1:18792]
"""

import argparse
import json
import os
import sys
import threading
import urllib.request
import urllib.parse
from datetime import datetime, timezone
from pathlib import Path

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from paths import AUTH_TOKEN_FILE, LISTEN_HOST, LISTEN_PORT, CONTACTS_DIR

# Replay detection cache (in-process, shared across calls)
_SEEN_IDS: dict[str, float] = {}
_SEEN_LOCK = threading.Lock()
_SEEN_TTL = 600  # 10 minutes
_SEEN_FILE = os.path.join(os.path.dirname(os.path.abspath(__file__)), "seen_message_ids.json")


def get_auth_token() -> str:
    if os.path.exists(AUTH_TOKEN_FILE):
        with open(AUTH_TOKEN_FILE) as f:
            return json.load(f).get("token", "")
    print(json.dumps({"error": f"auth token file not found: {AUTH_TOKEN_FILE}"}))
    sys.exit(1)


def get_server_url(args) -> str:
    return args.server_url or f"http://{LISTEN_HOST}:{LISTEN_PORT}"


def _load_seen() -> dict[str, float]:
    try:
        with open(_SEEN_FILE) as f:
            return json.load(f)
    except Exception:
        return {}


def _save_seen(data: dict[str, float]) -> None:
    with open(_SEEN_FILE, "w") as f:
        json.dump(data, f)


def check_replay(msg_id: str) -> bool:
    """Return True if msg_id has already been seen within TTL. Mutates cache."""
    now = datetime.now(timezone.utc).timestamp()
    with _SEEN_LOCK:
        seen = _load_seen()
        expired = [mid for mid, exp in seen.items() if exp < now]
        for mid in expired:
            del seen[mid]
        if msg_id in seen:
            return True
        seen[msg_id] = now + _SEEN_TTL
        _save_seen(seen)
    return False


def validate_schema(decrypted_text: str) -> tuple[bool, str | None, str | None]:
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
        # Unknown type is valid schema but triggers require-fix flow
        return True, msg_type or "unknown", None

    return True, msg_type, None


def main():
    parser = argparse.ArgumentParser(description="Fetch latest unread agent-comm message.")
    parser.add_argument(
        "--server-url",
        default=None,
        help=f"Base URL (default: http://{LISTEN_HOST}:{LISTEN_PORT})",
    )
    parser.add_argument(
        "--raw",
        action="store_true",
        help="Print raw queue entry JSON (pre-decrypt) instead of decrypted content.",
    )
    args = parser.parse_args()

    auth_token = get_auth_token()
    server_url = get_server_url(args)

    url = f"{server_url}/agent-comm/messages?mark_read=1"
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
        # No messages — emit empty marker and exit cleanly
        print(json.dumps({"empty": True}))
        sys.exit(0)

    # Take the last (newest) entry
    msg = messages[-1]
    msg_id = msg.get("id", "")

    # ── Replay check ────────────────────────────────────────────────────────
    if msg_id and check_replay(msg_id):
        print(json.dumps({"replay": True, "id": msg_id}))
        sys.exit(0)

    if args.raw:
        print(json.dumps(msg))
        sys.exit(0)

    # Resolve peer fingerprint
    enc = msg.get("encrypted", {})
    sender_fp = enc.get("from", "")

    # Resolve peer ID from contacts
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

    decrypted = msg.get("decrypted")
    decrypt_error = msg.get("decrypt_error")

    result = {
        "id": msg_id,
        "receivedAt": msg.get("receivedAt"),
        "from": sender_fp,
        "peerId": peer_id,
        "displayName": display_name,
    }

    if decrypt_error:
        result["decrypt_error"] = decrypt_error
    else:
        result["decrypted"] = decrypted
        # ── Schema validation ────────────────────────────────────────────
        if decrypted:
            ok, msg_type, schema_err = validate_schema(decrypted)
            result["msg_type"] = msg_type
            if not ok:
                result["schema_error"] = schema_err

    print(json.dumps(result))
    sys.exit(0)


if __name__ == "__main__":
    main()
