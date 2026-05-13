#!/usr/bin/env python3
"""
Message encryption and decryption for agent-to-agent communication.

Protocol (ECIES-like, RFC 6090):
- Key exchange: X25519 ECDH (Curve25519)
- Key derivation: HKDF-SHA256 (RFC 5869)
- Encryption: AES-256-GCM-SIV (AEAD, nonce-misuse resistant)

Encrypted message layout (JSON):
{
  "v": 1,                           # protocol version
  "from": "fingerprint",            # sender Ed25519 fingerprint (for routing + AAD)
  "ephemeralPk": "base64",          # X25519 ephemeral public key (32 bytes, raw)
  "nonce": "base64",                # 12-byte nonce for AES-GCM-SIV
  "ciphertext": "base64",           # encrypted payload + auth tag
  "timestamp": "ISO8601"            # for replay protection
}

Encryption flow (sender A → recipient B):
1. A has B's X25519 static public key from B's contact
2. A generates fresh ephemeral X25519 keypair (different for every message)
3. A performs ECDH: ephemeral_private_key ↔ B's static public key → shared secret
4. A derives AES-256 key: HKDF(shared_secret, "agent-comm-v1")
5. A encrypts message: AES-256-GCM-SIV(key, nonce, plaintext, AAD=fingerprint)
6. A sends (ephemeralPk, nonce, ciphertext, from, timestamp) to B

Decryption flow (recipient B):
1. B receives encrypted message
2. B performs ECDH: B's static private key ↔ ephemeralPk → shared secret
3. B derives same AES key via HKDF (ECDH is symmetric)
4. B decrypts: AES-256-GCM-SIV(key, nonce, ciphertext, AAD=from fingerprint)

Result: Perfect forward secrecy — each message uses a fresh ephemeral key.
        Only the intended recipient (owner of the static X25519 private key) can decrypt.
"""

import base64
import hashlib
import hmac
import json
import os
import secrets
import sys
from datetime import datetime, timezone


def _import_crypto():
    from cryptography.hazmat.primitives.asymmetric.x25519 import (
        X25519PrivateKey,
        X25519PublicKey,
    )
    from cryptography.hazmat.primitives.ciphers.aead import AESGCMSIV
    from cryptography.hazmat.primitives import serialization
    return X25519PrivateKey, X25519PublicKey, AESGCMSIV, serialization


def _hkdf(ikm: bytes, info: bytes, length: int = 32) -> bytes:
    """HKDF-Extract + HKDF-Expand (RFC 5869). Derives AES key from X25519 shared secret."""
    prk = hmac.new(b"agent-comm-hkdf-v1", ikm, hashlib.sha256).digest()
    T = b""
    counter = 1
    while len(T) < length:
        T += hmac.new(prk, T + info + bytes([counter]), hashlib.sha256).digest()
        counter += 1
    return T[:length]


def encrypt_message(
    plaintext: bytes,
    recipient_x25519_public_pem: bytes,
    sender_ed25519_fingerprint: str,
) -> dict:
    """Encrypt a message using ECIES-like hybrid encryption."""
    X25519PrivateKey, X25519PublicKey, AESGCMSIV, serialization = _import_crypto()

    ephemeral_sk = X25519PrivateKey.generate()
    ephemeral_pk = ephemeral_sk.public_key()
    recipient_pk = serialization.load_pem_public_key(recipient_x25519_public_pem)
    shared_secret = ephemeral_sk.exchange(recipient_pk)
    aes_key = _hkdf(shared_secret, b"agent-comm-v1")

    nonce = secrets.token_bytes(12)
    cipher = AESGCMSIV(aes_key)
    aad = sender_ed25519_fingerprint.encode("utf-8")
    ciphertext = cipher.encrypt(nonce, plaintext, aad)

    ephemeral_pk_bytes = ephemeral_pk.public_bytes(
        encoding=serialization.Encoding.Raw,
        format=serialization.PublicFormat.Raw,
    )

    return {
        "v": 1,
        "from": sender_ed25519_fingerprint,
        "ephemeralPk": base64.b64encode(ephemeral_pk_bytes).decode("ascii"),
        "nonce": base64.b64encode(nonce).decode("ascii"),
        "ciphertext": base64.b64encode(ciphertext).decode("ascii"),
        "timestamp": datetime.now(timezone.utc).isoformat(),
    }


def decrypt_message(
    encrypted_msg: dict,
    recipient_x25519_private_pem: bytes,
    sender_ed25519_fingerprint: str | None,
) -> bytes | None:
    """Decrypt a message. Returns plaintext bytes, or None on failure."""
    X25519PrivateKey, X25519PublicKey, AESGCMSIV, serialization = _import_crypto()

    ephemeral_pk_bytes = base64.b64decode(encrypted_msg["ephemeralPk"])
    if len(ephemeral_pk_bytes) != 32:
        return None
    ephemeral_pk = X25519PublicKey.from_public_bytes(ephemeral_pk_bytes)

    recipient_sk = serialization.load_pem_private_key(
        recipient_x25519_private_pem, password=None
    )
    shared_secret = recipient_sk.exchange(ephemeral_pk)
    aes_key = _hkdf(shared_secret, b"agent-comm-v1")

    nonce = base64.b64decode(encrypted_msg["nonce"])
    ciphertext = base64.b64decode(encrypted_msg["ciphertext"])

    sender_fingerprint = encrypted_msg.get("from", "")
    if sender_ed25519_fingerprint is not None:
        if sender_fingerprint != sender_ed25519_fingerprint:
            return None

    aad = sender_fingerprint.encode("utf-8")
    cipher = AESGCMSIV(aes_key)
    return cipher.decrypt(nonce, ciphertext, aad)


def decrypt_message_auto(
    encrypted_msg: dict,
    recipient_x25519_private_pem: bytes,
    peer_contacts_dir: str,
) -> tuple[bytes | None, str | None]:
    """Decrypt by auto-detecting sender from contacts directory."""
    import glob

    sender_fingerprint = encrypted_msg.get("from")
    if not sender_fingerprint:
        return None, None

    for cf in glob.glob(os.path.join(peer_contacts_dir, "peer-*.json")):
        try:
            with open(cf) as f:
                contact = json.load(f)
            if contact.get("_fingerprint") == sender_fingerprint:
                result = decrypt_message(encrypted_msg, recipient_x25519_private_pem, sender_fingerprint)
                if result is not None:
                    peer_id = os.path.splitext(os.path.basename(cf))[0].split("peer-", 1)[1]
                    return result, peer_id
        except Exception:
            continue

    return None, None


if __name__ == "__main__":
    print("Message encryption utilities. Import as a module.")
    sys.exit(1)