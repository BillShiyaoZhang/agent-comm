#!/usr/bin/env python3
"""
Flask HTTP server for receiving agent-comm encrypted messages.

Listens on localhost:18792, exposed via Cloudflare Tunnel (HTTPS).
Receives ECIES-encrypted agent-comm messages and queues them for retrieval.

Endpoints:
  GET  /agent-comm/health     — Liveness probe
  GET  /agent-comm/identity   — Public info for contact exchange
  POST /agent-comm/messages   — Submit an encrypted message
  GET  /agent-comm/messages   — Poll for new messages
  GET  /agent-comm/messages/<id> — Fetch a single message
"""

import glob
import json
import os
import secrets
import sys
import uuid
from datetime import datetime, timezone
from functools import wraps

AGENT_COMM_DIR = os.path.expanduser("~/.openclaw/workspace/skills/agent-comm")
CONTACTS_DIR = os.path.join(AGENT_COMM_DIR, "contacts")
QUEUE_DIR = os.path.join(CONTACTS_DIR, "message_queue")
AUTH_TOKEN_FILE = os.path.join(CONTACTS_DIR, "auth_token.json")
LISTEN_PORT = 18792
LISTEN_HOST = "127.0.0.1"


# ───────────────────────────────────────────────────────────────
# Auth token
# ───────────────────────────────────────────────────────────────

def get_or_create_auth_token() -> str:
    """Load existing auth token, or generate a new 256-bit one."""
    os.makedirs(CONTACTS_DIR, exist_ok=True)
    if os.path.exists(AUTH_TOKEN_FILE):
        with open(AUTH_TOKEN_FILE) as f:
            return json.load(f).get("token", "")
    token = secrets.token_hex(32)
    with open(AUTH_TOKEN_FILE, "w") as f:
        json.dump({"token": token, "createdAt": datetime.now(timezone.utc).isoformat()}, f)
    os.chmod(AUTH_TOKEN_FILE, 0o600)
    return token


# ───────────────────────────────────────────────────────────────
# Message queue (file-based, one JSON file per message)
# ───────────────────────────────────────────────────────────────

def enqueue_message(encrypted_msg: dict) -> str:
    """Write encrypted message to queue, return message id."""
    os.makedirs(QUEUE_DIR, exist_ok=True)
    msg_id = str(uuid.uuid4())
    entry = {
        "id": msg_id,
        "receivedAt": datetime.now(timezone.utc).isoformat(),
        "read": False,
        "encrypted": encrypted_msg,
    }
    path = os.path.join(QUEUE_DIR, f"{msg_id}.json")
    with open(path, "w") as f:
        json.dump(entry, f, indent=2)
    return msg_id


def list_messages(include_read: bool = False) -> list[dict]:
    """List queued messages, optionally filtered by read status."""
    os.makedirs(QUEUE_DIR, exist_ok=True)
    messages = []
    for path in sorted(glob.glob(os.path.join(QUEUE_DIR, "*.json"))):
        with open(path) as f:
            messages.append(json.load(f))
    if not include_read:
        messages = [m for m in messages if not m.get("read")]
    return messages


def get_message(msg_id: str) -> dict | None:
    """Fetch a single message by id, mark as read."""
    path = os.path.join(QUEUE_DIR, f"{msg_id}.json")
    if not os.path.exists(path):
        return None
    with open(path) as f:
        msg = json.load(f)
    if not msg.get("read"):
        msg["read"] = True
        with open(path, "w") as f:
            json.dump(msg, f, indent=2)
    return msg


def mark_message_read(msg_id: str) -> bool:
    path = os.path.join(QUEUE_DIR, f"{msg_id}.json")
    if not os.path.exists(path):
        return False
    with open(path) as f:
        msg = json.load(f)
    msg["read"] = True
    with open(path, "w") as f:
        json.dump(msg, f, indent=2)
    return True


# ───────────────────────────────────────────────────────────────
# Flask app
# ───────────────────────────────────────────────────────────────

