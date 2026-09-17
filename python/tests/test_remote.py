"""Two authenticated helper identities, real persistent authority, no network."""
from contextlib import contextmanager, redirect_stdout
from datetime import datetime, timezone
import io
import json
from pathlib import Path
import tempfile
import threading
import unittest
from unittest.mock import patch

from agent_comm_runtime.daemon import main
from agent_comm_runtime.remote import PROTOCOL, READ_METHODS, WRITE_METHODS, RemoteBridge, hermes_principal
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
                network.mail.setdefault(body["recipient_urn"], {})[body["message_id"]] = wire
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

    def allow_web_actions(self):
        self.bridge.pair(CONSOLE, "owner-a", [*READ_METHODS, *WRITE_METHODS], stamp(NOW + 2000))

    def pending_task(self, task_id="task-web", expires_at=None):
        if not self.store.resolve_contact("peer", "owner-a|native")["contacts"]:
            self.contact("peer", "owner-a")
        scope = {"purpose": "Coordinate one meeting", "topic": "Web confirmation",
                 "capabilities": ["send_text"], "recipient_ids": ["peer"], "participant_ids": ["self", "peer"],
                 "resource_ids": [], "window_start": stamp(NOW + 10), "window_end": stamp(NOW + 1000),
                 "max_duration_minutes": 30, "max_candidates": 2, "max_actions": 10,
                 "expires_at": expires_at or stamp(NOW + 1000)}
        return self.store.prepare_task(task_id, scope, "owner-a|native")

    def test_mutation_capabilities_require_explicit_scope_and_cannot_be_overridden(self):
        _, before = self.submit()
        methods = {item["name"]: item["available"] for item in self.result(before)["result"]["methods"]}
        self.assertTrue(all(not methods[method] for method in WRITE_METHODS))
        self.allow_web_actions()
        _, after = self.submit(request("after-pair"))
        methods = {item["name"]: item["available"] for item in self.result(after)["result"]["methods"]}
        self.assertTrue(all(methods[method] for method in WRITE_METHODS))
        for method in WRITE_METHODS:
            with self.assertRaises(ValueError):
                self.bridge.register_handler(method, lambda params, owner: {})

    def test_contacts_add_is_deterministic_pending_and_owner_scoped(self):
        self.allow_web_actions()
        params = {"contact_id": "wang", "aliases": [" 老王 ", "Wang", "Wang"], "urn": "urn:hermes:agent:wang"}
        wire, first = self.submit(request("add-wang", "contacts.add", params))
        result = self.result(first)["result"]
        self.assertEqual(result["status"], "requested")
        self.assertEqual(result["contact"]["connection_status"], "pending")
        self.assertEqual(result["contact"]["aliases"], ["Wang", "老王"])
        self.assertEqual(self.bridge.process(wire, self.agent), first)
        _, duplicate = self.submit(request("add-wang-again", "contacts.add", params))
        self.assertEqual(self.result(duplicate)["result"]["status"], "already_requested")
        self.assertEqual(self.store.state("owner-a|native")["contacts"], [result["contact"]])
        self.assertEqual(self.store.state("owner-b|native")["contacts"], [])
        self.assertEqual(self.bridge._all("turn"), [])
        for changed in ({**params, "urn": "urn:agent-comm:agent:changed"},
                        {**params, "contact_id": "duplicate-urn"}, {**params, "contact_id": "self"},
                        {**params, "aliases": []}, {**params, "owner_session": "owner-b"}):
            _, response = self.submit(request("invalid-contact-" + str(len(self.network.events)), "contacts.add", changed))
            self.assertEqual(self.result(response)["error"]["code"], "invalid_params")
        self.assertEqual(len(self.store.state("owner-a|native")["contacts"]), 1)

    def test_contacts_add_resolves_exact_native_pending_binding_and_supersedes_other_binding(self):
        self.allow_web_actions()
        pending = self.store.prepare_contact("wang", ["Wang"], "urn:agent-comm:agent:wang", "owner-a|native")
        other = self.store.prepare_contact("wang", ["Wang"], "urn:agent-comm:agent:otherwang", "owner-a|native")
        lease = self.store.begin_confirmation(pending["approval_id"], "owner-a|native")
        before = self.store.attention("owner-a|native")
        self.submit(request("add-pending", "contacts.add", {"contact_id": "wang", "aliases": ["Wang"], "urn": "urn:agent-comm:agent:wang"}))
        state = self.store.state("owner-a|native")
        self.assertEqual(state["pending_confirmations"], [])
        self.assertEqual([(a["approval_id"], a["status"]) for a in state["approval_decisions"]], [(pending["approval_id"], "approved")])
        self.assertEqual(set(state["approval_decisions"][0]), {"approval_id", "kind", "subject_id", "status"})
        changes = {item["approval_id"]: item["state"] for item in self.store.attention("owner-a|native", before["cursor"])["items"]}
        self.assertEqual(changes, {pending["approval_id"]: "resolved", other["approval_id"]: "superseded"})
        with self.assertRaisesRegex(ValueError, "No matching active"):
            self.store.finish_confirmation(pending["approval_id"], lease["token"], "owner-a|native", "拒绝")

    def test_remote_approval_replaces_native_lease_and_cannot_be_reversed_or_cross_owner(self):
        self.allow_web_actions()
        pending = self.store.prepare_contact("peer", ["Peer"], "urn:agent-comm:agent:peer", "owner-a|native")
        lease = self.store.begin_confirmation(pending["approval_id"], "owner-a|native")
        self.bridge.pair(OTHER, "owner-b", ["approval.respond"], stamp(NOW + 1000))
        body = {**request("wrong-owner", "approval.respond", {"approval_id": pending["approval_id"], "decision": "approve"}, OTHER), "sender_urn": OTHER}
        self.assertEqual(self.result(self.bridge.handle(body))["error"]["code"], "invalid_params")
        _, response = self.submit(request("approve-contact", "approval.respond", {"approval_id": pending["approval_id"], "decision": "approve"}))
        self.assertEqual(self.result(response)["result"]["status"], "approved_once")
        with self.assertRaisesRegex(ValueError, "No matching active"):
            self.store.finish_confirmation(pending["approval_id"], lease["token"], "owner-a|native", "拒绝")
        _, response = self.submit(request("reverse-contact", "approval.respond", {"approval_id": pending["approval_id"], "decision": "deny"}))
        self.assertEqual(self.result(response)["error"]["code"], "invalid_params")
        self.assertEqual(self.store.state("owner-b|native")["approval_decisions"], [])

    def test_remote_approval_applies_task_and_operation_without_sending_business_messages(self):
        self.allow_web_actions()
        pending = self.pending_task()
        self.submit(request("approve-task", "approval.respond", {"approval_id": pending["approval_id"], "decision": "approve"}))
        self.assertEqual(self.store.state("owner-a|native")["tasks"][0]["status"], "active")
        for decision, status in (("approve", "ready"), ("deny", "denied")):
            action = {"capability": "send_text", "recipient_ids": ["peer"], "payload": {"text": "Exact owner-reviewed text"}}
            pending = self.store.prepare_action("task-web", "op-" + decision, action, "owner-a|native")
            self.submit(request("decide-" + decision, "approval.respond", {"approval_id": pending["approval_id"], "decision": decision}))
            self.assertEqual(self.store._get("operation", "op-" + decision)["status"], status)
            self.assertEqual(self.store._get("operation", "op-" + decision).get("deliveries", []), [])
        self.assertTrue(all(body["kind"].startswith("control.") or body["kind"] == "contact.request" for body in self.network.accepted.values()))

    def test_remote_approval_rejects_revoked_expired_superseded_and_invalid_decisions(self):
        self.allow_web_actions()
        for change in ("revoked", "expired", "superseded"):
            pending = self.pending_task("task-" + change, stamp(NOW + 60))
            task = self.store._get("task", "task-" + change)
            if change == "revoked":
                self.store.revoke(task["task_id"], "owner-a|native")
            elif change == "superseded":
                with self.store._transaction():
                    self.store._put("task", task["task_id"], {**task, "revision": 2})
            else:
                self.now = NOW + 61
            _, response = self.submit(request("stale-" + change, "approval.respond", {"approval_id": pending["approval_id"], "decision": "approve"}))
            self.assertEqual(self.result(response)["error"]["code"], "invalid_params")
            self.assertNotEqual(self.store._get("task", task["task_id"])["status"], "active")
            self.now = NOW
        pending = self.store.prepare_contact("invalid-decision", ["Peer"], "urn:agent-comm:agent:other", "owner-a|native")
        for answer in (True, "yes", "approve if possible", None, []):
            _, response = self.submit(request("invalid-answer-" + str(len(self.network.events)), "approval.respond", {"approval_id": pending["approval_id"], "decision": answer}))
            self.assertEqual(self.result(response)["error"]["code"], "invalid_params")

    def test_remote_decision_can_renew_expired_presentation_without_extending_task_grant(self):
        self.allow_web_actions()
        pending = self.store.prepare_contact("renew", ["Renew"], "urn:agent-comm:agent:renew", "owner-a|native")
        self.now = NOW + 1000
        body = request("late-web-decision", "approval.respond", {"approval_id": pending["approval_id"], "decision": "approve"})
        payload = json.loads(body["text"])
        payload["deadline"] = body["deadline"] = stamp(self.now + 120)
        body["text"] = json.dumps(payload)
        _, response = self.submit(body)
        self.assertEqual(self.result(response)["result"]["status"], "approved_once")

    def test_mutation_receipt_survives_crash_before_bridge_cache_and_rejects_changed_replay(self):
        self.allow_web_actions()
        for method in ("contacts.add", "approval.respond"):
            if method == "contacts.add":
                params = {"contact_id": "crash-contact", "aliases": ["Crash"], "urn": "urn:agent-comm:agent:crash"}
            else:
                pending = self.store.prepare_contact("crash-approval", ["Crash"], "urn:agent-comm:agent:crashapproval", "owner-a|native")
                params = {"approval_id": pending["approval_id"], "decision": "approve"}
            wire = {**request("crash-" + method, method, params), "sender_urn": CONSOLE}
            original_put = self.bridge._put
            def fail_response_cache(kind, key, value):
                if kind == "request":
                    raise OSError("process stopped after business commit")
                return original_put(kind, key, value)
            with patch.object(self.bridge, "_put", side_effect=fail_response_cache):
                with self.assertRaises(OSError):
                    self.bridge.handle(wire)
            audit_count = len(self.store._all("audit"))
            self.bridge.close()
            self.store.close()
            self.store = Store(self.home / "collaboration.sqlite3", clock=lambda: self.now, local_urn=AGENT)
            self.bridge = RemoteBridge(self.home / "remote.sqlite3", self.store, AGENT, clock=lambda: self.now)
            altered = json.loads(wire["text"])
            altered["params"] = {**params, **({"contact_id": "another-contact"} if method == "contacts.add" else {"decision": "deny"})}
            changed = self.bridge.handle({**wire, "text": json.dumps(altered)})
            self.assertEqual(self.result(changed)["error"]["code"], "request_conflict")
            recovered = self.bridge.handle(wire)
            self.assertEqual(self.result(recovered)["result"]["status"], "requested" if method == "contacts.add" else "approved_once")
            self.assertEqual(len(self.store._all("audit")), audit_count)
            self.bridge.revoke(CONSOLE)
            self.assertEqual(self.result(self.bridge.handle(wire))["error"]["code"], "not_paired")
            self.allow_web_actions()

    def test_mutation_attention_failure_rolls_back_business_and_receipt(self):
        self.allow_web_actions()
        pending = self.store.prepare_contact("rollback", ["Rollback"], "urn:agent-comm:agent:rollback", "owner-a|native")
        wire = {**request("rollback", "approval.respond", {"approval_id": pending["approval_id"], "decision": "approve"}), "sender_urn": CONSOLE}
        with patch.object(self.store, "_attention_put", side_effect=OSError("disk full")):
            with self.assertRaises(OSError):
                self.bridge.handle(wire)
        self.assertIsNone(self.store._get("contact", "rollback"))
        self.assertEqual(self.store._all("remote_mutation"), [])
        self.assertEqual(self.store._get("approval", pending["approval_id"])["status"], "pending")

    def test_mutation_rechecks_request_and_pairing_expiry_after_waiting_for_store(self):
        for pairing_seconds, elapsed in ((1000, 121), (60, 61)):
            self.now = NOW
            self.bridge.pair(CONSOLE, "owner-a", WRITE_METHODS, stamp(NOW + pairing_seconds))
            wire = {**request("expired-after-wait-" + str(elapsed), "contacts.add", {
                "contact_id": "late", "aliases": ["Late"], "urn": "urn:agent-comm:agent:late"}), "sender_urn": CONSOLE}
            transaction = self.store._transaction
            @contextmanager
            def delayed_transaction():
                with transaction():
                    self.now += elapsed
                    yield
            with patch.object(self.store, "_transaction", delayed_transaction):
                self.assertIn("error", self.result(self.bridge.handle(wire)))
            self.assertIsNone(self.store._get("contact", "late"))
            self.assertEqual(self.store._all("remote_mutation"), [])

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

    def test_excessive_json_nesting_is_rejected_without_stopping_mailbox_processing(self):
        for depth in (40, 1200):
            with self.subTest(depth=depth):
                malformed = {**request(), "sender_urn": CONSOLE}
                malformed["text"] = malformed["text"].replace('"params": {}',
                    '"params": {"nested": ' + '[' * depth + '0' + ']' * depth + '}')
                self.assertLess(len(malformed["text"].encode()), 50000)
                with self.assertRaisesRegex(ValueError, "nesting"):
                    self.bridge.process(malformed, self.agent)
        self.assertEqual(self.network.events, [])
        _, response = self.submit(request("after-malformed"))
        self.assertIn("result", self.result(response))

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
