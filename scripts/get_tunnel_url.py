#!/usr/bin/env python3
"""Get the current Cloudflare Tunnel URL from the log file."""

import re
import sys
import os

sys.path.insert(0, os.path.dirname(__file__))
from paths import TUNNEL_LOG


def get_tunnel_url(log_path: str | None = None) -> str | None:
    path = log_path or TUNNEL_LOG
    try:
        with open(path) as f:
            content = f.read()
    except FileNotFoundError:
        return None

    # Look for URL like https://xxx.trycloudflare.com — return the LAST one (current tunnel)
    matches = re.findall(r'https://[a-zA-Z0-9\-]+\.trycloudflare\.com', content)
    if matches:
        return matches[-1]
    return None


if __name__ == "__main__":
    url = get_tunnel_url()
    if url:
        print(url)
        sys.exit(0)
    else:
        print("ERROR: Could not find tunnel URL. Is cloudflared running?", file=sys.stderr)
        sys.exit(1)