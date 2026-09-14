"""Independent contract tests: importing this suite never requires Hermes."""
from contextlib import redirect_stdout
from io import StringIO
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
from types import SimpleNamespace

from agent_comm_runtime import AdapterRegistry, Descriptor, HostSession, MemorySnapshot, Runtime, Store, Unsupported
from agent_comm_runtime.reference import FiniteMemory, TerminalHost, demo


class TestContracts(unittest.TestCase):
    def setUp(self):
        self.folder = tempfile.TemporaryDirectory()
        self.addCleanup(self.folder.cleanup)
        self.store = Store(Path(self.folder.name) / "state.sqlite3")
        self.addCleanup(self.store.close)
        self.host = TerminalHost(self.folder.name)
        self.registry = AdapterRegistry().register(self.host)
        self.runtime = Runtime(self.store, self.registry)

    def call(self, action, **args):
        return self.runtime.dispatch({"action": action, **args}, context=self.host.begin_turn())

    def memory(self):
        return FiniteMemory({"public": {"title": "Public introduction", "text": "Selected short text", "version": "v1"},
                             "private": {"title": "Private", "text": "Unrelated secret", "version": "v7"}})

    def approve(self, reply="同意", before=None):
        class Interaction:
            descriptor = Descriptor("test-native", "interaction", ("confirmation",))
            def request_confirmation(inner, session, question):
                self.assertIn("urn:agent-comm:agent:peer", question)
                self.assertNotIn("token", question)
                if before:
                    before()
                return reply
        self.registry.register(Interaction())

    def stage(self):
        return self.call("prepare_contact", contact_id="wang", aliases=["老王"], urn="urn:agent-comm:agent:peer")

    def test_registration_rejects_incompatible_version(self):
        class Bad:
            descriptor = Descriptor("bad", "memory", (), "2.0")
        with self.assertRaises(ValueError):
            self.registry.register(Bad())

    def test_registration_requires_advertised_methods(self):
        class Bad:
            descriptor = Descriptor("bad", "memory", ("search",))
        with self.assertRaises(ValueError):
            self.registry.register(Bad())

    def test_registration_rejects_unknown_capability(self):
        class Bad:
            descriptor = Descriptor("bad", "memory", ("export_all",))
        with self.assertRaises(ValueError):
            self.registry.register(Bad())

    def test_registration_does_not_replace_authority(self):
        with self.assertRaises(ValueError):
            self.registry.register(TerminalHost("another-profile"))

    def test_entry_point_is_explicit_and_can_register_memory(self):
        loaded = []
        entry = SimpleNamespace(name="my-graph", load=lambda: lambda options: (loaded.append(options), self.memory())[1])
        with patch("agent_comm_runtime.ports.metadata.entry_points", return_value=[entry]):
            self.assertEqual(loaded, [])
            self.registry.load_entry_point("my-graph", options={"selected": "notes"}, expected_port="memory")
        self.assertEqual(loaded, [{"selected": "notes"}])
        self.assertEqual(len(self.call("memory_search", query="Public")["matches"]), 1)

    def test_entry_point_missing_ambiguous_wrong_port_fail_closed(self):
        entry = SimpleNamespace(name="my-graph", load=lambda: lambda options: self.memory())
        for entries in ([], [entry, entry]):
            with patch("agent_comm_runtime.ports.metadata.entry_points", return_value=entries):
                with self.assertRaises(Unsupported):
                    self.registry.load_entry_point("my-graph")
        with patch("agent_comm_runtime.ports.metadata.entry_points", return_value=[entry]):
            with self.assertRaises(ValueError):
                self.registry.load_entry_point("my-graph", expected_port="interaction")

    def test_host_required(self):
        with self.assertRaises(Unsupported):
            Runtime(self.store, AdapterRegistry())

    def test_host_ids_cannot_inject_a_principal_separator(self):
        with self.assertRaises(ValueError):
            HostSession("owner|other", "session", "turn")

    def test_model_cannot_claim_owner_or_supply_answer(self):
        for extra in ({"owner_session": "root|session"}, {"raw_response": "同意"}, {"context": {}}):
            self.assertEqual(self.call("state", **extra)["status"], "not_executed")
        self.assertEqual(self.runtime.dispatch({"action": "state"}, context={})["status"], "not_executed")

    def test_unknown_action_has_explicit_unsupported_result(self):
        self.assertEqual(self.call("calendar_create")["status"], "unsupported")

    def test_missing_ports_do_not_fallback_to_memory_or_network(self):
        self.assertEqual(self.call("memory_search", query="friend")["status"], "unsupported")
        self.assertEqual(self.call("inbox")["status"], "unsupported")
        pending = self.stage()
        self.assertEqual(self.call("confirm", approval_id=pending["approval_id"])["status"], "unsupported")
        self.assertEqual(self.call("state")["pending_confirmations"][0]["status"], "pending")

    def test_exact_native_callback_is_required_for_authority(self):
        self.approve()
        pending = self.stage()
        self.call("confirm", approval_id=pending["approval_id"])
        self.assertEqual(len(self.call("resolve_contact", name="老王")["contacts"]), 1)

    def test_context_change_invalidates_yes(self):
        self.approve(before=self.host.begin_turn)
        pending = self.stage()
        result = self.call("confirm", approval_id=pending["approval_id"])
        self.assertNotEqual(result.get("status"), "approved")
        self.assertEqual(self.call("resolve_contact", name="老王")["contacts"], [])

    def test_conditional_response_does_not_authorize(self):
        self.approve("同意，但只联系另一个人")
        pending = self.stage()
        self.call("confirm", approval_id=pending["approval_id"])
        self.assertEqual(self.call("resolve_contact", name="老王")["contacts"], [])

    def test_search_and_snapshot_do_not_export_unselected_records(self):
        self.registry.register(self.memory())
        found = self.call("memory_search", query="Public", limit=1)
        self.assertEqual(len(found["matches"]), 1)
        self.assertNotIn("Unrelated secret", str(found))
        result = self.call("snapshot_resource", reference="public", resource_id="intro")
        self.assertEqual(result["status"], "registered_not_authorized")
        self.assertEqual(result["provenance"]["version"], "v1")
        self.assertEqual(self.store._get("resource", "intro")["provenance"]["reference"], "public")
        self.assertEqual(self.call("state")["tasks"], [])

    def test_memory_snapshot_is_immutable_after_registration(self):
        memory = self.memory()
        self.registry.register(memory)
        self.call("snapshot_resource", reference="public", resource_id="intro")
        memory.records["public"]["text"] = "Changed text"
        self.assertEqual(self.call("snapshot_resource", reference="public", resource_id="intro")["status"], "not_executed")

    def test_memory_input_and_result_bounds_are_enforced(self):
        self.registry.register(self.memory())
        for fields in ({"query": "", "limit": 1}, {"query": "private", "limit": True}, {"query": "private", "limit": 21}):
            self.assertEqual(self.call("memory_search", **fields)["status"], "not_executed")
        self.assertEqual(self.call("memory_snapshot", reference="public", max_chars=3)["status"], "not_executed")

    def test_overbroad_adapter_results_are_rejected(self):
        memory = self.memory()
        memory.search = lambda session, query, limit: [{"reference": "ref", "title": "T", "summary": "S", "whole_graph": "private"}]
        self.registry.register(memory)
        self.assertEqual(self.call("memory_search", query="T")["status"], "not_executed")

    def test_context_revalidated_after_memory_read(self):
        memory = self.memory()
        read = memory.read_snapshot
        def changed(session, ref, maximum):
            result = read(session, ref, maximum)
            self.host.begin_turn()
            return result
        memory.read_snapshot = changed
        self.registry.register(memory)
        self.assertEqual(self.call("snapshot_resource", reference="public", resource_id="intro")["status"], "not_executed")
        self.assertIsNone(self.store._get("resource", "intro"))

    def test_optional_notification_is_real_registered_dispatch(self):
        calls = []
        class Notices:
            descriptor = Descriptor("test-notification", "interaction", ("notification",))
            def notify(inner, session, text):
                calls.append(text)
                return {"status": "notified"}
        self.registry.register(Notices())
        session = self.host.capture(self.host.begin_turn())
        result = self.registry.invoke("interaction", "notification", "notify", session, "Resume task")
        self.assertEqual(result["status"], "notified")
        self.assertEqual(calls, ["Resume task"])
        with self.assertRaises(Unsupported):
            self.registry.invoke("interaction", "notification", "request_confirmation", session, "Approve?")

    def test_control_messages_are_not_consumed_by_peer_inbox(self):
        acked = []
        class Transport:
            def retrieve(inner):
                return [{"message_id": "control", "sender_urn": "urn:agent-comm:agent:owner-console", "text": "{}", "kind": "control.request"},
                        {"message_id": "peer", "sender_urn": "urn:agent-comm:agent:peer", "text": "Hello"}]
            def ack(inner, ids):
                acked.extend(ids)
        self.store.sync_inbox(Transport())
        self.assertEqual(acked, ["peer"])
        self.assertIsNone(self.store._get("inbound", "control"))

    def test_offline_reference_demo_runs_without_host_sdk(self):
        output = StringIO()
        with redirect_stdout(output):
            demo()
        self.assertIn("registered_not_authorized", output.getvalue())
        self.assertNotIn("Unrelated secret", output.getvalue())


if __name__ == "__main__":
    unittest.main()
