#!/usr/bin/env python3
"""
Flask HTTP server for receiving agent-comm encrypted messages.

Listens on localhost:18792, exposed via Cloudflare Tunnel (HTTPS).
Receives ECIES-encrypted agent-comm messages and queues them for retrieval.
"""

import glob
import json
import os
import secrets
import sys
import threading
import uuid
import urllib.request
import urllib.error
from datetime import datetime, timezone
from functools import wraps

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from paths import CONTACTS_DIR, QUEUE_DIR, AUTH_TOKEN_FILE, LISTEN_HOST, LISTEN_PORT

HOOK_TOKEN = os.environ.get("OPENCLAW_HOOK_TOKEN", "22b91b989127132cb84175d38c347c5f")
HOOK_BASE = os.environ.get("OPENCLAW_HOOK_BASE", "http://127.0.0.1:18789")
HOOK_PATH = "/hooks/agent"


def trigger_agent_hook(message_text: str = "") -> bool:
    """Fire POST /hooks/agent to trigger an isolated OpenClaw agent turn.

    Returns True if the hook was triggered successfully, False otherwise.
    Does NOT raise exceptions — logs errors and returns False.
    """
    payload = json.dumps({
        "message": message_text or (
            "有新的 agent-comm 加密消息到达，请读取并处理。\n"
            "详见：~/.openclaw/workspace/skills/agent-comm/prompts/inbound-message-handler.md"
        ),
        "name": "agent-comm-inbound",
        "timeoutSeconds": 300,
    }).encode("utf-8")

    req = urllib.request.Request(
        f"{HOOK_BASE}{HOOK_PATH}",
        data=payload,
        method="POST",
        headers={
            "Authorization": f"Bearer {HOOK_TOKEN}",
            "Content-Type": "application/json",
        },
    )
    try:
        with urllib.request.urlopen(req, timeout=10) as resp:
            body = resp.read().decode("utf-8")
            print(
                f"[hook] /hooks/agent triggered ok status={resp.status} body={body[:200]}",
                flush=True,
            )
            return True
    except urllib.error.HTTPError as e:
        print(
            f"[hook] /hooks/agent HTTP error {e.code}: {e.reason} body={e.read().decode()[:200]}",
            flush=True,
        )
        return False
    except urllib.error.URLError as e:
        print(f"[hook] /hooks/agent URL error: {e.reason}", flush=True)
        return False
    except Exception as e:
        print(f"[hook] /hooks/agent unexpected error: {e}", flush=True)
        return False

LISTEN_HOST_STR = "127.0.0.1"
LISTEN_PORT_INT = 18792

# Protects concurrent writes to the message queue directory within the same process.
_QUEUE_LOCK = threading.Lock()


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
    with _QUEUE_LOCK:
        with open(path, "w") as f:
            json.dump(entry, f, indent=2)
    sender_fp = encrypted_msg.get("from", "unknown")
    ts = entry["receivedAt"]
    print(f"[queue] Message queued id={msg_id} from={sender_fp} at={ts}", flush=True)
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
    with _QUEUE_LOCK:
        with open(path) as f:
            msg = json.load(f)
        msg["read"] = True
        with open(path, "w") as f:
            json.dump(msg, f, indent=2)
    return True


def cleanup_old_messages(max_age_seconds: int = 86400) -> int:
    """Delete read messages older than max_age_seconds."""
    os.makedirs(QUEUE_DIR, exist_ok=True)
    deleted = 0
    for path in glob.glob(os.path.join(QUEUE_DIR, "*.json")):
        try:
            with open(path) as f:
                msg = json.load(f)
            if not msg.get("read"):
                continue
            received_at = msg.get("receivedAt", "")
            if not received_at:
                continue
            ts = datetime.fromisoformat(received_at.replace("Z", "+00:00"))
            age = (datetime.now(timezone.utc) - ts).total_seconds()
            if age > max_age_seconds:
                os.remove(path)
                deleted += 1
        except Exception:
            continue
    return deleted


def create_app():
    try:
        from flask import Flask, request, jsonify
    except ImportError:
        print("ERROR: flask not installed. Run: uv pip install flask waitress", file=sys.stderr)
        sys.exit(1)

    import identity
    import crypto

    app = Flask(__name__)

    def require_auth(f):
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

    @app.route("/agent-comm/health", methods=["GET"])
    def health():
        return jsonify({
            "status": "ok",
            "at": datetime.now(timezone.utc).isoformat(),
        })

    @app.route("/agent-comm/identity", methods=["GET"])
    def identity_endpoint():
        ed_priv, ed_pub, x_priv, x_pub = identity.get_or_create_keypair()
        fp = identity.compute_fingerprint(ed_pub)
        return jsonify({
            "fingerprint": fp,
            "x25519PublicKey": identity.encode_hex(x_pub),
            "gatewayUrl": request.host_url.rstrip("/"),
        })

    @app.route("/agent-comm/consume-token", methods=["POST"])
    def consume_token_endpoint():
        import one_time_token as token_lib
        body = request.get_json(silent=True)
        if not body or "token" not in body:
            return jsonify({"error": "missing token"}), 400
        token = body["token"]
        if token_lib.consume_token(token):
            return jsonify({"consumed": True})
        return jsonify({"error": "token invalid, expired, or already consumed"}), 409

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

        ts_str = encrypted_msg.get("timestamp")
        if ts_str:
            try:
                ts = datetime.fromisoformat(ts_str.replace("Z", "+00:00"))
                now = datetime.now(timezone.utc)
                if ts.tzinfo is None:
                    from datetime import timedelta
                    ts = ts.replace(tzinfo=timezone.utc)
                age = abs((now - ts).total_seconds())
                if age > 300:
                    return jsonify({
                        "error": "message timestamp outside 5-minute window (possible replay)"
                    }), 400
            except Exception:
                pass

        msg_id = enqueue_message(encrypted_msg)

        # ── Trigger OpenClaw isolated agent turn ──────────────────────────
        sender_fp = encrypted_msg.get("from", "unknown")
        hook_msg = (
            f"[agent-comm] 收到来自 {sender_fp} 的加密消息 (id={msg_id})，"
            f"请按 prompts/inbound-message-handler.md 处理。"
        )
        trigger_agent_hook(hook_msg)

        return jsonify({"status": "queued", "id": msg_id}), 202

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
                qpath = os.path.join(QUEUE_DIR, f"{msg['id']}.json")
                try:
                    os.remove(qpath)
                    print(f"[queue] Deleted read message id={msg['id']}", flush=True)
                except Exception:
                    pass

            results.append(entry)

        return jsonify({"count": len(results), "messages": results})

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
    print(f"agent-comm server → {LISTEN_HOST_STR}:{LISTEN_PORT_INT}")
    print(f"Queue: {QUEUE_DIR}")
    token = get_or_create_auth_token()
    print(f"Auth token (first 8 chars): {token[:8]}...")
    print("NOTE: expose via cloudflared tunnel for HTTPS access")

    try:
        from waitress import serve
        print("Using waitress WSGI server (production-ready)")
        serve(app, host=LISTEN_HOST_STR, port=LISTEN_PORT_INT)
    except ImportError:
        print(
            "WARNING: waitress not installed — falling back to Flask dev server. "
            "Install for production: "
            "uv pip install waitress",
            file=sys.stderr,
        )
        app.run(host=LISTEN_HOST_STR, port=LISTEN_PORT_INT, threaded=True)