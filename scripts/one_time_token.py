#!/usr/bin/env python3
"""One-time token management for contact exchange.

Tokens prevent contact reuse: each publish generates a fresh token.
Only the first peer to present the token can complete registration.
"""

import fcntl
import json
import os
import secrets
import sys
import datetime
from contextlib import contextmanager

sys.path.insert(0, os.path.dirname(__file__))

TOKEN_DIR = os.path.expanduser("~/.openclaw/workspace/skills/agent-comm/contacts")
ISSUED_FILE = os.path.join(TOKEN_DIR, "issued_tokens.json")
_LOCK_FILE = ISSUED_FILE + ".lock"


@contextmanager
def _token_lock():
    """Acquire an exclusive file lock for atomic read-modify-write on issued_tokens.json.

    Using fcntl.LOCK_EX ensures that concurrent processes (e.g. two peers
    registering simultaneously) cannot corrupt the token registry.
    """
    os.makedirs(TOKEN_DIR, exist_ok=True)
    with open(_LOCK_FILE, "a") as lf:
        fcntl.flock(lf, fcntl.LOCK_EX)
        try:
            yield
        finally:
            fcntl.flock(lf, fcntl.LOCK_UN)


def _load_issued() -> dict:
    """Load issued tokens registry (call only while holding _token_lock)."""
    if not os.path.exists(ISSUED_FILE):
        return {"tokens": {}}
    with open(ISSUED_FILE) as f:
        return json.load(f)


def _save_issued(data: dict) -> None:
    """Save issued tokens registry (call only while holding _token_lock)."""
    os.makedirs(TOKEN_DIR, exist_ok=True)
    with open(ISSUED_FILE, "w") as f:
        json.dump(data, f, indent=2)
    os.chmod(ISSUED_FILE, 0o600)


def generate_token(ttl_seconds: int = 3600) -> dict:
    """Create a new one-time token, adding it to the issued registry."""
    with _token_lock():
        data = _load_issued()

        token_bytes = secrets.token_bytes(32)
        token_hex = token_bytes.hex()

        data["tokens"][token_hex] = {
            "createdAt": datetime.datetime.now(datetime.timezone.utc).isoformat(),
            "ttlSeconds": ttl_seconds,
            "used": False,
            "usedBy": None,
        }

        _save_issued(data)
        return {"token": token_hex, **data["tokens"][token_hex]}


def get_current_token() -> dict | None:
    """Load the most recently issued unused token, or None."""
    with _token_lock():
        data = _load_issued()
        unused = [t for t, v in data["tokens"].items() if not v.get("used")]
        if not unused:
            return None
        unused_entries = [(t, data["tokens"][t]) for t in unused]
        unused_entries.sort(key=lambda x: x[1]["createdAt"], reverse=True)
        token, info = unused_entries[0]
        return {"token": token, **info}


def add_peer_token(token_hex: str) -> bool:
    """
    Pre-register a peer's token so it can be consumed on first connection.
    This stores the token as pending in our issued registry.
    Returns True if added, False if already exists.
    """
    with _token_lock():
        data = _load_issued()
        if token_hex in data["tokens"]:
            return False  # already registered
        data["tokens"][token_hex] = {
            "createdAt": datetime.datetime.now(datetime.timezone.utc).isoformat(),
            "ttlSeconds": 3600,
            "used": False,
            "usedBy": "peer",
        }
        _save_issued(data)
        return True


def consume_token(token_hex: str) -> bool:
    """
    Attempt to consume a token that was previously issued by this agent
    OR pre-registered from a peer's contact.
    Returns True if successfully consumed (first and only caller).
    Returns False if token unknown, expired, or already used.
    """
    with _token_lock():
        data = _load_issued()

        if token_hex not in data["tokens"]:
            return False

        entry = data["tokens"][token_hex]

        if entry.get("used"):
            return False

        # Check expiry (handle both naive and timezone-aware stored values)
        created = datetime.datetime.fromisoformat(entry["createdAt"])
        if created.tzinfo is None:
            now = datetime.datetime.now()
        else:
            now = datetime.datetime.now(datetime.timezone.utc)
        age = (now - created).total_seconds()
        if age > entry.get("ttlSeconds", 3600):
            return False

        entry["used"] = True
        entry["usedAt"] = datetime.datetime.now(datetime.timezone.utc).isoformat()

        _save_issued(data)
        return True


def revoke_token(token_hex: str | None = None) -> bool:
    """Revoke a specific token, or the most recent unused one if None."""
    with _token_lock():
        data = _load_issued()
        if token_hex is None:
            unused = [t for t, v in data["tokens"].items() if not v.get("used")]
            if not unused:
                return False
            unused.sort(key=lambda t: data["tokens"][t]["createdAt"], reverse=True)
            token_hex = unused[0]
        if token_hex in data["tokens"]:
            del data["tokens"][token_hex]
            _save_issued(data)
            return True
        return False


if __name__ == "__main__":
    import argparse
    parser = argparse.ArgumentParser(description="Token management.")
    sub = parser.add_subparsers(dest="cmd")
    sub.add_parser("generate", help="Generate a new one-time token")
    sub.add_parser("revoke", help="Revoke the most recent unused token")
    sub.add_parser("list", help="List all issued tokens and their status")
    args = parser.parse_args()

    if args.cmd == "generate":
        entry = generate_token()
        print(f"Token: {entry['token']}")
        print(f"Expires: {entry['ttlSeconds']}s")
    elif args.cmd == "revoke":
        ok = revoke_token()
        print("Revoked" if ok else "No active token to revoke")
    elif args.cmd == "list":
        data = _load_issued()
        if not data["tokens"]:
            print("No tokens issued.")
        for tok, info in data["tokens"].items():
            status = "USED" if info.get("used") else "ACTIVE"
            print(f"  {status}  {tok[:16]}...  (created: {info['createdAt']})")
    else:
        parser.print_help()
        sys.exit(1)