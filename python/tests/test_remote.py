"""Two authenticated helper identities, real persistent authority, no network."""
from contextlib import redirect_stdout
from datetime import datetime, timezone
import io
import json
from pathlib import Path
import tempfile
import threading
import unittest

from agent_comm_runtime.daemon import main
from agent_comm_runtime.remote import PROTOCOL, READ_METHODS, RemoteBridge, hermes_principal
from agent_comm_runtime.store import Store

NOW = 2_000_000_000
AGENT = "urn:agent-comm:agent:hermes"
CONSOLE = "urn:agent-comm:agent:console"
OTHER = "urn:agent-comm:agent:otherconsole"


def stamp(value):
    return datetime.fromtimestamp(value, timezone.utc).isoformat()


def request(request_id="request-1", method="capabilities", params=None, console=CONSOLE):
    packet = {"protocol": PROTOCOL, "type": "request", "request_id": request_id, "method": method,
              "params": params or {}, "agent_urn": AGENT, "console_urn": console, "deadline": stamp(NOW + 120)}
    return {"message_id": request_id, "recipient_urn": AGENT, "kind": "control.request",
            "conversation_id": "control:" + request_id, "deadline": packet["deadline"], "text": json.dumps(packet)}


class MailNetwork:
    """Models the helper's authenticated sender stamping and stable store IDs."""
    def __init__(self):
        self.mail = {AGENT: {}, CONSOLE: {}, OTHER: {}}
        self.accepted = {}
        self.events = []
        self.fail_store = False
        self.fail_ack = False

    def endpoint(self, urn):
        network = self
        class Endpoint:
            def store(self, body):
                if network.fail_store:
                    raise OSError("helper unavailable")
                key = (urn, body["message_id"])
                old = network.accepted.get(key)
                if old is not None and old != body:
                    raise ValueError("stable message ID conflicts")
                network.accepted[key] = dict(body)
                wire = {**body, "sender_urn": urn}
                network.mail[body["recipient_urn"]][body["message_id"]] = wire
                network.events.append(("stored", urn, body["message_id"]))
                return {"success": True, "status": "accepted", "message_id": body["message_id"]}
            def retrieve(self):
                return list(network.mail[urn].values())
            def ack(self, ids):
                if network.fail_ack:
                    raise OSError("ack unavailable")
                for message_id in ids:
                    network.mail[urn].pop(message_id, None)
                    network.events.append(("acked", urn, message_id))
                return {"success": True}
        return Endpoint()


