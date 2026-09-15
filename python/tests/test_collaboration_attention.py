"""Protocol-to-attention integration using separate native principals."""
import unittest
import test_collaboration_v2 as fixtures


class CollaborationAttentionTests(unittest.TestCase):
    def setUp(self):
        self.flow = fixtures.CollaborationV2Tests()
        self.flow.setUp()
        self.addCleanup(self.flow.doCleanups)

    def test_invitation_resolves_only_on_binding_and_local_task_routes_correctly(self):
        f = self.flow
        f.send(f.a, "invite", {"peer_id": "bob"})
        message = f.transfer(f.b)[0]
        before = f.b.attention(fixtures.OWNER_B)
        invitation = next(i for i in before["items"] if i["kind"] == "new_collaboration_request")
        self.assertEqual(invitation["state"], "open")
        f.send(f.b, "join", {"message_id": message["message_id"]})
        after = f.b.attention(fixtures.OWNER_B, before["cursor"])
        resolved = next(i for i in after["items"] if i["attention_id"] == invitation["attention_id"])
        self.assertEqual(resolved["state"], "resolved")
        self.assertEqual(resolved["task_id"], "task-b")
        self.assertIn(message["message_id"], [m["message_id"] for m in f.b.inbox(fixtures.OWNER_B, "task-b")["messages"]])
        self.assertEqual(f.b.inbox(fixtures.OWNER_A)["messages"], [])

    def test_actionable_fact_then_matching_completion_without_receipt_noise(self):
        f = self.flow
        f.terms()
        before = f.b.attention(fixtures.OWNER_B)
        response = next(i for i in before["items"] if i["kind"] == "needs_response" and i["state"] == "open")
        self.assertEqual(response["target"], {"kind": "task", "id": "task-b"})
        f.send(f.a, "accept")
        f.pump()
        f.send(f.b, "accept")
        f.pump()
        complete = f.b.attention(fixtures.OWNER_B, before["cursor"])
        item = next(i for i in complete["items"] if i["attention_id"] == response["attention_id"])
        self.assertEqual(item["kind"], "collaboration_completed")
        self.assertIn("未创建日历", item["safe_summary"])
        f.pump()
        self.assertEqual(f.b.attention(fixtures.OWNER_B, complete["cursor"])["items"], [])
        self.assertEqual(f.b.attention(fixtures.OWNER_A)["items"], [])

    def test_uncertain_v2_send_uses_local_principal_and_resolves_on_exact_retry(self):
        f = self.flow
        f.terms()
        prepared = f.a.prepare_collaboration("task-a", "shared-1", "uncertain-attention", "accept", {}, fixtures.OWNER_A)
        self.assertEqual(prepared["decision"], "allow")
        f.bus.fail_ack_once = True
        result = f.a.dispatch("uncertain-attention", fixtures.OWNER_A, f.ta)
        self.assertEqual(result["status"], "sending")
        feed = f.a.attention(fixtures.OWNER_A)
        recovery = next(i for i in feed["items"] if i["kind"] == "needs_recovery")
        self.assertEqual(recovery["target"], {"kind": "task", "id": "task-a"})
        self.assertEqual(f.a.attention(fixtures.OWNER_B)["items"], [])
        f.a.dispatch("uncertain-attention", fixtures.OWNER_A, f.ta)
        changed = f.a.attention(fixtures.OWNER_A, feed["cursor"])
        self.assertEqual(next(i for i in changed["items"] if i["attention_id"] == recovery["attention_id"])["state"], "resolved")

    def test_v1_cannot_reuse_a_v2_operation_identifier(self):
        f = self.flow
        f.joined()
        f.a.prepare_collaboration("task-a", "shared-1", "reserved-both", "proposal", fixtures.proposal(), fixtures.OWNER_A)
        with self.assertRaises(ValueError):
            f.a.prepare_action("task-a", "reserved-both", {"capability": "share_slots", "recipient_ids": ["bob"],
                "payload": {"slots": [{"start": "2026-10-03T09:00:00Z", "end": "2026-10-03T09:30:00Z"}]}}, fixtures.OWNER_A)

    def test_maintenance_revocation_does_not_renotify_completed_agreement(self):
        f = self.flow
        f.terms()
        f.send(f.a, "accept")
        f.send(f.b, "accept")
        f.pump()
        before = f.a.attention(fixtures.OWNER_A)
        f.a.revoke_collaboration_maintenance("shared-1", fixtures.OWNER_A)
        self.assertEqual(f.a.attention(fixtures.OWNER_A, before["cursor"])["items"], [])

    def test_hard_crash_reservation_is_visible_without_inferred_send_success(self):
        f = self.flow
        f.terms()
        prepared = f.a.prepare_collaboration("task-a", "shared-1", "crash-window", "accept", {}, fixtures.OWNER_A)
        class Crash:
            def store(self, body):
                raise KeyboardInterrupt("process died before result commit")
        with self.assertRaises(KeyboardInterrupt):
            f.a.dispatch(prepared["operation_id"], fixtures.OWNER_A, Crash())
        before = f.a.attention(fixtures.OWNER_A)
        self.assertFalse(any(i["kind"] == "needs_recovery" for i in before["items"]))
        f.now += 31
        feed = f.a.attention(fixtures.OWNER_A, before["cursor"])
        item = next(i for i in feed["items"] if i["kind"] == "needs_recovery")
        self.assertEqual(item["state"], "open")
        self.assertEqual(f.a._get("v2_operation", "crash-window")["status"], "sending")
        f.a.revoke("task-a", fixtures.OWNER_A)
        self.assertEqual(f.a.dispatch("crash-window", fixtures.OWNER_A, f.ta)["decision"], "deny")
        self.assertEqual(f.state(f.a)["acceptances"], {})


if __name__ == "__main__":
    unittest.main()
