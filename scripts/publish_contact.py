#!/usr/bin/env python3
"""Publish this agent's contact info with a one-time token."""

import hashlib
import json
import os
import sys
import datetime
from datetime import timezone

sys.path.insert(0, os.path.dirname(__file__))

from get_tunnel_url import get_tunnel_url
import identity
import one_time_token as token_lib

CONTACTS_DIR = os.path.expanduser("~/.openclaw/workspace/skills/agent-comm/contacts")


def get_agent_id() -> str:
    """Read agent ID from openclaw config."""
    import subprocess
    result = subprocess.run(
        ["node", "-e",
         "const c=require(process.env.HOME+'/.openclaw/openclaw.json'); "
         "console.log(c.agents?.defaultAgentId || 'main')"],
        capture_output=True, text=True
    )
    return result.stdout.strip() or "main"


def publish_contact(output_path: str | None = None) -> dict:
    tunnel_url = get_tunnel_url()
    if not tunnel_url:
        print("ERROR: Cloudflare Tunnel not running. Run ~/.openclaw/start-claw.sh first.",
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
        "publicKey": identity.encode_hex(ed_pub),      # Ed25519 — for signatures
        "x25519PublicKey": identity.encode_hex(x_pub),  # X25519 — for ECIES key exchange
        "fingerprint": fingerprint,                     # stable identity (SHA-256 of Ed25519 pub)
        "publishedAt": datetime.datetime.now(timezone.utc).isoformat(),
        "token": token_entry["token"],                 # one-time token
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
        print(f"Contact written to: {output_path}")
        print(f"Token: {token_entry['token']}")
        print(f"Fingerprint: {fingerprint}")
    else:
        print(json.dumps(contact, indent=2))
        print(f"Token: {token_entry['token']}")
        print(f"Fingerprint: {fingerprint}")

    return contact


if __name__ == "__main__":
    import argparse
    parser = argparse.ArgumentParser(description="Publish this agent's contact info.")
    parser.add_argument("--output", help="Output JSON file path (default: print to stdout)")
    args = parser.parse_args()
    publish_contact(args.output)