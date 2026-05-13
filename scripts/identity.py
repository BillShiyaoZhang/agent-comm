#!/usr/bin/env python3
"""Ed25519 + X25519 identity keypair management for agent authentication."""

import os
import sys

# Add scripts/ to path for local imports
sys.path.insert(0, os.path.dirname(__file__))
from paths import CONTACTS_DIR, IDENTITY_SK, IDENTITY_PK, IDENTITY_X25519_SK, IDENTITY_X25519_PK


def _import_crypto():
    """Lazily import cryptography, exit with clear instructions if unavailable."""
    try:
        from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey, Ed25519PublicKey
        from cryptography.hazmat.primitives.asymmetric.x25519 import X25519PrivateKey, X25519PublicKey
        from cryptography.hazmat.primitives import serialization
        return Ed25519PrivateKey, Ed25519PublicKey, X25519PrivateKey, X25519PublicKey, serialization
    except ImportError:
        print("ERROR: cryptography library not installed.", file=sys.stderr)
        print("Run: uv pip install cryptography flask", file=sys.stderr)
        sys.exit(1)


def _load_or_generate_ed25519():
    """Load existing Ed25519 keypair, or generate a new one."""
    Ed25519PrivateKey, Ed25519PublicKey, _, _, serialization = _import_crypto()

    if os.path.exists(IDENTITY_SK) and os.path.exists(IDENTITY_PK):
        with open(IDENTITY_SK, "rb") as f:
            priv = f.read()
        with open(IDENTITY_PK, "rb") as f:
            pub = f.read()
        return priv, pub

    pk = Ed25519PrivateKey.generate()
    private_bytes = pk.private_bytes(
        encoding=serialization.Encoding.PEM,
        format=serialization.PrivateFormat.PKCS8,
        encryption_algorithm=serialization.NoEncryption()
    )
    public_bytes = pk.public_key().public_bytes(
        encoding=serialization.Encoding.PEM,
        format=serialization.PublicFormat.SubjectPublicKeyInfo
    )

    os.makedirs(CONTACTS_DIR, exist_ok=True)
    with open(IDENTITY_SK, "wb") as f:
        f.write(private_bytes)
    os.chmod(IDENTITY_SK, 0o600)
    with open(IDENTITY_PK, "wb") as f:
        f.write(public_bytes)

    return private_bytes, public_bytes


def _load_or_generate_x25519():
    """Load existing X25519 keypair, or generate a new one."""
    _, _, X25519PrivateKey, X25519PublicKey, serialization = _import_crypto()

    if os.path.exists(IDENTITY_X25519_SK) and os.path.exists(IDENTITY_X25519_PK):
        with open(IDENTITY_X25519_SK, "rb") as f:
            priv = f.read()
        with open(IDENTITY_X25519_PK, "rb") as f:
            pub = f.read()
        return priv, pub

    pk = X25519PrivateKey.generate()
    private_bytes = pk.private_bytes(
        encoding=serialization.Encoding.PEM,
        format=serialization.PrivateFormat.PKCS8,
        encryption_algorithm=serialization.NoEncryption()
    )
    public_bytes = pk.public_key().public_bytes(
        encoding=serialization.Encoding.PEM,
        format=serialization.PublicFormat.SubjectPublicKeyInfo
    )

    os.makedirs(CONTACTS_DIR, exist_ok=True)
    with open(IDENTITY_X25519_SK, "wb") as f:
        f.write(private_bytes)
    os.chmod(IDENTITY_X25519_SK, 0o600)
    with open(IDENTITY_X25519_PK, "wb") as f:
        f.write(public_bytes)

    return private_bytes, public_bytes


def get_or_create_keypair() -> tuple[bytes, bytes, bytes, bytes]:
    """
    Return (ed25519_private_pem, ed25519_public_pem, x25519_private_pem, x25519_public_pem).
    Creates all keys if missing. Existing keys are reused.
    """
    ed_priv, ed_pub = _load_or_generate_ed25519()
    x_priv, x_pub = _load_or_generate_x25519()
    return ed_priv, ed_pub, x_priv, x_pub


def sign_data(private_key_pem: bytes, data: bytes) -> bytes:
    """Sign data with Ed25519 private key. Returns raw 64-byte signature."""
    Ed25519PrivateKey, _, _, _, serialization = _import_crypto()
    private_key = serialization.load_pem_private_key(private_key_pem, password=None)
    return private_key.sign(data)


def verify_signature(public_key_pem: bytes, data: bytes, signature: bytes) -> bool:
    """Verify an Ed25519 signature. Returns True if valid, False otherwise."""
    _, Ed25519PublicKey, _, _, serialization = _import_crypto()
    try:
        if len(public_key_pem) == 32:
            # Raw 32-byte Ed25519 key — load from raw bytes
            public_key = Ed25519PublicKey.from_public_bytes(public_key_pem)
        else:
            public_key = serialization.load_pem_public_key(public_key_pem)
        public_key.verify(signature, data)
        return True
    except Exception:
        return False


def encode_hex(data: bytes) -> str:
    """Encode bytes as hex string."""
    return data.hex()


def decode_hex(hex_str: str) -> bytes:
    """Decode hex string to bytes. Also handles PEM strings transparently."""
    if "-----BEGIN" in hex_str:
        # PEM string passed directly — return as-is for decode_pub_key
        return hex_str.encode()
    return bytes.fromhex(hex_str)


def decode_pub_key(key_data: bytes | str) -> bytes:
    """
    Decode a public key from PEM or raw 32-byte hex string.
    Returns 32-byte raw Ed25519 public key.
    """
    if isinstance(key_data, str):
        key_data = key_data.encode()
    if key_data.startswith(b"-----BEGIN"):
        from cryptography.hazmat.primitives import serialization
        from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PublicKey
        pk = serialization.load_pem_public_key(key_data)
        return pk.public_bytes(serialization.Encoding.Raw, serialization.PublicFormat.Raw)
    # Try as hex first
    try:
        return bytes.fromhex(key_data.decode())
    except Exception:
        return key_data  # assume raw bytes


def compute_fingerprint(ed25519_public_key: bytes) -> str:
    """Compute SHA-256 fingerprint of an Ed25519 public key, truncated to 16 hex chars.

    Always normalizes the input to raw 32-byte key material before hashing,
    so the result is identical whether the caller passes PEM-encoded bytes or
    already-decoded raw bytes. Previously this function hashed the raw PEM text,
    while register_peer.py hashed the decoded raw key — causing fingerprint
    mismatches that broke message routing and decryption.
    """
    import hashlib
    raw = decode_pub_key(ed25519_public_key)  # PEM -> raw 32 bytes; raw bytes pass through
    return hashlib.sha256(raw).hexdigest()[:16]


if __name__ == "__main__":
    print("Identity keypair utilities. Import as a module.")
    sys.exit(1)