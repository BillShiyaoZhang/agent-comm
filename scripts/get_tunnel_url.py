#!/usr/bin/env python3
"""Get the current Cloudflare Tunnel URL from the log file."""

import re
import sys

LOG_PATH = "/tmp/cloudflared-tunnel.log"


def get_tunnel_url(log_path: str = LOG_PATH) -> str | None:
    try:
        with open(log_path) as f:
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