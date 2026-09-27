"""Real SQLite owner ACL/review tests; fake transport never contacts a service."""
import copy
import hashlib
import json
from pathlib import Path
import threading
import unittest

import test_social as social
import test_collaboration_v2 as protocol
import test_remote as remote
from agent_comm_runtime.store import Store
from agent_comm_runtime.social import key
from agent_comm_runtime.runtime import validate_args
from agent_comm_runtime.ports import Unsupported
from agent_comm_runtime.worker import run_worker_tick


class PeerSafetyTests(unittest.TestCase):
    def setUp(self):
        self.f = social.TestSocial()
        self.f.setUp()
        self.addCleanup(self.f.doCleanups)
        self.f.connect()
        self.store = self.f.b
        self.owner = "bob|owner-review"

    def incoming(self, message_id="content-1", text="peer private body"):
        return {"message_id": message_id, "sender_urn": social.A, "text": text, "kind": "chat.message"}

    def approve(self, message_id="content-1"):
        self.store.review_preview(message_id, self.owner)
        return self.store.review_peer(message_id, "approve", self.owner)

    def test_pending_has_only_metadata_and_requires_same_owner_preview(self):
        self.assertEqual(self.store.ingest_message(self.incoming())["status"], "pending_review")
        inbox = self.store.inbox(self.owner)
        self.assertEqual(set(inbox["pending_review"][0]), {"message_id", "sender_urn", "kind", "received_at", "status"})
        self.assertNotIn("peer private body", json.dumps(self.store.state(self.owner)))
        self.assertNotIn("content-1", [i["subject_id"] for i in self.store.attention(self.owner)["items"]])
        with self.assertRaises(ValueError):
            self.store.review_peer("content-1", "approve", self.owner)
        with self.assertRaises(ValueError):
            self.store.review_preview("content-1", "other|review")
        preview = self.store.review_preview("content-1", self.owner)
        self.assertEqual(preview["text"], "peer private body")
        self.assertFalse(preview["text_truncated"])
        self.assertEqual(len(preview["fingerprint"]), 64)
        with self.assertRaises(ValueError):
            self.store.review_peer("content-1", "approve", "bob|other-console")
        receipt = self.store.review_peer("content-1", "approve", self.owner)
        self.assertEqual(receipt["status"], "approved")
        self.assertEqual(receipt["fingerprint"], preview["fingerprint"])
        self.assertEqual(next(m for m in self.store.inbox(self.owner)["messages"] if m["message_id"] == "content-1")["text"], preview["text"])
        self.assertEqual(self.store.review_peer("content-1", "approve", self.owner), receipt)

    def test_rejection_is_terminal_across_restart_and_replay(self):
        self.store.ingest_message(self.incoming())
        self.store.review_peer("content-1", "reject", self.owner)
        with self.assertRaises(ValueError):
            self.approve()
        with self.assertRaises(ValueError):
            self.store.ingest_message(self.incoming(text="changed"))
        reopened = Store(Path(self.f.temp.name) / "b.sqlite3", local_urn=social.B, clock=lambda: self.f.now)
        self.addCleanup(reopened.close)
        self.assertEqual(reopened.ingest_message(self.incoming())["status"], "already_recorded")
        self.assertEqual(reopened.inbox(self.owner)["pending_review"], [])
        self.assertNotIn("peer private body", json.dumps(reopened.state(self.owner)))

    def test_preview_overflow_never_records_blind_approval_permission(self):
        self.store.ingest_message(self.incoming(text="\x01" * 50000))
        with self.assertRaisesRegex(ValueError, "response limit"):
            self.store.review_preview("content-1", self.owner)
        self.assertIsNone(self.store._get("peer_review_preview", key(self.owner, "content-1")))
        with self.assertRaises(ValueError):
            self.store.review_peer("content-1", "approve", self.owner)

    def test_block_retires_old_content_outbox_and_never_restores_it(self):
        self.store.ingest_message(self.incoming())
        self.approve()
        self.f.mutate(self.store, "messages.send", {"recipient_urn": social.A, "text": "queued", "message_id": "queued-1"}, "bob")
        receipt = self.store.set_peer_block(social.A, True, self.owner)
        self.assertEqual(receipt, {"urn": social.A, "status": "blocked", "blocked": True, "connection_status": "blocked", "safety_revision": 1})
        self.assertEqual(self.store.state(self.owner)["contacts"][0]["blocked"], True)
        self.assertEqual(self.store.flush_social_outbox(self.f.tb)["accepted"], 0)
        self.assertNotIn((social.B, "queued-1"), self.f.network.accepted)
        self.assertEqual(self.store.ingest_message(self.incoming("blocked-arrival"))["status"], "blocked")
        with self.assertRaises(ValueError):
            self.store.review_preview("content-1", self.owner)
        self.store.set_peer_block(social.A, False, self.owner)
        self.assertEqual(self.store.flush_social_outbox(self.f.tb)["accepted"], 0)
        self.assertNotIn("peer private body", json.dumps(self.store.state(self.owner)))
        self.assertEqual(self.store.ingest_message(self.incoming("blocked-arrival"))["status"], "already_recorded")
        self.assertEqual(self.store.ingest_message(self.incoming("fresh-arrival"))["status"], "pending_review")

    def test_unknown_blocked_sender_is_real_acl_and_friend_replay_stays_hidden(self):
        self.store.set_peer_block(social.C, True, self.owner)
        self.assertEqual(self.store.state(self.owner)["blocked_peers"][0]["urn"], social.C)
        request = {"message_id": "bad-friend", "sender_urn": social.C, "kind": "contact.request",
            "text": json.dumps({"protocol": "agent-comm-contacts/v1", "type": "request", "request_id": "bad-friend"})}
        self.assertEqual(self.store.ingest_message(request)["status"], "blocked")
        self.store.set_peer_block(social.C, False, self.owner)
        self.store.register_owner(self.owner)
        self.assertEqual(self.store.ingest_message(request)["status"], "already_recorded")
        self.assertNotIn("bad-friend", [r["request_id"] for r in self.store.contact_requests(self.owner)["contact_requests"]])

    def test_owner_acl_does_not_change_other_profile_or_its_queue(self):
        other = "other|native"
        request = self.store.prepare_contact("other-alice", ["Alice"], social.A, other)
        lease = self.store.begin_confirmation(request["approval_id"], other)
        self.store.finish_confirmation(request["approval_id"], lease["token"], other, "同意")
        with self.store._transaction():
            self.store._put("connection", key("other", social.A), {"owner_id": "other", "peer_urn": social.A, "request_id": "other-connected", "connected_at": self.f.now})
        self.f.mutate(self.store, "messages.send", {"recipient_urn": social.A, "text": "other queue", "message_id": "other-queue"}, "other")
        self.store.set_peer_block(social.A, True, self.owner)
        self.assertEqual(self.store.state(other)["blocked_peers"], [])
        self.assertFalse(self.store.state(other)["contacts"][0]["blocked"])
        self.store.flush_social_outbox(self.f.tb)
        self.assertIn((social.B, "other-queue"), self.f.network.accepted)

    def test_block_serializes_with_inflight_helper_admission_across_handles(self):
        self.f.mutate(self.store, "messages.send", {"recipient_urn": social.A, "text": "first", "message_id": "race-1"}, "bob")
        self.f.mutate(self.store, "messages.send", {"recipient_urn": social.A, "text": "second", "message_id": "race-2"}, "bob")
        other = Store(Path(self.f.temp.name) / "b.sqlite3", local_urn=social.B, clock=lambda: self.f.now)
        self.addCleanup(other.close)
        entered, release, blocked = threading.Event(), threading.Event(), threading.Event()
        errors = []
        base = self.f.tb
        class Transport:
            def store(_self, body):
                entered.set()
                if not release.wait(3):
                    raise OSError("test did not release admission")
                return base.store(body)
        def send():
            try:
                self.store.flush_social_outbox(Transport(), limit=1)
            except BaseException as exc:
                errors.append(exc)
        def block():
            try:
                other.set_peer_block(social.A, True, self.owner)
                blocked.set()
            except BaseException as exc:
                errors.append(exc)
        sender = threading.Thread(target=send)
        blocker = threading.Thread(target=block)
        sender.start()
        self.assertTrue(entered.wait(3))
        blocker.start()
        self.assertFalse(blocked.wait(.05))
        release.set()
        sender.join(3)
        blocker.join(3)
        self.assertFalse(sender.is_alive() or blocker.is_alive())
        self.assertEqual(errors, [])
        self.assertTrue(blocked.is_set())
        self.store.flush_social_outbox(base)
        self.assertIn((social.B, "race-1"), self.f.network.accepted)
        self.assertNotIn((social.B, "race-2"), self.f.network.accepted)

    def test_model_runtime_cannot_request_preview_review_or_block(self):
        for action in ("contacts.block", "contacts.unblock", "inbox.review_preview", "inbox.review", "review_peer", "set_peer_block"):
            with self.assertRaises(Unsupported):
                validate_args({"action": action})

    def test_schema_upgrade_preserves_records_and_requires_new_reader(self):
        with self.store._transaction():
            self.store._db.execute("UPDATE collaboration_meta SET version=1")
        before = self.store._db.execute("SELECT kind,id,body FROM collaboration_records ORDER BY kind,id").fetchall()
        reopened = Store(Path(self.f.temp.name) / "b.sqlite3", local_urn=social.B)
        self.addCleanup(reopened.close)
        self.assertEqual(reopened._db.execute("SELECT version FROM collaboration_meta").fetchone()[0], 2)
        self.assertEqual(reopened._db.execute("SELECT kind,id,body FROM collaboration_records ORDER BY kind,id").fetchall(), before)
        self.assertEqual(reopened.state(self.owner)["contacts"][0]["urn"], social.A)


