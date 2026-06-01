#!/usr/bin/env python3

"""Universal Chat Relay Bridge for agent-comm.

This script polls agent-comm's loopback inbox and routes incoming messages to a 
host AI command line. The stdout of the command is sent back as the reply.
"""

from __future__ import annotations

import argparse
import json
import subprocess
import time
import sys
import shlex
from urllib.request import Request, urlopen
from urllib.error import URLError

def poll_inbox(port: int) -> list[dict]:
    url = f"http://127.0.0.1:{port}/messages/inbox"
    req = Request(url, headers={"User-Agent": "agent-comm-bridge/1.0"})
    try:
        with urlopen(req, timeout=5) as response:
            data = json.loads(response.read().decode("utf-8"))
            msgs = data.get("messages")
            return msgs if msgs is not None else []
    except Exception as e:
        print(f"[Bridge] Connection to agent-comm daemon failed: {e}. Retrying...", file=sys.stderr)
        return []

def send_reply(port: int, recipient_urn: str, content: str) -> bool:
    url = f"http://127.0.0.1:{port}/messages/send"
    payload = json.dumps({
        "recipient_urn": recipient_urn,
        "content": content
    }).encode("utf-8")
    
    req = Request(
        url,
        data=payload,
        headers={
            "User-Agent": "agent-comm-bridge/1.0",
            "Content-Type": "application/json"
        },
        method="POST"
    )
    try:
        with urlopen(req, timeout=5) as response:
            res_data = json.loads(response.read().decode("utf-8"))
            return res_data.get("status") == "success"
    except Exception as e:
        print(f"[Bridge] Failed to send reply to agent-comm: {e}", file=sys.stderr)
        return False

def execute_command(command: list[str], message: str, use_stdin: bool) -> str:
    try:
        if use_stdin:
            # Pass message to stdin
            result = subprocess.run(
                command,
                input=message,
                capture_output=True,
                text=True,
                check=True
            )
        else:
            # Append message to the command args
            result = subprocess.run(
                command + [message],
                capture_output=True,
                text=True,
                check=True
            )
        return result.stdout.strip()
    except subprocess.CalledProcessError as e:
        print(f"[Bridge] Subprocess execution failed: {e.stderr}", file=sys.stderr)
        return f"Error executing agent command: {e.stderr.strip() or str(e)}"
    except Exception as e:
        print(f"[Bridge] Error executing subprocess: {e}", file=sys.stderr)
        return f"Error executing agent command: {str(e)}"

def main() -> int:
    parser = argparse.ArgumentParser(description="Bridge agent-comm messages to a command-line AI agent.")
    parser.add_argument("--port", "-p", type=int, default=8000, help="Local agent-comm HTTP server port (default: 8000)")
    parser.add_argument("--command", "-c", required=True, help="Command to invoke host agent (e.g., 'python hermes.py --ask')")
    parser.add_argument("--stdin", "-s", action="store_true", help="Pass incoming message via stdin instead of command arguments")
    parser.add_argument("--interval", "-i", type=float, default=3.0, help="Polling interval in seconds (default: 3.0)")
    args = parser.parse_args()

    # Split command into a list of strings for subprocess
    cmd_list = shlex.split(args.command)
    if not cmd_list:
        print("[Bridge] Invalid empty command specified.", file=sys.stderr)
        return 1

    print(f"[Bridge] Chat Relay Bridge started.")
    print(f"[Bridge] Polling: http://127.0.0.1:{args.port}/messages/inbox every {args.interval}s")
    print(f"[Bridge] Routing to CLI: {cmd_list} (passing message via {'stdin' if args.stdin else 'args'})")

    try:
        while True:
            messages = poll_inbox(args.port)
            for msg in messages:
                sender = msg.get("sender_urn")
                content = msg.get("content")
                if not sender or content is None:
                    continue
                
                print(f"[Bridge] New message from owner: {content!r}")
                
                # Execute agent logic
                reply = execute_command(cmd_list, content, args.stdin)
                
                if reply:
                    print(f"[Bridge] Agent reply: {reply!r}")
                    success = send_reply(args.port, sender, reply)
                    if success:
                        print(f"[Bridge] Reply delivered successfully.")
                    else:
                        print(f"[Bridge] Failed to deliver reply.")
                else:
                    print(f"[Bridge] Agent generated empty response. Skipped sending.")

            time.sleep(args.interval)
    except KeyboardInterrupt:
        print("\n[Bridge] Bridge terminated by user.")
    return 0

if __name__ == "__main__":
    sys.exit(main())
