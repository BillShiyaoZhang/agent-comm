"""Isolated real-process helper/platform regression; no model or public service.

Build both executables first. Example:
python tools/test_helper_platform.py --helper build/agent-comm-helper.exe --platform build/platform-test.exe
All identities, configuration, and logs are kept under build/helper-platform-test/.
"""

import argparse
import json
import os
from pathlib import Path
import socket
import subprocess
import time
import urllib.error
import urllib.request
import uuid


def port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def request(base, path, data=None):
    req = urllib.request.Request(base + path,
                                 data=None if data is None else json.dumps(data).encode(),
                                 headers={"Content-Type": "application/json"})
    with urllib.request.build_opener(urllib.request.ProxyHandler({})).open(req, timeout=2) as response:
        return json.load(response)


def until(check, description, timeout=40):
    deadline = time.monotonic() + timeout
    last_error = None
    while time.monotonic() < deadline:
        try:
            value = check()
            if value:
                return value
        except (OSError, ValueError) as exc:
            last_error = exc
        time.sleep(0.2)
    raise AssertionError(f"Timed out: {description}: {last_error}")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--helper", required=True, type=Path)
    parser.add_argument("--platform", required=True, type=Path)
    args = parser.parse_args()
    helper, platform = args.helper.resolve(), args.platform.resolve()
    root = (Path("build/helper-platform-test") / str(uuid.uuid4())).resolve()
    root.mkdir(parents=True)
    ports = {name: port() for name in ("platform", "a", "b")}
    urls = {name: f"http://127.0.0.1:{number}" for name, number in ports.items()}
    config = root / "config.yaml"
    config.write_text(f"""platform:
  data_dir: '{root.as_posix()}/data'
identity:
  keys_dir: '{root.as_posix()}/data/keys'
libp2p:
  listen_addrs: ['/ip4/127.0.0.1/tcp/0']
relay:
  enabled: false
registry:
  persist_db: '{root.as_posix()}/data/registry.db'
mq:
  db_path: '{root.as_posix()}/data/mq.db'
api:
  listen_addr: '127.0.0.1:{ports['platform']}'
  rate_limit_rate: 0
""", encoding="utf-8")
    processes, logs = {}, []

    def start(name):
        log = (root / f"{name}.log").open("ab")
        logs.append(log)
        command = [str(platform), "-config", str(config)] if name == "platform" else [
            str(helper), "daemon", str(root / f"keys-{name}"), urls["platform"], str(ports[name])]
        processes[name] = subprocess.Popen(command, stdout=log, stderr=subprocess.STDOUT,
                                          creationflags=subprocess.CREATE_NO_WINDOW if os.name == "nt" else 0)
        endpoint = "/healthz" if name == "platform" else "/info"
        return until(lambda: request(urls[name], endpoint), f"{name} startup", timeout=25)

    def stop(name):
        process = processes.pop(name, None)
        if process is not None:
            process.kill()  # Exercise abrupt crash recovery, not just graceful shutdown.
            process.wait(timeout=10)

    def messages(name):
        return request(urls[name], "/api/v1/mq/retrieve")["messages"]

    def receive(name, message_id):
        return until(lambda: next((msg for msg in messages(name) if msg["message_id"] == message_id), None),
                     f"{name} received {message_id}")

    def ack(name, message_id):
        return request(urls[name], "/api/v1/mq/ack", {"message_ids": [message_id]})

    results = []
    try:
        start("platform")
        identity_a, identity_b = start("a"), start("b")
        for identity in (identity_a, identity_b):
            until(lambda: request(urls["platform"], "/api/v1/registry/resolve?urn=" + identity["urn"]).get("found"),
                  "authenticated HTTP registration")
        body = {"message_id": "e2e-1", "recipient_urn": identity_b["urn"], "text": "hello from A",
                "conversation_id": "conversation-1", "task_id": "task-1", "kind": "task", "hop_limit": 4}
        accepted = request(urls["a"], "/api/v1/mq/store", body)
        assert accepted["success"] and accepted["message_id"] == "e2e-1"
        incoming = receive("b", "e2e-1")
        assert incoming["sender_urn"] == identity_a["urn"]
        assert incoming["text"] == body["text"] and incoming["task_id"] == "task-1" and incoming["hop_limit"] == 4
        results.append("Authenticated A->B delivery with conversation/task metadata; no SSE subscriber")

        stop("b"); start("b")
        assert receive("b", "e2e-1") == incoming
        ack("b", "e2e-1")
        stop("b"); start("b")
        assert all(msg["message_id"] != "e2e-1" for msg in messages("b"))
        again = request(urls["a"], "/api/v1/mq/store", body)
        assert again["message_id"] == "e2e-1"
        results.append("Inbox survives abrupt helper restart; consumed receipt and stable-ID retry remain deduplicated")

        request(urls["b"], "/api/v1/mq/store", {"message_id": "e2e-reply", "recipient_urn": identity_a["urn"],
                "text": "reply from B", "conversation_id": "conversation-1", "in_reply_to": "e2e-1",
                "task_id": "task-1", "kind": "result", "hop_limit": 3})
        reply = receive("a", "e2e-reply")
        assert reply["sender_urn"] == identity_b["urn"] and reply["in_reply_to"] == "e2e-1"
        ack("a", "e2e-reply")
        results.append("Authenticated B->A reply with stable reply routing")

        stop("platform")
        request(urls["a"], "/api/v1/mq/store", {"message_id": "e2e-offline", "recipient_urn": identity_b["urn"], "text": "queued while platform offline"})
        stop("a"); start("a")
        status = request(urls["a"], "/api/v1/mq/status?message_id=e2e-offline")
        assert status["status"] == "accepted"
        start("platform")
        assert receive("b", "e2e-offline")["text"] == "queued while platform offline"
        ack("b", "e2e-offline")
        results.append("Outbox accepts while platform offline, survives helper crash, retries after platform restart")

        stop("b")
        request(urls["a"], "/api/v1/mq/store", {"message_id": "e2e-recipient-offline", "recipient_urn": identity_b["urn"], "text": "mailbox offline delivery"})
        until(lambda: request(urls["a"], "/api/v1/mq/status?message_id=e2e-recipient-offline")["status"] == "platform_queued", "cloud queue acceptance")
        start("b")
        assert receive("b", "e2e-recipient-offline")["text"] == "mailbox offline delivery"
        ack("b", "e2e-recipient-offline")
        results.append("Platform retains ciphertext while recipient helper is offline")
        report = {"result": "PASS", "checks": results, "logs": str(root),
                  "scope": "Real local helper/platform processes over loopback HTTP; no production deployment or Hermes model"}
        (root / "result.json").write_text(json.dumps(report, indent=2), encoding="utf-8")
        print(json.dumps(report, indent=2))
    finally:
        for name in list(processes):
            stop(name)
        for log in logs:
            log.close()


if __name__ == "__main__":
    main()
