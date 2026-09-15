"""Two independent owners over the actual helper message shape, without models."""
import copy
from datetime import datetime, timezone
import json
from pathlib import Path
import tempfile
import unittest

from agent_comm_runtime.store import Store
from agent_comm_runtime.collaboration_v2 import PROTOCOL, canonical, digest, timestamp


NOW = datetime(2026, 10, 1, tzinfo=timezone.utc).timestamp()
ALICE = "urn:agent-comm:agent:alice"
BOB = "urn:agent-comm:agent:bob"
OWNER_A = "profile-a|native-1"
OWNER_B = "profile-b|native-2"


def scope(peer, **changes):
    return {"purpose": "仅协调一次会议方案", "topic": "协作协议",
            "capabilities": ["propose_meeting", "accept_meeting", "share_slots"],
            "recipient_ids": [peer], "participant_ids": ["self", peer], "resource_ids": [],
            "window_start": "2026-10-02T00:00:00Z", "window_end": "2026-10-05T00:00:00Z",
            "max_duration_minutes": 30, "max_candidates": 2, "max_actions": 50,
            "expires_at": "2026-10-06T00:00:00Z", **changes}


def proposal(peer="bob", version=1, **changes):
    return {"proposal_id": "meeting", "version": version, "topic": "协作协议",
            "participant_ids": ["self", peer], "start": "2026-10-03T09:00:00Z",
            "end": "2026-10-03T09:30:00Z", **changes}


def approve(store, request, owner, answer="可以"):
    lease = store.begin_confirmation(request["approval_id"], owner)
    return store.finish_confirmation(request["approval_id"], lease["token"], owner, answer)


class Bus:
    def __init__(self):
        self.pending = {ALICE: {}, BOB: {}}
        self.sent = {}
        self.fail_ack_once = False

    def transport(self, sender):
        bus = self

        class Transport:
            def store(self, body):
                assert set(body) == {"message_id", "recipient_urn", "text", "task_id", "conversation_id", "kind", "deadline", "hop_limit"}
                key = (sender, body["message_id"])
                if key in bus.sent and bus.sent[key] != body:
                    raise ValueError("Message ID content changed")
                if key not in bus.sent:
                    bus.sent[key] = copy.deepcopy(body)
                    # The helper supplies sender_urn. Peer payloads cannot do so.
                    bus.pending[body["recipient_urn"]][body["message_id"]] = {**copy.deepcopy(body), "sender_urn": sender}
                if bus.fail_ack_once:
                    bus.fail_ack_once = False
                    raise OSError("simulated lost local acceptance response")
                return {"success": True, "message_id": body["message_id"]}

            def retrieve(self):
                return list(bus.pending[sender].values())

            def ack(self, ids):
                for key in ids:
                    bus.pending[sender].pop(key, None)
                return {"success": True}

        return Transport()


