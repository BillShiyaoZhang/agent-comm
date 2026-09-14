"""Runnable terminal host + explicitly selected finite memory reference adapter.

    python -m agent_comm_runtime.reference --demo
    python -m agent_comm_runtime.reference --state ./collaboration.sqlite3

The terminal process owner is trusted; do not attach its stdin to a remote peer
or let model text impersonate terminal input. Production hosts must substitute
their authenticated current-session handle and native interaction callback.
"""
import argparse
import hashlib
import json
from pathlib import Path
import secrets
import sys
import tempfile

from .ports import AdapterRegistry, Descriptor, HostSession, MemorySnapshot
from .runtime import Runtime
from .store import Store
from .transport import HelperTransport


class TerminalHost:
    descriptor = Descriptor("reference-local-terminal", "host", ("owner_context",))

    def __init__(self, profile):
        self.principal = "terminal-" + hashlib.sha256(str(Path(profile).resolve()).encode()).hexdigest()
        self.session_id = secrets.token_hex(16)
        self.turn = 0
        self._authority = object()
        self.active = True

    def begin_turn(self):
        self.turn += 1
        return self._authority

    def capture(self, context):
        if context is not self._authority or not self.active:
            raise ValueError("Only the local terminal driver can supply its owner context")
        return HostSession(self.principal, self.session_id, str(self.turn), self._authority)

    def revalidate(self, session):
        if (not self.active or session.opaque is not self._authority
                or session.session_id != self.session_id or session.turn_id != str(self.turn)):
            raise ValueError("The terminal session or current turn changed")


class TerminalInteraction:
    descriptor = Descriptor("reference-terminal-answer", "interaction", ("confirmation",))

    def request_confirmation(self, session, question):
        if not sys.stdin.isatty():
            raise ValueError("Confirmation requires the owner's interactive terminal")
        print(question)
        return input("在此确认问题回答同意或拒绝 > ")


class FiniteMemory:
    """Only owner-selected records, no filesystem traversal or graph export.

    Search candidates deliberately have no URN-to-contact auto-binding; memory
    mentions may differ from the actual network identity. Reads never truncate:
    ask for a smaller selected record when it exceeds the caller's bound.
    """
    descriptor = Descriptor("reference-selected-records", "memory", ("search", "read_snapshot"))

    def __init__(self, records):
        if not isinstance(records, dict):
            raise ValueError("Memory input must be a map of selected reference IDs")
        self.records = records

    def search(self, session, query, limit):
        results = []
        for ref, record in self.records.items():
            if query.casefold() in (record["title"] + " " + record["text"]).casefold():
                results.append({"reference": ref, "title": record["title"], "summary": record["text"][:200]})
                if len(results) >= limit:
                    break
        return results

    def read_snapshot(self, session, reference, max_chars):
        record = self.records[reference]
        if len(record["text"]) > max_chars:
            raise ValueError("Selected record is too long; select a smaller record explicitly")
        return MemorySnapshot(reference, record["title"], record["text"], "owner-selected-reference-memory", record["version"])


def demo():
    """An offline smoke demo; no approvals, sends, real files or host SDK."""
    with tempfile.TemporaryDirectory(prefix="agent-comm-reference-") as folder:
        store = Store(Path(folder) / "collaboration.sqlite3")
        host = TerminalHost(folder)
        registry = AdapterRegistry().register(host).register(TerminalInteraction())
        registry.register(FiniteMemory({"intro": {"title": "试用介绍", "text": "一份明确选择的示例资料。", "version": "1"}}))
        runtime = Runtime(store, registry)
        try:
            for args in ({"action": "describe"}, {"action": "state"},
                         {"action": "memory_search", "query": "试用"},
                         {"action": "snapshot_resource", "reference": "intro", "resource_id": "intro-v1"}):
                print(json.dumps(runtime.dispatch(args, context=host.begin_turn()), ensure_ascii=False))
        finally:
            store.close()


def main(argv=None):
    for stream in (sys.stdout, sys.stderr):
        if callable(getattr(stream, "reconfigure", None)):
            stream.reconfigure(encoding="utf-8")
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--demo", action="store_true", help="Run an isolated offline adapter smoke demo")
    parser.add_argument("--state", type=Path, default=Path("collaboration.sqlite3"))
    parser.add_argument("--memory-json", type=Path, help="Explicit owner-selected memory records only")
    parser.add_argument("--helper-url", help="Optional loopback helper; no transport is registered by default")
    parser.add_argument("--agent-urn")
    args = parser.parse_args(argv)
    if args.demo:
        demo()
        return 0
    if not sys.stdin.isatty():
        parser.error("The reference host requires an interactive terminal; use --demo for offline smoke verification")
    store = Store(args.state, local_urn=args.agent_urn)
    host = TerminalHost(args.state.parent)
    registry = AdapterRegistry().register(host).register(TerminalInteraction())
    if args.memory_json:
        registry.register(FiniteMemory(json.loads(args.memory_json.read_text(encoding="utf-8"))))
    if args.helper_url:
        registry.register(HelperTransport(args.helper_url))
    runtime = Runtime(store, registry)
    print('输入工具 JSON，例如 {"action":"describe"}；exit 退出。')
    try:
        while True:
            line = input("owner > ").strip()
            if line in {"exit", "quit"}:
                break
            try:
                result = runtime.dispatch(json.loads(line), context=host.begin_turn())
            except ValueError as exc:
                result = {"status": "invalid_json", "error": str(exc)}
            print(json.dumps(result, ensure_ascii=False, indent=2))
    except (EOFError, KeyboardInterrupt):
        pass
    finally:
        host.active = False
        store.close()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
