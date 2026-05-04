#!/usr/bin/env python3
"""Ed25519 + X25519 identity keypair management for agent authentication."""

import os
import sys

KEY_DIR = os.path.expanduser("~/.openclaw/workspace/skills/agent-comm/contacts")
PRIV_KEY_FILE = os.path.join(KEY_DIR, "identity_sk.pem")
PUB_KEY_FILE = os.path.join(KEY_DIR, "identity_pk.pem")
X25519_PRIV_KEY_FILE = os.path.join(KEY_DIR, "identity_x25519_sk.pem")
X25519_PUB_KEY_FILE = os.path.join(KEY_DIR, "identity_x25519_pk.pem")


def _import_crypto():
    """Lazily import cryptography, exit with clear instructions if unavailable."""
    try:
        from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey, Ed25519PublicKey
        from cryptography.hazmat.primitives.asymmetric.x25519 import X25519PrivateKey, X25519PublicKey
        from cryptography.hazmat.primitives import serialization
        return Ed25519PrivateKey, Ed25519PublicKey, X25519PrivateKey, X25519PublicKey, serialization
    except ImportError:
        print("ERROR: cryptography library not installed.", file=sys.stderr)
        print("Run: uv pip install --python ~/.openclaw/venvs/kg/bin/python3 cryptography", file=sys.stderr)
        sys.exit(1)


def _load_or_generate_ed25519():
    """Load existing Ed25519 keypair, or generate a new one."""
    Ed25519PrivateKey, Ed25519PublicKey, _, _, serialization = _import_crypto()

    if os.path.exists(PRIV_KEY_FILE) and os.path.exists(PUB_KEY_FILE):
        with open(PRIV_KEY_FILE, "rb") as f:
            priv = f.read()
        with open(PUB_KEY_FILE, "rb") as f:
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

    os.makedirs(KEY_DIR, exist_ok=True)
    with open(PRIV_KEY_FILE, "wb") as f:
        f.write(private_bytes)
    os.chmod(PRIV_KEY_FILE, 0o600)
    with open(PUB_KEY_FILE, "wb") as f:
        f.write(public_bytes)

    return private_bytes, public_bytes


def _load_or_generate_x25519():
    """Load existing X25519 keypair, or generate a new one."""
    _, _, X25519PrivateKey, X25519PublicKey, serialization = _import_crypto()

    if os.path.exists(X25519_PRIV_KEY_FILE) and os.path.exists(X25519_PUB_KEY_FILE):
        with open(X25519_PRIV_KEY_FILE, "rb") as f:
            priv = f.read()
        with open(X25519_PUB_KEY_FILE, "rb") as f:
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

    os.makedirs(KEY_DIR, exist_ok=True)
    with open(X25519_PRIV_KEY_FILE, "wb") as f:
        f.write(private_bytes)
    os.chmod(X25519_PRIV_KEY_FILE, 0o600)
    with open(X25519_PUB_KEY_FILE, "wb") as f:
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
    public_key = serialization.load_pem_public_key(public_key_pem)
    try:
        public_key.verify(signature, data)
        return True
    except Exception:
        return False


def encode_hex(data: bytes) -> str:
    """Encode bytes as hex string."""
    return data.hex()


def decode_hex(hex_str: str) -> bytes:
    """Decode hex string to bytes."""
    return bytes.fromhex(hex_str)


def compute_fingerprint(ed25519_public_pem: bytes) -> str:
    """
    Compute SHA-256 fingerprint of Ed25519 public key, truncated to 16 hex chars.
    Used as the agent's stable identity identifier.
    """
    import hashlib
    return hashlib.sha256(ed25519_public_pem).hexdigest()[:16]


if __name__ == "__main__":
    print("Identity keypair utilities. Import as a module.")
    sys.exit(1)