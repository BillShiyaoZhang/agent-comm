#!/usr/bin/env python3
"""Poll the server for new messages, auto-decrypt, print to stdout."""

import argparse
import json
import os
import sys

sys.path.insert(0, os.path.dirname(__file__))

CONTACTS_DIR = os.path.expanduser("~/.openclaw/workspace/skills/agent-comm/contacts")
AUTH_TOKEN_FILE = os.path.join(CONTACTS_DIR, "auth_token.json")
LISTEN_HOST = "127.0.0.1"
LISTEN_PORT = 18792


def get_auth_token() -> str:
    """Load the HTTP server's Bearer token."""
    if os.path.exists(AUTH_TOKEN_FILE):
        with open(AUTH_TOKEN_FILE) as f:
            return json.load(f).get("token", "")
    print(f"ERROR: {AUTH_TOKEN_FILE} not found. Is server running?", file=sys.stderr)
    sys.exit(1)


def get_server_url() -> str:
    """Get the local server base URL."""
    return f"http://{LISTEN_HOST}:{LISTEN_PORT}"


def main():
    parser = argparse.ArgumentParser(description="Poll server for new messages.")
    parser.add_argument("--auth-token", help="Bearer token (reads from auth_token.json if omitted)")
    parser.add_argument("--mark-read", action="store_true", help="Mark messages as read after fetching")
    parser.add_argument("--all", action="store_true", help="Include already-read messages")
    parser.add_argument("--server-url", default=get_server_url(), help="Base URL of server")
    parser.add_argument("--poll-interval", type=int, default=0, help="Poll every N seconds (0 = once)")
    parser.add_argument("--json", action="store_true", help="Output raw JSON (no decryption)")
    args = parser.parse_args()

    auth_token = args.auth_token or get_auth_token()
    headers = {"Authorization": f"Bearer {auth_token}"}
    params = {}
    if args.mark_read:
        params["mark_read"] = "1"
    if args.all:
        params["all"] = "1"

    import urllib.request
    import urllib.parse

    query = urllib.parse.urlencode(params) if params else ""
    url = f"{args.server_url}/agent-comm/messages"
    if query:
        url = f"{url}?{query}"

    try:
        req = urllib.request.Request(url, headers=headers)
        with urllib.request.urlopen(req, timeout=10) as resp:
            data = json.loads(resp.read())
    except Exception as e:
        print(f"ERROR: {e}", file=sys.stderr)
        sys.exit(1)

    messages = data.get("messages", [])
    if not messages:
        print("No messages.")
        sys.exit(0)

    for msg in messages:
        sender = msg.get("from", "unknown")
        received = msg.get("receivedAt", "")
        decrypted = msg.get("decrypted")
        error = msg.get("decrypt_error")
        msg_id = msg.get("id", "")

        if args.json:
            print(json.dumps(msg))
            continue

        print(f"[{received}] from={sender}")
        if decrypted is not None:
            print(f"  {decrypted}")
        elif error:
            print(f"  (decrypt error: {error})")
        else:
            print(f"  (no decrypted field)")

    if args.mark_read:
        print(f"Marked {len(messages)} message(s) as read.")


if __name__ == "__main__":
    main()