def create_app():
    try:
        from flask import Flask, request, jsonify
    except ImportError:
        print("ERROR: flask not installed. Run: uv pip install --python "
              "~/.openclaw/venvs/kg/bin/python3 flask", file=sys.stderr)
        sys.exit(1)

    sys.path.insert(0, os.path.join(AGENT_COMM_DIR, "scripts"))
    import identity
    import crypto

    app = Flask(__name__)

    def require_auth(f):
        """Decorator: require valid Bearer token."""
        @wraps(f)
        def decorated(*args, **kwargs):
            auth = request.headers.get("Authorization", "")
            if auth.startswith("Bearer "):
                token = auth[7:]
            else:
                token = ""
            if token != get_or_create_auth_token():
                return jsonify({"error": "unauthorized"}), 401
            return f(*args, **kwargs)
        return decorated

    def get_peer_x25519_pub(fingerprint: str) -> bytes | None:
        """Look up a peer's X25519 public key from contacts by fingerprint."""
        for cf in glob.glob(os.path.join(CONTACTS_DIR, "peer-*.json")):
            try:
                with open(cf) as f:
                    contact = json.load(f)
                if contact.get("_fingerprint") == fingerprint:
                    x25519_hex = contact.get("x25519PublicKey")
                    if x25519_hex:
                        return identity.decode_hex(x25519_hex)
            except Exception:
                continue
        return None

    # ── GET /agent-comm/health ────────────────────────────────
    @app.route("/agent-comm/health", methods=["GET"])
    def health():
        return jsonify({
            "status": "ok",
            "at": datetime.now(timezone.utc).isoformat(),
        })

    # ── GET /agent-comm/identity ──────────────────────────────
    # Returns public info for contact exchange (out-of-band sharing, not via HTTP)
    @app.route("/agent-comm/identity", methods=["GET"])
    def identity_endpoint():
        ed_priv, ed_pub, x_priv, x_pub = identity.get_or_create_keypair()
        fp = identity.compute_fingerprint(ed_pub)
        return jsonify({
            "fingerprint": fp,
            "x25519PublicKey": identity.encode_hex(x_pub),
            "authToken": get_or_create_auth_token(),
        })

    # ── POST /agent-comm/messages ─────────────────────────────
    # Submit an ECIES-encrypted message. No sender auth needed
    # (the ciphertext itself proves sender identity).
    @app.route("/agent-comm/messages", methods=["POST"])
    def receive_message():
        try:
            encrypted_msg = request.get_json()
        except Exception:
            return jsonify({"error": "invalid JSON"}), 400

        if not isinstance(encrypted_msg, dict):
            return jsonify({"error": "body must be JSON object"}), 400
        if "ciphertext" not in encrypted_msg:
            return jsonify({"error": "missing ciphertext"}), 400

        msg_id = enqueue_message(encrypted_msg)
        return jsonify({"status": "queued", "id": msg_id}), 202

    # ── GET /agent-comm/messages ──────────────────────────────
    # Poll for messages. Returns messages optionally decrypted.
    # Query params:
    #   all=1        — include already-read messages
    #   mark_read=1  — mark returned messages as read
    @app.route("/agent-comm/messages", methods=["GET"])
    @require_auth
    def list_messages_handler():
        include_read = request.args.get("all", "0") == "1"
        mark_read = request.args.get("mark_read", "0") == "1"

        messages = list_messages(include_read=include_read)
        _, _, x_priv, _ = identity.get_or_create_keypair()

        results = []
        for msg in messages:
            enc = msg.get("encrypted", {})
            sender_fp = enc.get("from", "")
            peer_x25519_pem = get_peer_x25519_pub(sender_fp)

            entry = {
                "id": msg["id"],
                "receivedAt": msg["receivedAt"],
                "read": msg.get("read"),
                "from": sender_fp,
            }

            if peer_x25519_pem:
                # x_priv here is our own X25519 private key
                dec = crypto.decrypt_message(enc, x_priv, sender_fp)
                if dec is not None:
                    entry["decrypted"] = dec.decode("utf-8", errors="replace")
                else:
                    entry["decrypted"] = None
                    entry["decrypt_error"] = "AAD/tamper mismatch or wrong recipient key"
            else:
                entry["decrypted"] = None
                entry["decrypt_error"] = "sender not in contacts"

            if mark_read:
                mark_message_read(msg["id"])

            results.append(entry)

        return jsonify({"count": len(results), "messages": results})

    # ── GET /agent-comm/messages/<id> ────────────────────────
    @app.route("/agent-comm/messages/<msg_id>", methods=["GET"])
    @require_auth
    def get_message_handler(msg_id: str):
        msg = get_message(msg_id)
        if msg is None:
            return jsonify({"error": "not found"}), 404

        enc = msg.get("encrypted", {})
        sender_fp = enc.get("from", "")
        _, _, x_priv, _ = identity.get_or_create_keypair()

        entry = {
            "id": msg["id"],
            "receivedAt": msg["receivedAt"],
            "read": msg.get("read"),
            "from": sender_fp,
        }

        dec = crypto.decrypt_message(enc, x_priv, sender_fp)
        if dec is not None:
            entry["decrypted"] = dec.decode("utf-8", errors="replace")
        else:
            entry["decrypted"] = None

        return jsonify(entry)

    return app


if __name__ == "__main__":
    app = create_app()
    print(f"agent-comm server → {LISTEN_HOST}:{LISTEN_PORT}")
    print(f"Queue: {QUEUE_DIR}")
    token = get_or_create_auth_token()
    print(f"Auth token (first 8 chars): {token[:8]}...")
    print("NOTE: expose via cloudflared tunnel for HTTPS access")
    app.run(host=LISTEN_HOST, port=LISTEN_PORT, threaded=True)