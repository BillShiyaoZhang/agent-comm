#!/usr/bin/env python3

"""Automates agent-comm identity registration with the platform registry.

Signs the registry payload using agent-comm-helper and delivers the register 
request to the platform HTTP endpoint.
"""

from __future__ import annotations

import argparse
import base64
import json
import os
import struct
import subprocess
import sys
import time
import urllib.request
import urllib.parse
from urllib.error import HTTPError, URLError

def get_helper_path() -> str:
    home = os.path.expanduser("~")
    binary_name = "agent-comm-helper.exe" if sys.platform == "win32" else "agent-comm-helper"
    return os.environ.get("AGENT_COMM_HELPER_PATH", os.path.join(home, ".agent-comm", "bin", binary_name))

def invoke_helper(args: list[str]) -> dict:
    helper_path = get_helper_path()
    try:
        res = subprocess.run([helper_path] + args, capture_output=True, text=True, check=True)
        parsed = json.loads(res.stdout.strip())
        if "error" in parsed:
            raise Exception(f"Helper error: {parsed['error']}")
        return parsed
    except subprocess.CalledProcessError as e:
        raise Exception(f"Helper failed: {e}. Stderr: {e.stderr.strip()}")
    except Exception as e:
        raise Exception(f"Failed to invoke helper binary: {e}")

def main() -> int:
    parser = argparse.ArgumentParser(description="Register agent-comm identity with the Platform registry.")
    parser.add_argument("--keys-dir", "-k", required=True, help="Path to your keys directory")
    parser.add_argument("--platform-url", "-p", default="http://8.130.40.38", help="Platform base URL (default: http://8.130.40.38)")
    args = parser.parse_args()

    keys_dir = os.path.abspath(os.path.expanduser(args.keys_dir))
    platform_url = args.platform_url.strip()
    if platform_url.endswith("/"):
        platform_url = platform_url[:-1]

    print(f"[*] Initializing local identity from keys: {keys_dir}")
    try:
        init_res = invoke_helper(["init", keys_dir])
    except Exception as e:
        print(f"[❌] Error: Failed to load identity keys: {e}", file=sys.stderr)
        return 1

    urn = init_res["urn"]
    ed25519_pubkey_hex = init_res["ed25519_pubkey"]
    x25519_pubkey_hex = init_res["x25519_pubkey"]

    print(f"[+] Loaded URN: {urn}")
    print(f"[+] Ed25519 PubKey: {ed25519_pubkey_hex}")
    print(f"[+] X25519 PubKey: {x25519_pubkey_hex}")

    # Build registry signed message
    # Format: urn + "|" + peerID + "|" + x25519PubkeyHex + "|" + flag + "|" + big_endian_timestamp (8 bytes)
    timestamp = int(time.time())
    peer_id = "12D3KooWKzoJzGnRpfd9ohJTYzGQbebi4rvRh1LWNnb4EaUBTThS" # virtual peer ID placeholder
    flag = "0" # stores_user_data is false

    canonical_str = f"{urn}|{peer_id}|{x25519_pubkey_hex}|{flag}|"
    ts_bytes = struct.pack(">q", timestamp)
    message_bytes = canonical_str.encode("utf-8") + ts_bytes

    print("[*] Signing registry payload...")
    try:
        sign_res = invoke_helper(["sign-store", keys_dir, message_bytes.hex()])
    except Exception as e:
        print(f"[❌] Error: Signing registry message failed: {e}", file=sys.stderr)
        return 1

    signature_hex = sign_res["signature"]

    # Construct request JSON body
    req_body = {
        "urn": urn,
        "peer_id": peer_id,
        "addrs": [],
        "relay_addrs": [],
        "x25519_pubkey": base64.b64encode(bytes.fromhex(x25519_pubkey_hex)).decode("utf-8"),
        "ed25519_pubkey": base64.b64encode(bytes.fromhex(ed25519_pubkey_hex)).decode("utf-8"),
        "stores_user_data": False,
        "signature": base64.b64encode(bytes.fromhex(signature_hex)).decode("utf-8"),
        "timestamp": timestamp
    }

    body_bytes = json.dumps(req_body).encode("utf-8")

    print("[*] Signing HTTP request body...")
    try:
        http_sign_res = invoke_helper(["sign-store", keys_dir, body_bytes.hex()])
    except Exception as e:
        print(f"[❌] Error: Signing HTTP request body failed: {e}", file=sys.stderr)
        return 1

    http_sig_hex = http_sign_res["signature"]
    auth_header = f"Ed25519 {http_sig_hex}:{ed25519_pubkey_hex}"

    register_url = f"{platform_url}/api/v1/registry/register"
    print(f"[*] Posting registration to: {register_url}")

    req = urllib.request.Request(
        register_url,
        data=body_bytes,
        headers={
            "Content-Type": "application/json",
            "Authorization": auth_header,
            "User-Agent": "agent-comm-register/1.0"
        },
        method="POST"
    )

    try:
        with urllib.request.urlopen(req, timeout=10) as response:
            res_body = response.read().decode("utf-8")
            parsed_res = json.loads(res_body)
            if parsed_res.get("ok") is True:
                print(f"[✅] Registration successful! URN {urn} is now registered on the platform.")
                return 0
            else:
                print(f"[❌] Registration failed: {res_body}", file=sys.stderr)
                return 1
    except HTTPError as e:
        print(f"[❌] Registration HTTP Error {e.code}: {e.read().decode('utf-8')}", file=sys.stderr)
        return 1
    except URLError as e:
        print(f"[❌] Connection Error: {e.reason}", file=sys.stderr)
        return 1

if __name__ == "__main__":
    sys.exit(main())
