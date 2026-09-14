"""Local pairing administration and optional mailbox RPC daemon.

Use the Hermes adapter OR this daemon as the active consumer for a helper, never
both. The standalone daemon exposes only read methods; a host adapter supplies
real conversation handlers. No public listening socket is opened.
"""
import argparse
import json
from pathlib import Path
import sys
import time

from .remote import CONTROL_KINDS, READ_METHODS, RemoteBridge, hermes_principal
from .store import Store
from .transport import HelperTransport


def parser():
    root = argparse.ArgumentParser(prog="agent-comm-runtime")
    remote = root.add_subparsers(dest="group", required=True).add_parser("remote")
    commands = remote.add_subparsers(dest="command", required=True)
    for name in ("pair", "revoke", "pairings", "serve"):
        command = commands.add_parser(name)
        command.add_argument("--state", type=Path, help="Remote state SQLite path; defaults to PROFILE/agent-comm/remote.sqlite3")
        command.add_argument("--hermes-profile", type=Path, help="Existing Hermes profile path; computes the local owner principal")
        if name in {"pair", "revoke"}:
            command.add_argument("--console-urn", required=True)
        if name == "pair":
            command.add_argument("--owner-principal", help="Explicit principal for a non-Hermes host")
            command.add_argument("--allow", action="append", required=True, help="One explicit method; repeat for each allowed method")
            command.add_argument("--expires", required=True, help="RFC3339 pairing expiry, including timezone")
        if name == "serve":
            command.add_argument("--collaboration-state", type=Path)
            command.add_argument("--agent-urn", required=True)
            command.add_argument("--helper-url", default="http://127.0.0.1:45042")
            command.add_argument("--once", action="store_true", help="Process one bounded inbox batch then exit")
    return root


def main(argv=None):
    for stream in (sys.stdout, sys.stderr):
        if callable(getattr(stream, "reconfigure", None)):
            stream.reconfigure(encoding="utf-8")
    cli = parser()
    args = cli.parse_args(argv)
    profile = args.hermes_profile.expanduser().resolve() if args.hermes_profile else None
    if args.state is None and profile is None:
        cli.error("Provide --state or --hermes-profile; no real profile is guessed")
    remote_path = args.state or profile / "agent-comm" / "remote.sqlite3"
    store = None
    bridge = None
    try:
        # Pairing administration needs only remote SQLite, not private collaboration data.
        agent_urn = args.agent_urn if args.command == "serve" else "urn:agent-comm:agent:localAdministration"
        if args.command == "serve":
            collab = args.collaboration_state or (profile / "agent-comm" / "collaboration.sqlite3" if profile else None)
            if collab is None:
                cli.error("serve requires --collaboration-state or --hermes-profile")
            store = Store(collab, local_urn=agent_urn)
        bridge = RemoteBridge(remote_path, store, agent_urn)
        if args.command == "pair":
            principal = args.owner_principal or (hermes_principal(profile) if profile else None)
            if principal is None:
                cli.error("pair requires --owner-principal or --hermes-profile")
            result = bridge.pair(args.console_urn, principal, args.allow, args.expires)
            print(json.dumps({"status": "paired_locally", **result}, ensure_ascii=False))
        elif args.command == "revoke":
            print(json.dumps(bridge.revoke(args.console_urn), ensure_ascii=False))
        elif args.command == "pairings":
            print(json.dumps({"pairings": bridge.pairings()}, ensure_ascii=False))
        else:
            transport = HelperTransport(args.helper_url, timeout=10)
            while True:
                processed = 0
                errors = 0
                for message in bridge.select_inbox_batch(transport.retrieve()):
                    try:
                        if message.get("kind") == "control.request":
                            bridge.process(message, transport)
                        elif message.get("kind") in CONTROL_KINDS:
                            continue  # Belongs to a control client, never ordinary owner memory.
                        else:
                            store.ingest_message(message)
                            transport.ack([message["message_id"]])
                        processed += 1
                    except (ValueError, TypeError, KeyError, OSError):
                        errors += 1  # Keep malformed/failed messages pending without logging plaintext.
                if args.once:
                    print(json.dumps({"processed": processed, "pending_errors": errors, "methods": READ_METHODS}))
                    break
                time.sleep(1)
        return 0
    except (ValueError, OSError) as exc:
        print(str(exc), file=sys.stderr)
        return 2
    except KeyboardInterrupt:
        return 130
    finally:
        if bridge is not None:
            bridge.close()
        if store is not None:
            store.close()


if __name__ == "__main__":
    raise SystemExit(main())
