#!/usr/bin/env python3
"""
Centralized path configuration for agent-comm skill.
All scripts import from here so paths are defined once and kept relative.
"""

import os

# The directory containing this skill (agent-comm/)
SKILL_DIR = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
# Scripts subdirectory
SCRIPTS_DIR = os.path.join(SKILL_DIR, "scripts")
# Contacts directory (key material, tokens, peer contacts)
CONTACTS_DIR = os.path.join(SKILL_DIR, "contacts")
# Message queue subdirectory
QUEUE_DIR = os.path.join(CONTACTS_DIR, "message_queue")
# Share output directory
SHARE_OUTPUT_DIR = os.path.join(SKILL_DIR, "share-output")

# Key files
IDENTITY_SK = os.path.join(CONTACTS_DIR, "identity_sk.pem")
IDENTITY_PK = os.path.join(CONTACTS_DIR, "identity_pk.pem")
IDENTITY_X25519_SK = os.path.join(CONTACTS_DIR, "identity_x25519_sk.pem")
IDENTITY_X25519_PK = os.path.join(CONTACTS_DIR, "identity_x25519_pk.pem")
AUTH_TOKEN_FILE = os.path.join(CONTACTS_DIR, "auth_token.json")
ISSUED_TOKENS_FILE = os.path.join(CONTACTS_DIR, "issued_tokens.json")
TOKEN_LOCK_FILE = ISSUED_TOKENS_FILE + ".lock"

# Server settings
LISTEN_HOST = "127.0.0.1"
LISTEN_PORT = 18792

# Runtime (Python path is managed via KG runtime convention — no hardcoding here)
LOG_DIR = "/tmp"

# Cloudflare tunnel log
TUNNEL_LOG = os.path.join(LOG_DIR, "cloudflared-tunnel.log")