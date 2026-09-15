"""Finite timer runs and actual Hermes native response router in isolated fixtures."""
from pathlib import Path
import sys
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[3] / "python" / "tests"))
import test_collaboration_v2 as fixtures
import test_collaboration_native as native_fixtures
from hermes_platform_agent_comm.collaboration import hermes, worker


class WorkerHostTests(unittest.TestCase):
    def test_native_policy_question_enables_timer_and_background_never_captures_native_context(self):
        bridge = native_fixtures.TestNativeBridge()
        bridge.setUp()
        self.addCleanup(bridge.doCleanups)
        principal = hermes.profile_principal()
        with patch.object(fixtures, "OWNER_A", principal + "|native-session"):
            flow = fixtures.CollaborationV2Tests()
            flow.setUp()
            self.addCleanup(flow.doCleanups)
            flow.joined()
            path = Path(flow.temp.name) / "a.sqlite3"
            bridge.settings.update(collaboration_state_path=str(path), urn=fixtures.ALICE)
            policy = {"collaboration_id": "shared-1", "allow_propose": True, "allow_accept": True,
                      "proposal": fixtures.proposal(), "max_runs": 8, "max_sends": 6,
                      "interval_seconds": 15, "expires_at": "2026-10-06T00:00:00Z"}
            with bridge.native():
                request = bridge.call("prepare_worker_policy", task_id="task-a", policy=policy)
                self.assertEqual(request["decision"], "ask", request)
                self.assertIn("运行次数", request["question"])
                before = len(flow.bus.sent)
                with patch.object(worker, "read_settings", return_value=bridge.settings), \
                     patch.object(worker, "profile_principal", return_value=principal), \
                     patch.object(worker, "HelperTransport", return_value=flow.ta):
                    self.assertEqual(worker.tick_profile_worker()["results"], [])
                    self.assertEqual(len(flow.bus.sent), before)
                    with bridge.answer() as events:
                        accepted = bridge.call("confirm", approval_id=request["approval_id"])
                    self.assertEqual(accepted["decision"], "allow", accepted)
                    self.assertEqual(len(events), 1)
                    with patch.object(hermes, "native_context", side_effect=AssertionError("timer cannot fake native context")):
                        result = worker.tick_profile_worker()
                    self.assertEqual(len(result["results"]), 1)
                    self.assertGreater(len(flow.bus.sent), before)
                    paused = bridge.call("pause_worker", task_id="task-a")
                    self.assertEqual(paused["worker"]["status"], "paused")
                    before = len(flow.bus.sent)
                    worker.tick_profile_worker()
                    self.assertEqual(len(flow.bus.sent), before)
            # The same tool from outside a trusted native turn cannot enable it.
            self.assertEqual(bridge.call("prepare_worker_policy", task_id="task-a", policy=policy)["status"], "not_executed")

    def test_disabled_or_empty_profile_does_not_create_state_or_transport(self):
        with patch.object(worker, "read_settings", return_value={}), \
             patch.object(worker, "state_path", return_value=Path("definitely-absent-worker-state.sqlite3")), \
             patch.object(worker, "Store") as store, patch.object(worker, "HelperTransport") as transport:
            self.assertEqual(worker.tick_profile_worker()["status"], "disabled_or_empty")
            store.assert_not_called()
            transport.assert_not_called()


if __name__ == '__main__':
    unittest.main()
