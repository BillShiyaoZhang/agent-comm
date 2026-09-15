"""Independent attention review regressions, using real collaboration flows."""
import unittest
import test_collaboration_v2 as fixtures


class AttentionReviewTests(unittest.TestCase):
    def setUp(self):
        self.f = fixtures.CollaborationV2Tests()
        self.f.setUp()
        self.addCleanup(self.f.doCleanups)

    def test_v2_without_optional_transport_task_id_retains_bound_owner_isolation(self):
        f = self.f
        f.terms()
        other = "profile-c|native-c"
        fixtures.approve(f.b, f.b.prepare_contact("alice-other-owner", ["Alice"], fixtures.ALICE, other), other)
        f.send(f.a, "proposal", fixtures.proposal(version=2, start="2026-10-03T10:00:00Z", end="2026-10-03T10:30:00Z"))
        message = f.tb.retrieve()[0]
        message.pop("task_id")  # v2 currently allows this optional helper routing field to be absent.
        f.b.ingest_message(message)
        self.assertEqual(f.b.inbox(other)["messages"], [])
        notices = f.b.attention(other)["items"]
        self.assertFalse(any(i["kind"] == "peer_message_received" for i in notices))

    def test_new_peer_cancel_is_actionable_after_business_grant_is_revoked(self):
        f = self.f
        f.terms()
        f.send(f.a, "accept")
        f.pump()
        f.send(f.b, "accept")
        f.pump()
        f.b.revoke("task-b", fixtures.OWNER_B)
        before = f.b.attention(fixtures.OWNER_B)["cursor"]
        f.send(f.a, "cancel_request")
        f.pump()
        notices = f.b.attention(fixtures.OWNER_B, before)["items"]
        response = next(i for i in notices if i["kind"] == "needs_response")
        self.assertEqual(response["state"], "open")
        pending = f.b.prepare_collaboration("task-b", "shared-1", "cancel-after-revoke", "cancel_ack", {}, fixtures.OWNER_B)
        self.assertEqual(pending["decision"], "ask")
        self.assertIn("explicit_one_event_recovery", pending["reasons"])


if __name__ == "__main__":
    unittest.main()
