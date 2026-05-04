#!/usr/bin/env python3
"""One-time token management for contact exchange.

Tokens prevent contact reuse: each publish generates a fresh token.
Only the first peer to present the token can complete registration.
"""

import json
import os
import secrets
import sys
import datetime

sys.path.insert(0, os.path.dirname(__file__))

TOKEN_FILE = os.path.expanduser("~/.openclaw/workspace/skills/agent-comm/contacts/pending_token.json")


def generate_token(ttl_seconds: int = 3600) -> dict:
    """Create a new one-time token, invalidating any previous one."""
    os.makedirs(os.path.dirname(TOKEN_FILE), exist_ok=True)

    token_bytes = secrets.token_bytes(32)
    token_hex = token_bytes.hex()

    entry = {
        "token": token_hex,
        "createdAt": datetime.datetime.now().isoformat(),
        "ttlSeconds": ttl_seconds,
        "used": False,
        "usedBy": None,
    }

    with open(TOKEN_FILE, "w") as f:
        json.dump(entry, f, indent=2)
    os.chmod(TOKEN_FILE, 0o600)

    return entry


def get_current_token() -> dict | None:
    """Load the current pending token, or None if none exists."""
    if not os.path.exists(TOKEN_FILE):
        return None
    with open(TOKEN_FILE) as f:
        return json.load(f)


def consume_token(token_hex: str) -> bool:
    """
    Attempt to consume a token.
    Returns True if successfully consumed (first and only caller).
    Returns False if token missing, expired, or already used.
    """
    if not os.path.exists(TOKEN_FILE):
        return False

    with open(TOKEN_FILE) as f:
        entry = json.load(f)

    if entry.get("used"):
        return False

    # Check expiry
    created = datetime.datetime.fromisoformat(entry["createdAt"])
    age = (datetime.datetime.now() - created).total_seconds()
    if age > entry.get("ttlSeconds", 3600):
        return False

    if entry["token"] != token_hex:
        return False

    entry["used"] = True
    entry["usedAt"] = datetime.datetime.now().isoformat()

    with open(TOKEN_FILE, "w") as f:
        json.dump(entry, f, indent=2)

    return True


def revoke_token() -> bool:
    """Manually revoke the current pending token (e.g., suspect abuse)."""
    if not os.path.exists(TOKEN_FILE):
        return False
    os.remove(TOKEN_FILE)
    return True


if __name__ == "__main__":
    print("Token management. Use as a module.")
    sys.exit(1)