class TestRemote(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.home = Path(self.temp.name)
        self.now = NOW
        self.store = Store(self.home / "collaboration.sqlite3", clock=lambda: self.now, local_urn=AGENT)
        self.bridge = RemoteBridge(self.home / "remote.sqlite3", self.store, AGENT, clock=lambda: self.now, conversations=True)
        self.network = MailNetwork()
        self.console, self.agent = self.network.endpoint(CONSOLE), self.network.endpoint(AGENT)
        self.bridge.pair(CONSOLE, "owner-a", [*READ_METHODS, "conversation.send", "conversation.get"], stamp(NOW + 1000))
        self.bridge.pair(OTHER, "owner-b", [*READ_METHODS, "conversation.send", "conversation.get"], stamp(NOW + 1000))

    def tearDown(self):
        self.bridge.close()
        self.store.close()
        self.temp.cleanup()

    def submit(self, body=None):
        self.console.store(body or request())
        wire = self.agent.retrieve()[0]
        return wire, self.bridge.process(wire, self.agent)

    def result(self, response):
        return json.loads(response["text"])

    def contact(self, name, owner):
        staged = self.store.prepare_contact(name, [name], "urn:agent-comm:agent:" + name, owner + "|native")
        lease = self.store.begin_confirmation(staged["approval_id"], owner + "|native")
        self.store.finish_confirmation(staged["approval_id"], lease["token"], owner + "|native", "同意")

    def test_two_identity_helper_rpc_has_correlated_response_and_ordered_ack(self):
        _, response = self.submit()
        packet = self.result(response)
        self.assertEqual(packet["request_id"], "request-1")
        self.assertEqual(packet["method"], "capabilities")
        self.assertEqual(packet["agent_urn"], AGENT)
        self.assertEqual(packet["console_urn"], CONSOLE)
        self.assertEqual(response["in_reply_to"], "request-1")
        self.assertEqual(response["deadline"], stamp(NOW + 120))
        self.assertEqual(self.network.events[-2:], [("stored", AGENT, response["message_id"]), ("acked", AGENT, "request-1")])
        native_approval = next(m for m in packet["result"]["methods"] if m["name"] == "approval.respond")
        self.assertFalse(native_approval["available"])

    def test_persisted_replay_does_not_execute_handler_again(self):
        calls = []
        self.bridge.register_handler("test.read", lambda params, owner: calls.append(owner) or {"value": len(calls)})
        self.bridge.pair(CONSOLE, "owner-a", ["test.read"], stamp(NOW + 1000))
        wire, first = self.submit(request(method="test.read"))
        self.bridge.close()
        self.bridge = RemoteBridge(self.home / "remote.sqlite3", self.store, AGENT, clock=lambda: self.now)
        self.bridge.register_handler("test.read", lambda params, owner: self.fail("replay must not execute"))
        second = self.bridge.process(wire, self.agent)
        self.assertEqual(first, second)
        self.assertEqual(calls, ["owner-a|remote:" + __import__("hashlib").sha256(CONSOLE.encode()).hexdigest()[:24]])

    def test_contact_state_and_inbox_are_owner_scoped(self):
        self.contact("alice", "owner-a")
        self.contact("bob", "owner-b")
        self.store.ingest_message({"message_id": "alice-message", "sender_urn": "urn:agent-comm:agent:alice", "text": "owner a"})
        self.store.ingest_message({"message_id": "bob-message", "sender_urn": "urn:agent-comm:agent:bob", "text": "owner b"})
        _, response = self.submit(request(method="contacts.list"))
        self.assertEqual([c["contact_id"] for c in self.result(response)["result"]["contacts"]], ["alice"])
        _, response = self.submit(request("inbox-a", "inbox.list"))
        self.assertEqual([m["text"] for m in self.result(response)["result"]["messages"]], ["owner a"])
        other = self.network.endpoint(OTHER)
        other.store(request("state-b", "collaboration.state", console=OTHER))
        response = self.bridge.process(self.agent.retrieve()[0], self.agent)
        self.assertEqual([c["contact_id"] for c in self.result(response)["result"]["contacts"]], ["bob"])

    def test_params_cannot_claim_owner_or_remote_approval(self):
        _, response = self.submit(request(method="collaboration.state", params={"owner_session": "owner-b|native"}))
        self.assertEqual(self.result(response)["error"]["code"], "invalid_params")
        _, response = self.submit(request("approval", "approval.respond", {"approved": True}))
        self.assertEqual(self.result(response)["error"]["code"], "method_not_allowed")

    def test_revocation_and_expiry_block_cached_disclosure_and_new_turns(self):
        wire, _ = self.submit(request(method="contacts.list"))
        self.bridge.revoke(CONSOLE)
        self.assertEqual(self.result(self.bridge.process(wire, self.agent))["error"]["code"], "not_paired")
        self.bridge.pair(CONSOLE, "owner-a", READ_METHODS, stamp(NOW + 10))
        self.now += 11
        self.assertEqual(self.result(self.bridge.process(wire, self.agent))["error"]["code"], "not_paired")

    def test_sender_envelope_and_target_identity_mismatch_never_ack(self):
        body = request()
        body["sender_urn"] = OTHER
        with self.assertRaises(ValueError):
            self.bridge.process(body, self.agent)
        body["sender_urn"] = CONSOLE
        body["conversation_id"] = "control:wrong"
        with self.assertRaises(ValueError):
            self.bridge.process(body, self.agent)
        self.assertEqual(self.network.events, [])

    def test_expired_request_does_not_execute_and_can_be_consumed(self):
        body = request(method="conversation.send", params={"text": "do work"})
        body["sender_urn"] = CONSOLE
        self.now += 121
        self.assertIsNone(self.bridge.process(body, self.agent))
        self.assertIsNone(self.bridge.claim_turn())
        self.assertEqual(self.network.events, [("acked", AGENT, "request-1")])

    def test_response_failure_and_ack_failure_are_safely_retryable(self):
        self.console.store(request(method="conversation.send", params={"text": "hello"}))
        wire = self.agent.retrieve()[0]
        self.network.fail_store = True
        with self.assertRaises(OSError):
            self.bridge.process(wire, self.agent)
        self.assertEqual(len(self.agent.retrieve()), 1)
        self.network.fail_store = False
        self.network.fail_ack = True
        with self.assertRaises(OSError):
            self.bridge.process(wire, self.agent)
        self.network.fail_ack = False
        self.bridge.process(wire, self.agent)
        self.assertEqual(len(self.bridge._all("turn")), 1)

    def test_conversation_ack_is_submission_and_host_result_is_separate(self):
        _, response = self.submit(request(method="conversation.send", params={"text": "hello", "conversation_id": "chat-1"}))
        self.assertEqual(self.result(response)["result"]["status"], "submitted")
        job = self.bridge.claim_turn()
        _, running = self.submit(request("poll-1", "conversation.get", {"conversation_id": "chat-1"}))
        self.assertEqual(self.result(running)["result"]["turns"][0]["status"], "running")
        self.assertIsNone(self.result(running)["result"]["turns"][0]["response"])
        self.bridge.finish_turn(job["turn_id"], response="actual host answer")
        _, completed = self.submit(request("poll-2", "conversation.get", {"conversation_id": "chat-1"}))
        turn = self.result(completed)["result"]["turns"][0]
        self.assertEqual(turn["status"], "completed")
        self.assertEqual(turn["response"], "actual host answer")
        self.network.endpoint(OTHER).store(request("poll-other", "conversation.get", {"conversation_id": "chat-1"}, OTHER))
        other = self.bridge.process(self.agent.retrieve()[0], self.agent)
        self.assertEqual(self.result(other)["result"]["turns"], [])

    def test_request_conflict_and_crash_do_not_repeat_conversation_work(self):
        self.submit(request(method="conversation.send", params={"text": "one"}))
        altered = request(method="conversation.send", params={"text": "two"})
        altered["sender_urn"] = CONSOLE
        response = self.bridge.handle(altered)
        self.assertEqual(self.result(response)["error"]["code"], "request_conflict")
        self.bridge.claim_turn()
        self.bridge.recover_interrupted_turns()
        self.assertIsNone(self.bridge.claim_turn())
        self.assertEqual(self.bridge._all("turn")[0]["status"], "interrupted")

    def test_local_pair_cli_derives_profile_principal_without_private_config(self):
        profile = self.home / "new-empty-profile"
        profile.mkdir()
        with redirect_stdout(io.StringIO()) as output:
            code = main(["remote", "pair", "--hermes-profile", str(profile), "--console-urn", CONSOLE,
                         "--allow", "capabilities", "--expires", "2099-01-01T00:00:00Z"])
        self.assertEqual(code, 0)
        self.assertEqual(json.loads(output.getvalue())["owner_principal"], hermes_principal(profile))
        self.assertFalse((profile / "config.yaml").exists())
        self.assertTrue((profile / "agent-comm" / "remote.sqlite3").exists())

    def test_existing_web_hermes_namespace_can_pair_and_receive_rpc(self):
        web = "urn:hermes:agent:VBYEE9xFV6AiTZQcsvbBEz"
        self.network.mail[web] = {}
        self.bridge.pair(web, "owner-a", ["capabilities"], stamp(NOW + 1000))
        self.network.endpoint(web).store(request(console=web))
        response = self.bridge.process(self.agent.retrieve()[0], self.agent)
        self.assertEqual(self.result(response)["console_urn"], web)
        self.assertIn("result", self.result(response))

    def test_revoke_between_snapshot_and_delivery_suppresses_old_result(self):
        wire = {**request(method="contacts.list"), "sender_urn": CONSOLE}
        snapshot = self.bridge.handle(wire)
        self.assertIn("result", self.result(snapshot))
        self.bridge.revoke(CONSOLE)
        with self.bridge.delivery(wire, snapshot) as response:
            self.assertNotIn("result", self.result(response))
            self.assertEqual(self.result(response)["error"]["code"], "not_paired")

    def test_local_revoke_is_linearized_after_inflight_bounded_send(self):
        second = RemoteBridge(self.home / "remote.sqlite3", self.store, AGENT, clock=lambda: self.now)
        started, completed = threading.Event(), threading.Event()
        def revoke():
            started.set()
            second.revoke(CONSOLE)
            completed.set()
        thread = threading.Thread(target=revoke)
        wire = {**request(), "sender_urn": CONSOLE}
        snapshot = self.bridge.handle(wire)
        try:
            with self.bridge.delivery(wire, snapshot):
                thread.start()
                self.assertTrue(started.wait(1))
                self.assertFalse(completed.wait(.03))
            thread.join(2)
            self.assertTrue(completed.is_set())
        finally:
            if thread.is_alive():
                thread.join(3)
            second.close()

    def test_persistent_cursor_moves_past_hundred_unconsumed_poison_messages(self):
        messages = [{"message_id": f"poison-{i:03d}", "kind": "control.response"} for i in range(100)]
        messages.append({"message_id": "z-valid", "kind": "control.request"})
        self.assertNotIn("z-valid", [m["message_id"] for m in self.bridge.select_inbox_batch(messages)])
        self.bridge.close()
        self.bridge = RemoteBridge(self.home / "remote.sqlite3", self.store, AGENT, clock=lambda: self.now)
        self.assertEqual(self.bridge.select_inbox_batch(messages)[0]["message_id"], "z-valid")


if __name__ == "__main__":
    unittest.main()
