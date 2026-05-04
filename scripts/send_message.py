#!/usr/bin/env python3
"""Send an encrypted message to a peer agent."""

import json
import os
import sys
import argparse

sys.path.insert(0, os.path.dirname(__file__))
import identity
import crypto

CONTACTS_DIR = os.path.expanduser("~/.openclaw/workspace/skills/agent-comm/contacts")


def resolve_peer(peer_id: str) -> dict | None:
    """Load a peer's contact from contacts directory."""
    peer_file = os.path.join(CONTACTS_DIR, f"peer-{peer_id}.json")
    if not os.path.exists(peer_file):
        print(f"ERROR: Peer '{peer_id}' not found. Run register_peer.py first.", file=sys.stderr)
        return None
    with open(peer_file) as f:
        return json.load(f)


def resolve_session_key(peer_id: str) -> str | None:
    """Resolve a peer's session key for sessions_send."""
    peer = resolve_peer(peer_id)
    if not peer:
        return None
    gateway_url = peer.get("gatewayUrl")
    agent_id = peer.get("agentId", "main")
    if not gateway_url:
        print("ERROR: Peer contact has no gatewayUrl", file=sys.stderr)
        return None
    return f"{gateway_url}/{agent_id}"


def encrypt_for_peer(peer_id: str, plaintext: str) -> dict | None:
    """Encrypt a plaintext message for a peer using their X25519 public key."""
    peer = resolve_peer(peer_id)
    if not peer:
        return None

    x25519_pub_hex = peer.get("x25519PublicKey")
    if not x25519_pub_hex:
        print("ERROR: Peer has no x25519PublicKey — cannot encrypt. "
              "Peer may need to re-register with updated contact.", file=sys.stderr)
        return None

    x25519_pub = identity.decode_hex(x25519_pub_hex)
    # identity.encode_hex already gives hex; decode_hex returns bytes from PEM-like hex
    # But we stored it as PEM... let me check what format x25519PublicKey is stored in
    # Actually in publish_contact.py: identity.encode_hex(x_pub) where x_pub is PEM bytes
    # decode_hex reverses that. But if x_pub is PEM, encode_hex(PEM) gives hex of the PEM bytes.
    # We stored the raw PEM bytes via identity.encode_hex. Let's just check.
    try:
        # Try as raw bytes (already decoded by identity.decode_hex)
        x25519_pub_bytes = x25519_pub
    except Exception:
        print("ERROR: Could not decode peer's x25519PublicKey", file=sys.stderr)
        return None

    ed_priv, ed_pub, _, _ = identity.get_or_create_keypair()
    fp = identity.compute_fingerprint(ed_pub)

    return crypto.encrypt_message(plaintext.encode("utf-8"), x25519_pub_bytes, fp)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description="Encrypt a message for a peer.")
    parser.add_argument("--peer-id", required=True, help="Peer ID to send to")
    parser.add_argument("--encrypt", metavar="TEXT", help="Plaintext message to encrypt and print")
    parser.add_argument("--session-key", action="store_true", help="Print peer's session key instead")
    args = parser.parse_args()

    if args.session_key:
        key = resolve_session_key(args.peer_id)
        if key:
            print(key)
    elif args.encrypt:
        enc = encrypt_for_peer(args.peer_id, args.encrypt)
        if enc:
            print(json.dumps(enc, indent=2))
    else:
        print("ERROR: Provide --encrypt 'text' or --session-key", file=sys.stderr)
        sys.exit(1)