class ProtocolPeerSafetyTests(unittest.TestCase):
    def setUp(self):
        self.f = protocol.CollaborationV2Tests()
        self.f.setUp()
        self.addCleanup(self.f.doCleanups)
        self.f.joined()

    def test_sync_recovery_applies_evidence_but_quarantines_nested_proposal(self):
        f = self.f
        f.send(f.a, "proposal", protocol.proposal(topic="协作协议"))
        f.send(f.a, "accept")
        messages = f.tb.retrieve()
        accepted = next(m for m in messages if json.loads(m["text"])["kind"] == "accept")
        f.b.ingest_message(accepted)
        f.tb.ack([m["message_id"] for m in messages])
        f.send(f.b, "sync_request")
        f.transfer(f.a)
        for op in f.a.collaborations(protocol.OWNER_A)["operations"]:
            if op["kind"] == "sync_response" and op["status"] == "ready":
                f.a.dispatch(op["operation_id"], protocol.OWNER_A, f.ta)
        recovery = next(m for m in f.tb.retrieve() if json.loads(m["text"])["kind"] == "sync_response")
        f.b.ingest_message(recovery)
        pending = f.b.inbox(protocol.OWNER_B)["pending_review"]
        self.assertTrue(pending)
        self.assertIsNone(f.b._get("v2_collaboration", "shared-1")["terms"])
        self.assertNotIn(recovery["message_id"], [m["message_id"] for m in f.b.inbox(protocol.OWNER_B)["messages"]])
        self.assertEqual(f.b.collaborations(protocol.OWNER_B)["collaborations"], [])
        self.assertEqual(f.b.prepare_collaboration("task-b", "shared-1", "not-yet", "accept", {}, protocol.OWNER_B)["decision"], "deny")
        for item in pending:
            f.b.review_preview(item["message_id"], protocol.OWNER_B)
            f.b.review_peer(item["message_id"], "approve", protocol.OWNER_B)
        self.assertIsNotNone(f.b.collaborations(protocol.OWNER_B)["collaborations"][0]["terms"])

    def test_block_denies_v2_queue_approval_and_worker_after_unblock(self):
        f = self.f
        f.send(f.a, "proposal", protocol.proposal())
        f.transfer(f.b)
        policy = {"collaboration_id": "shared-1", "allow_propose": False, "allow_accept": True,
            "proposal": None, "max_runs": 40, "max_sends": 24, "interval_seconds": 15, "expires_at": "2026-10-06T00:00:00Z"}
        protocol.approve(f.b, f.b.prepare_worker_policy("task-b", policy, protocol.OWNER_B), protocol.OWNER_B)
        op = f.b.prepare_collaboration("task-b", "shared-1", "queued-accept", "accept", {}, protocol.OWNER_B)
        f.b.set_peer_block(protocol.ALICE, True, protocol.OWNER_B)
        before = copy.deepcopy(f.bus.sent)
        self.assertEqual(f.b.dispatch(op["operation_id"], protocol.OWNER_B, f.tb)["decision"], "deny")
        run_worker_tick(f.b, "profile-b", f.tb)
        f.b.set_peer_block(protocol.ALICE, False, protocol.OWNER_B)
        self.assertEqual(f.b.dispatch(op["operation_id"], protocol.OWNER_B, f.tb)["decision"], "deny")
        self.assertEqual(f.bus.sent, before)
        self.assertEqual(f.b._get("worker_policy", "task-b")["status"], "paused")

    def test_pre_upgrade_applied_peer_terms_are_hidden_until_fresh_owner_review(self):
        f = self.f
        f.send(f.a, "proposal", protocol.proposal())
        f.transfer(f.b)
        with f.b._transaction():
            f.b._db.execute("DELETE FROM collaboration_records WHERE kind IN ('peer_review','peer_review_preview')")
        result = f.b.state(protocol.OWNER_B)
        self.assertTrue(result["pending_review"])
        self.assertEqual(result["collaboration"]["collaborations"], [])
        self.assertEqual(result["pending_confirmations"], [])
        self.assertNotIn('"terms": {', json.dumps(result))


