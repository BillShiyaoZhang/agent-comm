#!/usr/bin/env python3
"""Publish this agent's contact info with a one-time token."""

import hashlib
import json
import os
import sys
import datetime
from datetime import timezone

sys.path.insert(0, os.path.dirname(__file__))

from paths import CONTACTS_DIR, SHARE_OUTPUT_DIR
from get_tunnel_url import get_tunnel_url
import identity
import one_time_token as token_lib


def get_agent_id() -> str:
    """Read agent ID from openclaw config."""
    try:
        config_path = os.path.expanduser("~/.openclaw/openclaw.json")
        with open(config_path) as f:
            config = json.load(f)
        return config.get("agents", {}).get("defaultAgentId", "main")
    except Exception:
        return "main"


def publish_contact(output_path: str | None = None, silent: bool = False) -> dict:
    tunnel_url = get_tunnel_url()
    if not tunnel_url:
        print("ERROR: Cloudflare Tunnel not running. Run start-claw.sh first.",
              file=sys.stderr)
        sys.exit(1)

    agent_id = get_agent_id()

    # Load or generate both keypairs
    ed_priv, ed_pub, x_priv, x_pub = identity.get_or_create_keypair()

    # Compute fingerprint from Ed25519 public key
    fingerprint = identity.compute_fingerprint(ed_pub)

    # Generate a fresh one-time token (invalidates any previous unused token)
    token_entry = token_lib.generate_token(ttl_seconds=3600)

    # Build contact payload — includes both public keys + token in signed fields
    contact = {
        "gatewayUrl": tunnel_url,
        "agentId": agent_id,
        "publicKey": identity.encode_hex(ed_pub),
        "x25519PublicKey": identity.encode_hex(x_pub),
        "fingerprint": fingerprint,
        "publishedAt": datetime.datetime.now(timezone.utc).isoformat(),
        "token": token_entry["token"],
        "sessionHint": "direct",
    }

    # Sign all identifying fields to prove identity
    sign_payload = {k: contact[k] for k in (
        "gatewayUrl", "agentId", "publicKey", "x25519PublicKey",
        "fingerprint", "publishedAt", "token", "sessionHint"
    )}
    sign_data = json.dumps(sign_payload, sort_keys=True, separators=(",", ":")).encode()
    signature = identity.sign_data(ed_priv, sign_data)
    contact["signature"] = identity.encode_hex(signature)

    if output_path:
        os.makedirs(os.path.dirname(output_path), exist_ok=True)
        with open(output_path, "w") as f:
            json.dump(contact, f, indent=2)
        if not silent:
            print(f"Contact written to: {output_path}")
    elif not silent:
        print(json.dumps(contact, indent=2))
        print(f"Token: {token_entry['token']}")
        print(f"Fingerprint: {fingerprint}")

    return contact


def generate_share_package(output_dir: str | None = None, peer_id: str = "peer") -> str:
    """Generate a share package: contact JSON + instructions file."""
    if output_dir is None:
        output_dir = SHARE_OUTPUT_DIR
    os.makedirs(output_dir, exist_ok=True)
    contact_file = os.path.join(output_dir, f"contact-{peer_id}.json")

    contact = publish_contact(contact_file, silent=True)

    instructions = f"""# 添加我的 agent 为好友

我使用的是 agent-comm skill 来实现安全通信的。请确保你也安装了这个 skill：
https://github.com/BillShiyaoZhang/agent-comm

你可以根据 SKILL.md 的说明初始化并向我分享你的 contact JSON 文件。
"""
    print(instructions)
    return output_dir


if __name__ == "__main__":
    import argparse
    parser = argparse.ArgumentParser(description="Publish this agent's contact info.")
    parser.add_argument("--output", help="Output JSON file path (default: print to stdout)")
    parser.add_argument("--share", nargs="?", metavar="DIR", const=None, help="Generate a share package. Defaults to share-output/ inside the skill dir if DIR is omitted.")
    args = parser.parse_args()

    if args.share is None:
        generate_share_package(None)
    else:
        generate_share_package(args.share)