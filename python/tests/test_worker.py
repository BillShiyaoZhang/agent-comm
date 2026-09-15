"""Two owners, real SQLite and a deterministic helper bus; never a model or live send."""
from contextlib import contextmanager
from unittest.mock import patch
import unittest

import test_collaboration_v2 as fixtures
from agent_comm_runtime.worker import run_worker_tick, TaskRunContext


class WorkerTests(unittest.TestCase):
    def setUp(self):
        self.f = fixtures.CollaborationV2Tests()
        self.f.setUp()
        self.addCleanup(self.f.doCleanups)
        self.f.joined()

    def policy(self, side="a", **changes):
        return {"collaboration_id": "shared-1", "allow_propose": side == "a", "allow_accept": True,
                "proposal": fixtures.proposal() if side == "a" else None,
                "max_runs": 40, "max_sends": 24, "interval_seconds": 15,
                "expires_at": "2026-10-06T00:00:00Z", **changes}

    def enable(self, side="a", *, approve=True, **changes):
        store, owner, task = ((self.f.a, fixtures.OWNER_A, "task-a") if side == "a" else (self.f.b, fixtures.OWNER_B, "task-b"))
        request = store.prepare_worker_policy(task, self.policy(side, **changes), owner)
        if approve:
            self.assertEqual(fixtures.approve(store, request, owner)["decision"], "allow")
        return request

    def tick(self, side="a"):
        store, owner, transport = ((self.f.a, fixtures.OWNER_A, self.f.ta) if side == "a" else (self.f.b, fixtures.OWNER_B, self.f.tb))
        return run_worker_tick(store, owner.split("|")[0], transport)

    def advance(self):
        self.f.now += 16

    def test_native_policy_is_required_and_full_meeting_closes_without_model_or_confirmation_calls(self):
        pending = self.enable(approve=False)
        before = len(self.f.bus.sent)
        self.assertEqual(self.tick()["results"], [])
        self.assertEqual(len(self.f.bus.sent), before)
        self.assertEqual(self.f.a.state(fixtures.OWNER_A)["tasks"][0]["worker"]["status"], "pending")
        fixtures.approve(self.f.a, pending, fixtures.OWNER_A)
        self.enable("b")
        with patch.object(self.f.a, "begin_confirmation", side_effect=AssertionError("background cannot confirm")), \
                patch.object(self.f.b, "begin_confirmation", side_effect=AssertionError("background cannot confirm")):
            for _ in range(24):
                self.tick("a")
                self.f.transfer(self.f.b)
                self.tick("b")
                self.f.transfer(self.f.a)
                self.advance()
        for store, owner in ((self.f.a, fixtures.OWNER_A), (self.f.b, fixtures.OWNER_B)):
            c = store.collaborations(owner)["collaborations"][0]
            self.assertEqual(c["phase"], "closed", c)
            view = store.state(owner)["tasks"][0]["worker"]
            self.assertLessEqual(view["runs_used"], 40)
            self.assertLessEqual(view["sends_used"], 24)
            self.assertEqual(view["waiting_reason"], "collaboration_complete")
        wire = [__import__('json').loads(body['text'])['kind'] for body in list(self.f.bus.sent.values())[before:]]
        self.assertTrue(set(wire) <= {'proposal', 'accept', 'agreement', 'agreement_ack', 'receipt', 'sync_request', 'sync_response'})
        self.assertIn('proposal', wire)
        self.assertEqual(wire.count('accept'), 2)

    def test_out_of_scope_peer_terms_pause_and_create_owner_attention_without_sending_acceptance(self):
        self.enable("b")
        # A's separately approved one-time longer proposal is outside B's mandate.
        self.f.send(self.f.a, "proposal", fixtures.proposal(end="2026-10-03T10:00:00Z"))
        self.f.transfer(self.f.b)
        # Drain existing fixed receipt before the next deterministic business step.
        self.tick("b")
        self.advance()
        before = len(self.f.bus.sent)
        self.tick("b")
        state = self.f.b.state(fixtures.OWNER_B)
        self.assertEqual(state["tasks"][0]["worker"]["status"], "paused")
        self.assertEqual(state["tasks"][0]["worker"]["waiting_reason"], "exception_requires_owner")
        self.assertEqual(len(self.f.bus.sent), before)
        self.assertTrue(any(a["kind"] == "collaboration_v2" for a in state["pending_confirmations"]))
        self.assertTrue(any(i["kind"] == "owner_decision_required" and i["state"] == "open" for i in self.f.b.attention(fixtures.OWNER_B)["items"]))

    def test_run_and_send_budgets_are_separate_and_never_reset_by_tick(self):
        self.enable(max_runs=1, max_sends=1)
        self.tick()
        self.advance()
        before = len(self.f.bus.sent)
        self.tick()
        self.assertEqual(len(self.f.bus.sent), before)
        state = self.f.a.state(fixtures.OWNER_A)["tasks"][0]["worker"]
        self.assertEqual((state["runs_used"], state["sends_used"]), (1, 1))
        self.assertEqual(state["waiting_reason"], "budget_exhausted")
        self.assertTrue(any(i["kind"] == "needs_recovery" and i["state"] == "open" for i in self.f.a.attention(fixtures.OWNER_A)["items"]))

    def test_pause_revoke_and_changed_policy_require_new_native_consent(self):
        self.enable()
        self.f.a.pause_worker("task-a", fixtures.OWNER_A)
        before = len(self.f.bus.sent)
        self.tick()
        self.assertEqual(len(self.f.bus.sent), before)
        newer = self.enable(approve=False, max_runs=20)
        self.tick()
        self.assertEqual(len(self.f.bus.sent), before)
        self.f.a.pause_worker("task-a", fixtures.OWNER_A, revoke=True)
        with self.assertRaises(ValueError):
            self.f.a.begin_confirmation(newer["approval_id"], fixtures.OWNER_A)
        with self.assertRaises(ValueError):
            self.f.a.pause_worker("task-a", fixtures.OWNER_B)

    def test_unknown_send_is_not_automatically_retried_after_restart_or_later_tick(self):
        self.enable()
        self.f.bus.fail_ack_once = True
        self.tick()
        first = len(self.f.bus.sent)
        state = self.f.a.state(fixtures.OWNER_A)["tasks"][0]["worker"]
        self.assertEqual(state["status"], "paused")
        self.assertEqual(state["waiting_reason"], "send_uncertain")
        self.advance()
        self.tick()
        self.assertEqual(len(self.f.bus.sent), first)
        self.assertTrue(any(i["kind"] == "needs_recovery" and i["state"] == "open" for i in self.f.a.attention(fixtures.OWNER_A)["items"]))

    def test_revocation_between_reservation_and_send_prevents_external_effect(self):
        self.enable()
        original = self.f.a._transaction
        stopped = False
        @contextmanager
        def revoke_after_reservation():
            nonlocal stopped
            with original():
                yield
            if not stopped and any(o["operation_id"].startswith("worker-op-") and o["status"] == "sending" for o in self.f.a._all("v2_operation")):
                stopped = True
                self.f.a.pause_worker("task-a", fixtures.OWNER_A, revoke=True)
        before = len(self.f.bus.sent)
        with patch.object(self.f.a, "_transaction", revoke_after_reservation):
            self.tick()
        self.assertTrue(stopped)
        self.assertEqual(len(self.f.bus.sent), before)
        self.assertEqual(self.f.a.state(fixtures.OWNER_A)["tasks"][0]["worker"]["status"], "revoked")

    def test_expiry_and_task_revocation_stop_worker_and_forged_run_context_is_rejected(self):
        self.enable(expires_at="2026-10-01T00:00:30Z")
        self.f.now += 31
        before = len(self.f.bus.sent)
        self.tick()
        self.assertEqual(len(self.f.bus.sent), before)
        self.assertEqual(self.f.a.state(fixtures.OWNER_A)["tasks"][0]["worker"]["status"], "expired")
        fake = TaskRunContext("profile-a", "task-a", 1, 1, "never-issued")
        with self.assertRaises(ValueError):
            with self.f.a._transaction():
                self.f.a._check_worker_context(fake)
        self.enable()
        self.f.a.revoke("task-a", fixtures.OWNER_A)
        self.tick()
        self.assertEqual(len(self.f.bus.sent), before)

    def test_policy_cannot_expand_scope_or_choose_unbound_peer_or_use_invalid_budgets(self):
        for changes in ({"max_runs": True}, {"max_sends": 33}, {"interval_seconds": 1},
                        {"expires_at": "2099-01-01T00:00:00Z"}, {"proposal": fixtures.proposal(end="2026-10-03T11:00:00Z")},
                        {"collaboration_id": "unknown"}, {"allow_propose": False}):
            with self.subTest(changes=changes), self.assertRaises(ValueError):
                self.enable(approve=False, **changes)
        with self.assertRaises(ValueError):
            self.enable("b", allow_propose=True, proposal=fixtures.proposal("alice"))

    def test_expired_or_not_due_policy_does_not_starve_later_task_with_host_one_task_limit(self):
        self.enable()
        # These stale projections represent other previously native-approved tasks.
        # The valid policy remains the only task eligible for a worker run.
        from copy import deepcopy
        with self.f.a._transaction():
            task = deepcopy(self.f.a._get("task", "task-a"))
            policy = deepcopy(self.f.a._get("worker_policy", "task-a"))
            task.update(task_id="a-expired", status="revoked")
            policy.update(task_id="a-expired", last_run_at=None)
            self.f.a._put("task", "a-expired", task)
            self.f.a._put("worker_policy", "a-expired", policy)
            task = deepcopy(self.f.a._get("task", "task-a"))
            policy = deepcopy(self.f.a._get("worker_policy", "task-a"))
            task["task_id"] = "b-not-due"
            policy.update(task_id="b-not-due", last_run_at=self.f.now)
            self.f.a._put("task", "b-not-due", task)
            self.f.a._put("worker_policy", "b-not-due", policy)
        before = len(self.f.bus.sent)
        result = run_worker_tick(self.f.a, fixtures.OWNER_A.split("|")[0], self.f.ta, max_tasks=1)
        self.assertEqual([r["task_id"] for r in result["results"]], ["task-a"])
        self.assertGreater(len(self.f.bus.sent), before)


if __name__ == '__main__':
    unittest.main()
