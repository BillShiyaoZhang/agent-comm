#!/usr/bin/env python3
"""Register a peer agent's contact info from a JSON file, verifying its signature."""

import hashlib
import json
import os
import sys
import datetime
import urllib.request
import urllib.error

sys.path.insert(0, os.path.dirname(__file__))
import identity
import one_time_token as token_lib

CONTACTS_DIR = os.path.expanduser("~/.openclaw/workspace/skills/agent-comm/contacts")

# Fields that were signed — MUST match publish_contact.py's sign_payload keys exactly.
_SIGNED_FIELDS = (
    "gatewayUrl", "agentId", "publicKey", "x25519PublicKey",
    "fingerprint", "publishedAt", "token", "sessionHint"
)


def verify_contact(contact: dict) -> bool:
    """Verify Ed25519 signature on the contact.

    The signed payload must include exactly the fields in _SIGNED_FIELDS
    (excluding 'signature' itself), matching what publish_contact.py signed.
    """
    if "signature" not in contact:
        print("WARNING: Contact has no signature — accepting anyway (unverified)", file=sys.stderr)
        return True

    if "publicKey" not in contact:
        print("ERROR: Contact has a signature but no publicKey", file=sys.stderr)
        return False

    try:
        key_bytes = identity.decode_hex(contact["publicKey"])
        pub_bytes = identity.decode_pub_key(key_bytes)
        signature = identity.decode_hex(contact["signature"])
        # Build payload from exactly the signed fields (signature is never part of the payload)
        payload = {k: contact[k] for k in _SIGNED_FIELDS if k in contact}
        sign_data = json.dumps(payload, sort_keys=True, separators=(",", ":")).encode()
        return identity.verify_signature(pub_bytes, sign_data, signature)
    except Exception as e:
        print(f"ERROR: Signature verification failed: {e}", file=sys.stderr)
        return False


def consume_peer_token_remote(contact: dict) -> bool:
    """Call the peer's server to consume their one-time token.

    This is the critical step that makes tokens truly single-use: without calling
    the peer's /consume-token endpoint, the same contact JSON could be registered
    by multiple parties. Returns True on success, False with a warning on failure.
    """
    gateway_url = contact.get("gatewayUrl", "").rstrip("/")
    token = contact.get("token")
    if not gateway_url or not token:
        print("WARNING: Cannot consume token remotely — missing gatewayUrl or token",
              file=sys.stderr)
        return False

    url = f"{gateway_url}/agent-comm/consume-token"
    data = json.dumps({"token": token}).encode("utf-8")
    req = urllib.request.Request(
        url, data=data,
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    try:
        with urllib.request.urlopen(req, timeout=10) as resp:
            result = json.loads(resp.read())
            if result.get("consumed"):
                print("Peer's one-time token consumed successfully (token is now invalid).")
                return True
            print(f"WARNING: Peer returned unexpected response: {result}", file=sys.stderr)
            return False
    except urllib.error.HTTPError as e:
        body = e.read().decode(errors="replace")
        print(f"WARNING: Token consumption failed (HTTP {e.code}): {body}", file=sys.stderr)
        return False
    except Exception as e:
        print(f"WARNING: Could not reach peer server to consume token: {e}", file=sys.stderr)
        return False


def register_peer(
    peer_id: str,
    contact_data: dict,
    verify: bool = True,
    consume_remote: bool = True,
) -> str:
    """Verify and store a peer contact.

    Args:
        peer_id: Short local name for this peer (e.g. "alice").
        contact_data: Parsed contact JSON from the peer.
        verify: If True, reject contacts with invalid Ed25519 signatures.
        consume_remote: If True, call the peer's server to consume their
            one-time token, making it impossible to re-register the same contact.
    """
    if verify and not verify_contact(contact_data):
        print("ERROR: Signature verification failed. Refusing to register.", file=sys.stderr)
        sys.exit(1)

    if "token" not in contact_data:
        print("ERROR: Contact has no token field — cannot complete one-time exchange.", file=sys.stderr)
        sys.exit(1)

    # Consume the token on the PEER's server — this is what makes it truly one-time.
    # Without this step any party who obtains the contact JSON could register it.
    if consume_remote:
        if not consume_peer_token_remote(contact_data):
            print("WARNING: Remote token consumption failed. "
                  "The same contact JSON may be registerable by others.",
                  file=sys.stderr)

    os.makedirs(CONTACTS_DIR, exist_ok=True)
    peer_file = os.path.join(CONTACTS_DIR, f"peer-{peer_id}.json")

    pub_bytes = identity.decode_pub_key(identity.decode_hex(contact_data["publicKey"]))
    fingerprint = hashlib.sha256(pub_bytes).hexdigest()[:16]

    meta = {
        "_registered_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
        "_fingerprint": fingerprint,
    }

    with open(peer_file, "w") as f:
        json.dump({**meta, **contact_data}, f, indent=2)

    print(f"Registered peer '{peer_id}' (fingerprint: {fingerprint})")
    return peer_file


def complete_peer_registration(peer_id: str) -> bool:
    """
    Consume the deferred token for a peer after first successful connection.
    Returns True if successfully consumed.
    """
    peer_file = os.path.join(CONTACTS_DIR, f"peer-{peer_id}.json")
    if not os.path.exists(peer_file):
        print(f"ERROR: Peer '{peer_id}' not registered.", file=sys.stderr)
        return False

    with open(peer_file) as f:
        peer_data = json.load(f)

    peer_token = peer_data.get("_peer_token")
    if not peer_token:
        print(f"ERROR: No token found for peer '{peer_id}'.", file=sys.stderr)
        return False

    if not token_lib.consume_token(peer_token):
        print(f"ERROR: Token already used, expired, or revoked for peer '{peer_id}'.", file=sys.stderr)
        return False

    # Update the peer file to mark token as consumed
    peer_data["_token_consumed_at"] = datetime.datetime.now().isoformat()
    with open(peer_file, "w") as f:
        json.dump(peer_data, f, indent=2)

    print(f"Completed registration for '{peer_id}' — token consumed.")
    return True


if __name__ == "__main__":
    import argparse
    parser = argparse.ArgumentParser(description="Register a peer agent contact.")
    parser.add_argument("--contact-file", help="Path to peer contact JSON file")
    parser.add_argument("--peer-id", required=True, help="Short ID for this peer (e.g. alice)")
    parser.add_argument("--no-verify", action="store_true", help="Skip signature verification")
    parser.add_argument("--contact-json", help="Raw JSON string (alternative to --contact-file)")
    parser.add_argument(
        "--no-consume-remote", action="store_true",
        help="Skip consuming the token on the peer's server (use for offline/testing only)",
    )
    args = parser.parse_args()

    if args.contact_file:
        with open(args.contact_file) as f:
            contact = json.load(f)
    elif args.contact_json:
        contact = json.loads(args.contact_json)
    else:
        print("ERROR: Provide either --contact-file or --contact-json", file=sys.stderr)
        sys.exit(1)

    register_peer(
        args.peer_id, contact,
        verify=not args.no_verify,
        consume_remote=not args.no_consume_remote,
    )