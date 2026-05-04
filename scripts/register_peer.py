#!/usr/bin/env python3
"""Register a peer agent's contact info from a JSON file, verifying its signature."""

import hashlib
import json
import os
import sys
import datetime

sys.path.insert(0, os.path.dirname(__file__))
import identity
import one_time_token as token_lib

CONTACTS_DIR = os.path.expanduser("~/.openclaw/workspace/skills/agent-comm/contacts")

# Fields that were signed (must match publish_contact.py exactly)
_SIGNED_FIELDS = ("gatewayUrl", "agentId", "publicKey", "publishedAt", "token", "sessionHint", "signature")


def verify_contact(contact: dict) -> bool:
    """Verify Ed25519 signature on the contact."""
    if "signature" not in contact:
        print("WARNING: Contact has no signature — accepting anyway (unverified)", file=sys.stderr)
        return True

    if "publicKey" not in contact:
        print("ERROR: Contact has a signature but no publicKey", file=sys.stderr)
        return False

    try:
        public_key = identity.decode_hex(contact["publicKey"])
        signature = identity.decode_hex(contact["signature"])
        payload = {k: contact[k] for k in _SIGNED_FIELDS if k in contact and k != "signature"}
        sign_data = json.dumps(payload, sort_keys=True, separators=(",", ":")).encode()
        return identity.verify_signature(public_key, sign_data, signature)
    except Exception as e:
        print(f"ERROR: Signature verification failed: {e}", file=sys.stderr)
        return False


def register_peer(peer_id: str, contact_data: dict, verify: bool = True) -> str:
    if verify and not verify_contact(contact_data):
        print("ERROR: Signature verification failed. Refusing to register.", file=sys.stderr)
        sys.exit(1)

    if "token" not in contact_data:
        print("ERROR: Contact has no token field — cannot complete one-time exchange.", file=sys.stderr)
        sys.exit(1)

    # Consume the token — fails if already used or expired
    if not token_lib.consume_token(contact_data["token"]):
        print("ERROR: Token already used, expired, or revoked. Cannot register peer.", file=sys.stderr)
        print("Hint: Request a fresh contact from your peer.", file=sys.stderr)
        sys.exit(1)

    os.makedirs(CONTACTS_DIR, exist_ok=True)
    peer_file = os.path.join(CONTACTS_DIR, f"peer-{peer_id}.json")

    pub_bytes = identity.decode_hex(contact_data["publicKey"])
    fingerprint = hashlib.sha256(pub_bytes).hexdigest()[:16]

    meta = {
        "_registered_at": datetime.datetime.now().isoformat(),
        "_fingerprint": fingerprint,
        "_token_consumed_at": datetime.datetime.now().isoformat(),
    }

    with open(peer_file, "w") as f:
        json.dump({**meta, **contact_data}, f, indent=2)

    print(f"Registered peer '{peer_id}' (fingerprint: {fingerprint})")
    print("Token consumed — this contact cannot be reused.")
    return peer_file


if __name__ == "__main__":
    import argparse
    parser = argparse.ArgumentParser(description="Register a peer agent contact.")
    parser.add_argument("--contact-file", help="Path to peer contact JSON file")
    parser.add_argument("--peer-id", required=True, help="Short ID for this peer (e.g. alice)")
    parser.add_argument("--no-verify", action="store_true", help="Skip signature verification")
    parser.add_argument("--contact-json", help="Raw JSON string (alternative to --contact-file)")
    args = parser.parse_args()

    if args.contact_file:
        with open(args.contact_file) as f:
            contact = json.load(f)
    elif args.contact_json:
        contact = json.loads(args.contact_json)
    else:
        print("ERROR: Provide either --contact-file or --contact-json", file=sys.stderr)
        sys.exit(1)

    register_peer(args.peer_id, contact, verify=not args.no_verify)