class CollaborationV2Tests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.now = NOW
        self.a = Store(Path(self.temp.name) / "a.sqlite3", clock=lambda: self.now, local_urn=ALICE)
        self.b = Store(Path(self.temp.name) / "b.sqlite3", clock=lambda: self.now, local_urn=BOB)
        self.addCleanup(self.a.close)
        self.addCleanup(self.b.close)
        approve(self.a, self.a.prepare_contact("bob", ["私密称呼甲"], BOB, OWNER_A), OWNER_A)
        approve(self.b, self.b.prepare_contact("alice", ["私密称呼乙"], ALICE, OWNER_B), OWNER_B)
        approve(self.a, self.a.prepare_task("task-a", scope("bob"), OWNER_A), OWNER_A)
        approve(self.b, self.b.prepare_task("task-b", scope("alice"), OWNER_B), OWNER_B)
        self.bus = Bus()
        self.ta, self.tb = self.bus.transport(ALICE), self.bus.transport(BOB)
        self.n = 0

    def send(self, store, kind, payload=None, *, confirm=True, operation_id=None):
        owner, task, transport = (OWNER_A, "task-a", self.ta) if store is self.a else (OWNER_B, "task-b", self.tb)
        self.n += 1
        op = operation_id or f"op-{self.n}"
        result = store.prepare_collaboration(task, "shared-1", op, kind, payload or {}, owner)
        if result["decision"] == "ask" and confirm:
            self.assertEqual(approve(store, result, owner)["decision"], "allow")
        if result["decision"] == "ask" and not confirm:
            return result
        self.assertNotEqual(result["decision"], "deny", result)
        dispatched = store.dispatch(op, owner, transport)
        self.assertEqual(dispatched["status"], "accepted", dispatched)
        return dispatched

    def transfer(self, recipient):
        transport = self.ta if recipient is self.a else self.tb
        messages = transport.retrieve()
        for message in messages:
            recipient.ingest_message(message)
        transport.ack([m["message_id"] for m in messages])
        return messages

    def pump(self):
        for _ in range(20):
            progress = False
            for store, owner, transport in ((self.a, OWNER_A, self.ta), (self.b, OWNER_B, self.tb)):
                for op in store.collaborations(owner)["operations"]:
                    if op["status"] in {"ready", "sending"} and op["decision"] == "allow":
                        store.dispatch(op["operation_id"], owner, transport)
                        progress = True
            for recipient in (self.a, self.b):
                if self.transfer(recipient):
                    progress = True
            if not progress:
                return
        self.fail("Protocol did not quiesce within bounded pumping")

    def joined(self):
        self.send(self.a, "invite", {"peer_id": "bob"})
        messages = self.transfer(self.b)
        self.assertEqual(self.b.collaborations(OWNER_B)["collaborations"], [])
        self.send(self.b, "join", {"message_id": messages[0]["message_id"]})
        self.transfer(self.a)
        self.pump()

    def terms(self):
        self.joined()
        self.send(self.a, "proposal", proposal())
        self.pump()

    def state(self, store):
        return store.collaborations(OWNER_A if store is self.a else OWNER_B)["collaborations"][0]

    def test_independent_tasks_same_terms_accept_and_agreement_only_completion(self):
        self.terms()
        self.assertEqual(self.state(self.a)["task_id"], "task-a")
        self.assertEqual(self.state(self.b)["task_id"], "task-b")
        self.assertEqual(self.state(self.a)["terms"], self.state(self.b)["terms"])
        self.send(self.a, "accept")
        self.pump()
        self.assertIsNone(self.state(self.a)["agreement"])
        self.send(self.b, "accept")
        self.pump()
        for store in (self.a, self.b):
            state = self.state(store)
            self.assertEqual(state["phase"], "closed")
            self.assertEqual(state["closure_reason"], "agreement_only_complete")
            self.assertTrue(state["agreement_synced"])
        self.assertEqual(self.state(self.a)["agreement"], self.state(self.b)["agreement"])
        self.assertFalse(self.a.collaborations(OWNER_A)["calendar_created"])
        for body in self.bus.sent.values():
            self.assertNotIn("私密称呼", body["text"])
            self.assertNotIn("profile-a", body["text"])

    def test_native_invite_consent_is_exact_finite_and_peer_cannot_grant_it(self):
        pending = self.send(self.a, "invite", {"peer_id": "bob"}, confirm=False)
        self.assertEqual(pending["decision"], "ask")
        stored = self.a._get("approval", pending["approval_id"])
        self.assertIn("32", stored["question"])
        self.assertIn("独立于业务委托", stored["question"])
        with self.assertRaises(ValueError):
            self.a.begin_confirmation(pending["approval_id"], OWNER_B)
        self.assertEqual(approve(self.a, pending, OWNER_A, "如果不泄露隐私就可以")["decision"], "clarify")
        self.assertEqual(self.a.dispatch(pending["operation_id"], OWNER_A, self.ta)["decision"], "deny")
        approve(self.a, pending, OWNER_A, "拒绝")
        self.assertEqual(self.a.collaborations(OWNER_A)["operations"][0]["status"], "denied")

    def test_invitation_join_cannot_select_remote_owner_or_task(self):
        self.send(self.a, "invite", {"peer_id": "bob"})
        message = self.transfer(self.b)[0]
        with self.assertRaises(ValueError):
            self.b.prepare_collaboration("task-b", "shared-1", "bad-join", "join",
                                         {"message_id": message["message_id"], "owner_id": OWNER_A}, OWNER_B)
        with self.assertRaises(ValueError):
            self.b.prepare_collaboration("task-a", "shared-1", "bad-task", "join", {"message_id": message["message_id"]}, OWNER_B)
        self.assertEqual(self.b.collaborations(OWNER_A)["invitations"], [])

    def test_proposal_scope_and_accept_scope_are_separate(self):
        self.terms()
        with self.b._transaction():
            task = self.b._get("task", "task-b")
            task["scope"]["capabilities"] = ["propose_meeting"]
            self.b._put("task", "task-b", task)
        pending = self.send(self.b, "accept", confirm=False)
        self.assertEqual(pending["decision"], "ask")
        self.assertIn("scope_exceeded", pending["reasons"])
        self.assertIsNone(self.state(self.b)["agreement"])
        with self.assertRaises(ValueError):
            self.b.prepare_collaboration("task-b", "shared-1", "not-a-proposal", "accept", {"approved": True}, OWNER_B)

    def test_changed_terms_invalidate_pending_native_acceptance(self):
        self.terms()
        with self.b._transaction():
            task = self.b._get("task", "task-b")
            task["scope"]["capabilities"] = ["propose_meeting"]
            self.b._put("task", "task-b", task)
        pending = self.send(self.b, "accept", confirm=False)
        self.send(self.a, "proposal", proposal(version=2, start="2026-10-03T10:00:00Z", end="2026-10-03T10:30:00Z"))
        self.pump()
        with self.assertRaises(ValueError):
            approve(self.b, pending, OWNER_B)

    def test_withdraw_before_second_accept_prevents_formation(self):
        self.terms()
        self.send(self.a, "accept")
        self.pump()
        self.send(self.a, "withdraw")
        self.pump()
        self.send(self.b, "accept")
        self.pump()
        self.assertIsNone(self.state(self.a)["agreement"])
        self.assertFalse(self.state(self.a)["acceptances"][ALICE]["active"])

    def test_formation_before_withdraw_preserves_commitment_until_cancel_ack(self):
        self.terms()
        self.send(self.a, "accept")
        self.pump()
        self.send(self.b, "accept")
        self.transfer(self.a)  # A commits formation before B's withdrawal arrives.
        agreement = copy.deepcopy(self.state(self.a)["agreement"])
        self.assertIsNotNone(agreement)
        self.send(self.b, "withdraw")
        self.pump()
        self.assertEqual(self.state(self.a)["agreement"], agreement)
        self.assertEqual(self.state(self.b)["agreement"], agreement)
        self.send(self.b, "cancel_request")
        self.pump()
        self.assertEqual(self.state(self.a)["waiting_reason"], "cancel_decision")
        self.send(self.a, "cancel_ack")
        self.pump()
        self.assertEqual(self.state(self.a)["closure_reason"], "cancelled")
        self.assertEqual(self.state(self.b)["closure_reason"], "cancelled")

    def test_lost_helper_ack_reuses_event_wire_bytes_and_task_budget(self):
        self.terms()
        result = self.a.prepare_collaboration("task-a", "shared-1", "retry-accept", "accept", {}, OWNER_A)
        self.assertEqual(result["decision"], "allow")
        before = self.a._get("task", "task-a")["used_count"]
        self.bus.fail_ack_once = True
        result = self.a.dispatch("retry-accept", OWNER_A, self.ta)
        self.assertEqual(result["status"], "sending")
        self.assertEqual(result["deliveries"][0]["status"], "uncertain")
        self.assertEqual(self.a.dispatch("retry-accept", OWNER_A, self.ta)["status"], "accepted")
        self.assertEqual(self.a._get("task", "task-a")["used_count"], before + 1)

    def test_duplicate_after_expiry_keeps_historical_applied_result(self):
        self.terms()
        self.send(self.a, "accept")
        message = self.transfer(self.b)[0]
        packet = json.loads(message["text"])
        self.now += 20 * 86400
        with self.b._transaction():
            result = self.b.ingest_v2_record(message)
        self.assertEqual(result["status"], "applied")
        record = self.b._v2_record(self.state(self.b), ALICE, packet["event_id"])
        self.assertEqual(record["status"], "applied")

    def test_event_id_and_sender_identity_conflicts_fail_closed(self):
        self.terms()
        self.send(self.a, "accept")
        message = self.transfer(self.b)[0]
        packet = json.loads(message["text"])
        packet["payload"]["authority_basis"] = "authority_verified"
        packet["payload_digest"] = digest(packet["payload"])
        with self.assertRaises(ValueError):
            with self.b._transaction():
                self.b.ingest_v2_record({**message, "text": canonical(packet)})
        with self.assertRaises(ValueError):
            with self.b._transaction():
                self.b.ingest_v2_record({**message, "sender_urn": "urn:agent-comm:agent:attacker"})

    def test_revoke_stops_business_but_finite_approved_maintenance_can_sync(self):
        self.terms()
        ready = self.a.prepare_collaboration("task-a", "shared-1", "revoked-accept", "accept", {}, OWNER_A)
        self.assertEqual(ready["decision"], "allow")
        self.a.revoke("task-a", OWNER_A)
        self.assertEqual(self.a.dispatch("revoked-accept", OWNER_A, self.ta)["decision"], "deny")
        self.send(self.a, "sync_request")
        self.pump()
        self.now += 8 * 86400
        result = self.a.prepare_collaboration("task-a", "shared-1", "expired-sync", "sync_request", {}, OWNER_A)
        self.assertEqual(result["decision"], "deny")

    def test_shared_budget_with_v1_cannot_be_spent_twice(self):
        self.terms()
        ready = self.a.prepare_collaboration("task-a", "shared-1", "last-accept", "accept", {}, OWNER_A)
        self.assertEqual(ready["decision"], "allow")
        with self.a._transaction():
            task = self.a._get("task", "task-a")
            task["used_count"] = task["scope"]["max_actions"]
            self.a._put("task", "task-a", task)
        self.assertEqual(self.a.dispatch("last-accept", OWNER_A, self.ta)["decision"], "deny")

    def test_out_of_order_accept_waits_for_proposal_then_applies_once(self):
        self.joined()
        self.send(self.a, "proposal", proposal())
        self.send(self.a, "accept")
        messages = self.tb.retrieve()
        original = {json.loads(m["text"])["kind"]: m for m in messages}
        self.b.ingest_message(original["accept"])
        packet = json.loads(original["accept"]["text"])
        self.assertEqual(self.b._v2_record(self.state(self.b), ALICE, packet["event_id"])["status"], "buffered")
        self.assertIsNone(self.state(self.b)["terms"])
        self.b.ingest_message(original["proposal"])
        self.assertEqual(self.b._v2_record(self.state(self.b), ALICE, packet["event_id"])["status"], "applied")
        self.assertEqual(self.state(self.b)["acceptances"][ALICE]["event_id"], packet["event_id"])

    def test_recovery_is_out_of_band_and_repairs_a_lost_predecessor(self):
        self.joined()
        self.send(self.a, "proposal", proposal())
        self.send(self.a, "accept")
        messages = self.tb.retrieve()
        proposal_message = next(m for m in messages if json.loads(m["text"])["kind"] == "proposal")
        accept_message = next(m for m in messages if json.loads(m["text"])["kind"] == "accept")
        self.b.ingest_message(accept_message)
        self.tb.ack([m["message_id"] for m in messages])  # Simulate missing/retrieved-away old predecessor.
        self.send(self.b, "sync_request")
        self.pump()
        self.assertIsNotNone(self.state(self.b)["terms"])
        self.assertIn(ALICE, self.state(self.b)["acceptances"])
        sync_packets = [json.loads(body["text"]) for body in self.bus.sent.values()
                        if json.loads(body["text"])["kind"] in {"sync_request", "sync_response"}]
        self.assertTrue(sync_packets)
        self.assertTrue(all(p["sender_sequence"] == 0 and p["previous_event_id"] is None for p in sync_packets))

    def test_accept_expiring_while_buffered_cannot_form_agreement(self):
        self.joined()
        self.send(self.a, "proposal", proposal())
        self.send(self.a, "accept")
        messages = self.tb.retrieve()
        by_kind = {json.loads(m["text"])["kind"]: m for m in messages}
        self.b.ingest_message(by_kind["accept"])
        self.now += 6 * 86400
        self.b.ingest_message(by_kind["proposal"])
        acceptance = json.loads(by_kind["accept"]["text"])
        record = self.b._v2_record(self.state(self.b), ALICE, acceptance["event_id"])
        self.assertEqual(record["status"], "rejected")
        self.assertNotIn(ALICE, self.state(self.b)["acceptances"])

    def agreement_in_flight(self):
        self.terms()
        self.send(self.a, "accept")
        self.pump()
        self.send(self.b, "accept")
        self.transfer(self.a)
        for op in self.a.collaborations(OWNER_A)["operations"]:
            if op["kind"] == "agreement" and op["status"] == "ready":
                self.a.dispatch(op["operation_id"], OWNER_A, self.ta)
        return next(m for m in self.tb.retrieve() if json.loads(m["text"])["kind"] == "agreement")

    def test_rejected_acceptance_record_cannot_be_used_as_agreement_evidence(self):
        message = self.agreement_in_flight()
        packet = json.loads(message["text"])
        ref = next(r for r in packet["payload"]["acceptances"] if r["actor_urn"] == ALICE)
        with self.b._transaction():
            key = self.b._v2_key("shared-1", ALICE, ref["event_id"])
            record = self.b._get("v2_event", key)
            record.update(status="rejected", reason="expired_event")
            self.b._put("v2_event", key, record)
        self.b.ingest_message(message)
        self.assertIsNone(self.state(self.b)["agreement"])
        record = self.b._v2_record(self.state(self.b), ALICE, packet["event_id"])
        self.assertEqual(record["status"], "rejected")

    def test_formation_cursor_including_prior_withdrawal_is_rejected(self):
        message = self.agreement_in_flight()
        self.send(self.b, "withdraw")
        packet = json.loads(message["text"])
        packet["payload"]["peer_sequence"] = self.state(self.b)["out_sequence"]
        packet["payload_digest"] = digest(packet["payload"])
        self.b.ingest_message({**message, "text": canonical(packet)})
        self.assertIsNone(self.state(self.b)["agreement"])
        self.assertEqual(self.b._v2_record(self.state(self.b), ALICE, packet["event_id"])["status"], "rejected")

    def test_agreement_ack_queued_is_not_claimed_as_peer_synchronized(self):
        message = self.agreement_in_flight()
        self.b.ingest_message(message)
        for op in self.b.collaborations(OWNER_B)["operations"]:
            if op["kind"] == "agreement_ack" and op["status"] == "ready":
                self.b.dispatch(op["operation_id"], OWNER_B, self.tb)
        self.assertFalse(self.state(self.b)["agreement_synced"])
        self.assertEqual(self.state(self.b)["waiting_reason"], "agreement_ack_delivery")
        self.assertFalse(self.state(self.a)["agreement_synced"])
        self.pump()
        self.assertTrue(self.state(self.a)["agreement_synced"])
        self.assertTrue(self.state(self.b)["agreement_synced"])

    def test_coordinator_cannot_acknowledge_for_peer(self):
        self.agreement_in_flight()
        with self.assertRaises(ValueError):
            self.a.prepare_collaboration("task-a", "shared-1", "fake-ack", "agreement_ack", {}, OWNER_A)

    def test_invalid_recovery_rolls_back_prior_records_and_outbox_effects(self):
        self.terms()
        self.send(self.a, "accept")
        message = self.tb.retrieve()[0]
        original = json.loads(message["text"])
        conflict = {**copy.deepcopy(original), "event_id": "same-sequence-different-event"}
        payload = {"events": [original, conflict]}
        packet = {"protocol": PROTOCOL, "collaboration_id": "shared-1", "event_id": "invalid-recovery",
                  "kind": "sync_response", "sender_urn": ALICE, "recipient_urn": BOB,
                  "sender_sequence": 0, "previous_event_id": None,
                  "created_at": timestamp(self.now), "expires_at": timestamp(self.now + 600),
                  "payload": payload, "payload_digest": digest(payload)}
        self.b.ingest_message({"message_id": "invalid-recovery-wire", "sender_urn": ALICE,
                               "task_id": "shared-1", "text": canonical(packet)})
        self.assertIsNone(self.b._v2_record(self.state(self.b), ALICE, original["event_id"]))
        self.assertEqual(self.b._v2_record(self.state(self.b), ALICE, packet["event_id"])["status"], "rejected")

    def test_late_first_agreement_recovers_already_valid_acceptance_history(self):
        message = self.agreement_in_flight()
        self.now += 6 * 86400
        self.b.ingest_message(message)
        self.assertIsNotNone(self.state(self.b)["agreement"])
        with self.assertRaises(ValueError):
            self.b.prepare_collaboration("task-b", "shared-1", "late-new-accept", "accept", {}, OWNER_B)

    def test_restart_resumes_uncertain_send_without_duplicate_acceptance_or_budget(self):
        self.terms()
        self.a.prepare_collaboration("task-a", "shared-1", "restart-accept", "accept", {}, OWNER_A)
        before = self.a._get("task", "task-a")["used_count"]
        self.bus.fail_ack_once = True
        self.assertEqual(self.a.dispatch("restart-accept", OWNER_A, self.ta)["status"], "sending")
        self.a.close()
        self.a = Store(Path(self.temp.name) / "a.sqlite3", clock=lambda: self.now, local_urn=ALICE)
        self.addCleanup(self.a.close)
        self.assertEqual(self.a.dispatch("restart-accept", OWNER_A, self.ta)["status"], "accepted")
        self.assertEqual(self.a._get("task", "task-a")["used_count"], before + 1)
        events = [r for r in self.a._all("v2_event") if r["packet"]["sender_urn"] == ALICE and r["packet"]["kind"] == "accept"]
        self.assertEqual(len(events), 1)
        self.pump()
        self.send(self.b, "accept")
        self.pump()
        self.assertEqual(self.state(self.a)["agreement"], self.state(self.b)["agreement"])

    def test_fixed_maintenance_budget_is_exhaustible_and_cannot_be_refreshed_by_peer(self):
        self.terms()
        with self.a._transaction():
            c = self.a._get("v2_collaboration", "shared-1")
            c["maintenance"]["used"] = c["maintenance"]["max_events"]
            self.a._put("v2_collaboration", "shared-1", c)
        denied = self.a.prepare_collaboration("task-a", "shared-1", "exhausted-sync", "sync_request", {}, OWNER_A)
        self.assertEqual(denied["decision"], "deny")
        with self.assertRaises(ValueError):
            self.a.prepare_collaboration("task-a", "shared-1", "refresh-maintenance", "invite", {"peer_id": "bob"}, OWNER_A)

    def test_revoked_grant_can_authorize_only_one_explicit_recovery_event(self):
        self.terms()
        self.send(self.a, "accept")
        self.pump()
        self.a.revoke("task-a", OWNER_A)
        before = self.a._get("task", "task-a")["used_count"]
        pending = self.send(self.a, "withdraw", confirm=False, operation_id="recovery-withdraw")
        self.assertIn("explicit_one_event_recovery", pending["reasons"])
        self.assertEqual(self.a.dispatch("recovery-withdraw", OWNER_A, self.ta)["decision"], "deny")
        approve(self.a, pending, OWNER_A)
        self.assertEqual(self.a.dispatch("recovery-withdraw", OWNER_A, self.ta)["status"], "accepted")
        self.assertEqual(self.a.dispatch("recovery-withdraw", OWNER_A, self.ta)["status"], "accepted")
        self.assertEqual(self.a._get("task", "task-a")["status"], "revoked")
        self.assertEqual(self.a._get("task", "task-a")["used_count"], before)
        self.pump()
        with self.assertRaises(ValueError):
            self.a.prepare_collaboration("task-a", "shared-1", "bad-new-proposal", "proposal", proposal(version=2), OWNER_B)
        denied = self.a.prepare_collaboration("task-a", "shared-1", "new-accept", "accept", {}, OWNER_A)
        self.assertEqual(denied["decision"], "deny")

    def test_expired_tasks_can_cancel_recorded_agreement_with_two_new_native_decisions(self):
        self.terms()
        self.send(self.a, "accept")
        self.pump()
        self.send(self.b, "accept")
        self.pump()
        self.now += 8 * 86400
        request = self.send(self.a, "cancel_request", confirm=False, operation_id="expired-cancel")
        self.assertIn("explicit_one_event_recovery", request["reasons"])
        approve(self.a, request, OWNER_A)
        self.a.dispatch("expired-cancel", OWNER_A, self.ta)
        self.transfer(self.b)
        response = self.send(self.b, "cancel_ack", confirm=False, operation_id="expired-cancel-ack")
        self.assertIn("explicit_one_event_recovery", response["reasons"])
        approve(self.b, response, OWNER_B)
        self.b.dispatch("expired-cancel-ack", OWNER_B, self.tb)
        self.transfer(self.a)
        self.assertEqual(self.state(self.a)["closure_reason"], "cancelled")
        self.assertEqual(self.state(self.b)["closure_reason"], "cancelled")

    def test_recovery_confirmation_expires_in_900_seconds_and_version_change_invalidates_it(self):
        self.terms()
        self.send(self.a, "accept")
        self.pump()
        self.a.revoke("task-a", OWNER_A)
        pending = self.send(self.a, "withdraw", confirm=False, operation_id="short-recovery")
        op = self.a._get("v2_operation", "short-recovery")
        self.assertEqual(op["expires_at"], self.now + 900)
        with self.a._transaction():
            task = self.a._get("task", "task-a")
            task["revision"] += 1
            self.a._put("task", "task-a", task)
        with self.assertRaises(ValueError):
            approve(self.a, pending, OWNER_A)
        later = self.send(self.a, "withdraw", confirm=False, operation_id="expiring-recovery")
        self.now += 901
        with self.assertRaises(ValueError):
            approve(self.a, later, OWNER_A)

    def test_owner_can_revoke_maintenance_and_cross_owner_cannot(self):
        self.terms()
        self.a.prepare_collaboration("task-a", "shared-1", "queued-sync", "sync_request", {}, OWNER_A)
        with self.assertRaises(ValueError):
            self.a.revoke_collaboration_maintenance("shared-1", OWNER_B)
        self.assertEqual(self.a.revoke_collaboration_maintenance("shared-1", OWNER_A)["status"], "revoked")
        self.assertEqual(self.a.dispatch("queued-sync", OWNER_A, self.ta)["decision"], "deny")
        self.assertEqual(self.a.prepare_collaboration("task-a", "shared-1", "new-sync", "sync_request", {}, OWNER_A)["decision"], "deny")
        # Revoking maintenance grants no extra business actions and does not
        # silently revoke the separate still-active business mandate either.
        self.assertEqual(self.a._get("task", "task-a")["status"], "active")


if __name__ == "__main__":
    unittest.main()