class RemotePeerSafetyTests(unittest.TestCase):
    def setUp(self):
        self.f = remote.TestRemote()
        self.f.setUp()
        self.addCleanup(self.f.tearDown)

    def test_old_conversation_host_cannot_claim_new_model_admission_safety(self):
        f = self.f
        old_host = remote.RemoteBridge(f.home / "old-host.sqlite3", f.store, remote.AGENT, clock=lambda:f.now, conversations=True)
        self.addCleanup(old_host.close)
        pairing = old_host.pair(remote.CONSOLE,"owner-a",["capabilities"],remote.stamp(remote.NOW+1000))
        self.assertNotIn("peer_content_safety",old_host._invoke({"method":"capabilities","params":{}},pairing))

    def test_unsafe_host_refuses_new_replayed_and_queued_model_turns_but_allows_reads(self):
        f = self.f
        wire, result = f.submit(remote.request("safe-turn", "conversation.send", {"text":"owner private prompt"}))
        self.assertEqual(f.result(result)["result"]["status"],"submitted")
        f.bridge.peer_content_safety_enabled = False
        _, denied = f.submit(remote.request("new-unsafe-turn","conversation.send",{"text":"new prompt"}))
        self.assertEqual(f.result(denied)["error"]["code"],"peer_content_safety_required")
        replay = f.bridge.process(wire,f.agent)
        self.assertEqual(f.result(replay)["error"]["code"],"peer_content_safety_changed")
        self.assertIsNone(f.bridge.claim_turn())
        self.assertEqual(f.bridge._all("turn")[0]["status"],"failed")
        self.assertEqual(len(f.bridge._all("turn")),1)
        _, read = f.submit(remote.request("old-read","conversation.get",{"conversation_id":"safe-turn"}))
        self.assertNotIn("error",f.result(read))

    def test_unsafe_replay_does_not_claim_completed_turn_was_unexecuted(self):
        f = self.f
        wire, result = f.submit(remote.request("completed-safe-turn", "conversation.send", {"text":"owner private prompt"}))
        job = f.bridge.claim_turn()
        self.assertEqual(job["turn_id"],f.result(result)["result"]["turn_id"])
        f.bridge.finish_turn(job["turn_id"],response="previously completed response")
        f.bridge.peer_content_safety_enabled = False
        replay = f.bridge.process(wire,f.agent)
        self.assertEqual(f.result(replay)["error"]["code"],"peer_content_safety_changed")
        self.assertEqual(f.bridge._all("turn")[0]["status"],"completed")
        _, read = f.submit(remote.request("completed-read","conversation.get",{"conversation_id":"completed-safe-turn"}))
        self.assertIn("previously completed response",json.dumps(f.result(read)))

    def test_legacy_pairing_does_not_gain_new_owner_methods(self):
        f = self.f
        f.bridge.pair(remote.CONSOLE, "owner-a", ["capabilities", "contacts.list", "inbox.list"], remote.stamp(remote.NOW + 2000))
        _, reply = f.submit(remote.request("old-caps"))
        methods = {m["name"]:m["available"] for m in f.result(reply)["result"]["methods"]}
        self.assertEqual(f.result(reply)["result"]["peer_content_safety"],
            {"version":1, "mode":"owner_review", "automatic_peer_model_execution":False})
        for method in ("contacts.block", "contacts.unblock", "inbox.review_preview", "inbox.review"):
            self.assertFalse(methods[method])
            _, reply = f.submit(remote.request(method.replace(".", "-"), method, {"urn":social.A} if "contacts" in method else {"message_id":"none", **({"decision":"approve"} if method == "inbox.review" else {})}))
            self.assertEqual(f.result(reply)["error"]["code"], "method_not_allowed")
        self.assertEqual(f.store.state("owner-a")["blocked_peers"], [])

    def test_strict_rpc_replay_and_separate_read_write_review_scopes(self):
        f = self.f
        f.allow_web_actions()
        params = {"urn": social.A}
        wire, first = f.submit(remote.request("owner-block", "contacts.block", params))
        self.assertEqual(f.result(first)["result"]["status"], "blocked")
        self.assertEqual(f.bridge.process(wire, f.agent), first)
        _, reply = f.submit(remote.request("bad-owner", "contacts.unblock", {**params,"owner_session":"other"}))
        self.assertEqual(f.result(reply)["error"]["code"], "invalid_params")
        self.assertTrue(f.store.state("owner-a")["blocked_peers"])
        f.bridge.pair(remote.CONSOLE, "owner-a", ["inbox.review"], remote.stamp(remote.NOW + 2000))
        _, reply = f.submit(remote.request("preview-denied", "inbox.review_preview", {"message_id":"none"}))
        self.assertEqual(f.result(reply)["error"]["code"], "method_not_allowed")

    def test_safety_revision_is_monotone_owner_scoped_and_rpc_replay_is_historical(self):
        f = self.f
        f.allow_web_actions()
        self.assertEqual(f.store.state("owner-a")["safety_revision"], 0)
        wire, reply = f.submit(remote.request("block-revision", "contacts.block", {"urn": social.A}))
        self.assertEqual(f.result(reply)["result"]["safety_revision"], 1)
        self.assertEqual(f.store.set_peer_block(social.A, True, "owner-a")["safety_revision"], 1)
        f.store.set_peer_block(social.A, False, "owner-a")
        self.assertEqual(f.store.state("owner-a")["safety_revision"], 2)
        self.assertEqual(f.store.state("owner-b")["safety_revision"], 0)
        self.assertEqual(f.result(f.bridge.process(wire, f.agent))["result"]["safety_revision"], 1)
        _, reply = f.submit(remote.request("latest-contacts", "contacts.list"))
        self.assertEqual(f.result(reply)["result"]["safety_revision"], 2)
        self.assertEqual(f.result(reply)["result"]["blocked_peers"], [])
