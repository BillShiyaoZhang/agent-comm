"""Pure reminder projection and export tests; no model, notification or real home."""
from contextlib import contextmanager
import json
from pathlib import Path
import sys
import tempfile
import time
from types import SimpleNamespace
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from hermes_platform_agent_comm.collaboration import attention
from hermes_platform_agent_comm.companion_export import ASSETS, export_companion


class AttentionTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.path = Path(self.temp.name) / "attention.sqlite3"
        self.path.touch()
        self.calls = []
        calls = self.calls

        class FakeStore:
            def __init__(self, path, **kwargs):
                calls.append(("open", path))
                calls.append(("local_urn", kwargs.get("local_urn")))
            def attention(self, owner, **kwargs):
                calls.append(("attention", owner, kwargs))
                return {"schema": "agent-comm-attention/v1", "cursor": 1, "has_more": False,
                        "items": [{"attention_id": "attention-1", "task_id": "meeting", "kind": "approval",
                                   "revision": 1, "source_revision": 1, "state": "open", "title": "需要决定",
                                   "safe_summary": "有一个待确认动作", "created_at": 1, "updated_at": 1,
                                   "target": {"kind": "approval", "id": "approval-1"}}]}
            def state(self, owner, task_id):
                calls.append(("state", owner, task_id))
                return {"tasks": [{"owner_session": "native-owner|stored-session"}]}
            def attention_detail(self, owner, attention_id):
                calls.append(("detail", owner, attention_id))
                return {"item": {"attention_id": "attention-1", "task_id": "meeting", "kind": "approval",
                                "revision": 1, "state": "open", "target": {"kind": "approval", "id": "approval-1"},
                                "details": {"context_summary": "Owner-only context", "can_resume": True}},
                        "origin_session_id": "stored-session", "bound_session_id": None}
            def close(self):
                calls.append(("close",))
        self.store_type = FakeStore
        for patcher in (patch.object(attention, "read_settings", return_value={"collaboration_enabled": True, "urn": "urn:agent-comm:agent:local"}),
                        patch.object(attention, "state_path", return_value=self.path),
                        patch.object(attention, "profile_principal", return_value="native-owner"),
                        patch.object(attention, "_resolve_native_session", side_effect=lambda candidate: candidate),
                        patch.object(attention, "Store", FakeStore)):
            patcher.start()
            self.addCleanup(patcher.stop)

    def test_owner_is_host_derived_and_resume_is_navigation_only(self):
        result = attention.read_attention(after=0, limit=50)
        self.assertIn(("attention", "native-owner|attention", {"after": 0, "limit": 50}), self.calls)
        item = result["items"][0]
        self.assertEqual(item["resume"]["stored_session_id"], "stored-session")
        self.assertIn('"action": "state"', item["resume"]["instruction"])
        self.assertIn('"action": "confirm", "approval_id": "approval-1"', item["resume"]["instruction"])
        self.assertIn('"target": {"kind": "approval", "id": "approval-1"}', item["resume"]["instruction"])
        self.assertIn(("local_urn", "urn:agent-comm:agent:local"), self.calls)
        self.assertNotIn("token", json.dumps(result))
        self.assertEqual([c[0] for c in self.calls], ["open", "local_urn", "attention", "detail", "close"])

    def test_missing_session_is_not_a_native_navigation_target(self):
        with patch.object(attention, "_resolve_native_session", return_value=None):
            result = attention.read_attention()["items"][0]["resume"]
            self.assertIsNone(result["stored_session_id"])
            self.assertEqual(result["session_state"], "missing")
        with patch.object(attention, "_resolve_native_session", side_effect=TimeoutError("host unavailable")):
            with self.assertRaises(TimeoutError):
                attention.read_attention()

    def test_polling_disabled_or_new_profile_does_not_create_db(self):
        with patch.object(attention, "read_settings", return_value={}):
            self.assertFalse(attention.read_attention()["available"])
        self.path.unlink()
        result = attention.read_attention()
        self.assertEqual(result["items"], [])
        self.assertFalse(self.path.exists())
        self.assertEqual(self.calls, [])

    def test_invalid_cursor_and_limits_fail_before_opening_state(self):
        for args in ({"after": -1}, {"after": True}, {"limit": 101}, {"limit": 0}, {"limit": True}):
            with self.subTest(args=args), self.assertRaises(ValueError):
                attention.read_attention(**args)
        self.assertEqual(self.calls, [])

    def test_remote_only_profile_shares_real_message_read_state_with_local_attention(self):
        from agent_comm_runtime.store import Store
        settings = {"remote_enabled": True, "urn": "urn:agent-comm:agent:local"}
        store = Store(self.path, local_urn=settings["urn"], owner_principal="native-owner")
        try:
            store.ingest_message({"message_id": "friend-request", "sender_urn": "urn:agent-comm:agent:peer",
                "conversation_id": "friend-request", "kind": "contact.request",
                "text": json.dumps({"protocol": "agent-comm-contacts/v1", "type": "request",
                                    "request_id": "friend-request"})})
            store.remote_mutation("contacts.respond", {"request_id": "friend-request", "decision": "accept"},
                "native-owner|attention", request_key="e" * 64, fingerprint="f" * 64,
                valid_until=time.time() + 300)
            store.ingest_message({"message_id": "incoming", "sender_urn": "urn:agent-comm:agent:peer", "text": "完整消息"})
        finally:
            store.close()
        with patch.object(attention, "Store", Store), patch.object(attention, "read_settings", return_value=settings):
            feed = attention.read_attention()
            self.assertTrue(feed["available"])
            message_item = next(item for item in feed["items"] if item["kind"] == "peer_message_received")
            self.assertEqual(message_item["details"]["peer_message"]["text"], "完整消息")
            self.assertEqual(attention.mark_attention_read({"message_id": "incoming"})["status"], "read")
            changed = attention.read_attention(after=feed["cursor"])
            self.assertEqual(next(item for item in changed["items"]
                                  if item["attention_id"] == message_item["attention_id"])["state"], "resolved")

    def test_friend_request_is_visible_with_context_and_native_handling_instruction(self):
        from agent_comm_runtime.store import Store
        from agent_comm_runtime.social import SOCIAL_PROTOCOL
        store = Store(self.path, local_urn="urn:agent-comm:agent:local", owner_principal="native-owner")
        try:
            store.ingest_message({"message_id": "friend-request", "sender_urn": "urn:agent-comm:agent:peer",
                "conversation_id": "friend-request", "kind": "contact.request",
                "text": json.dumps({"protocol": SOCIAL_PROTOCOL, "type": "request", "request_id": "friend-request"})})
        finally:
            store.close()
        with patch.object(attention, "Store", Store):
            feed = attention.read_attention()
        request = next(item for item in feed["items"] if item["kind"] == "friend_request_received")
        self.assertEqual(request["target"]["kind"], "contact")
        self.assertEqual(request["details"]["contact_request"]["peer_urn"], "urn:agent-comm:agent:peer")
        self.assertIn("prepare_contact_response", request["resume"]["instruction"])

    def test_router_authenticates_before_resolving_profile_or_reading(self):
        from fastapi import FastAPI, HTTPException
        from fastapi.testclient import TestClient
        events = []

        def require(request):
            events.append("auth")
            if request.headers.get("authorization") != "Bearer test-only":
                raise HTTPException(status_code=401)

        @contextmanager
        def profile_scope(profile):
            events.append(("profile", profile))
            if profile == "../other":
                raise HTTPException(status_code=400)
            yield

        modules = {"hermes_cli.web_server": SimpleNamespace(_require_token=require),
                   "hermes_cli.web_server_profiles": SimpleNamespace(_config_profile_scope=profile_scope)}
        with patch.dict(sys.modules, modules):
            app = FastAPI()
            app.include_router(attention.create_router(), prefix="/api/plugins/agent-comm-attention")
            with TestClient(app) as client:
                path = "/api/plugins/agent-comm-attention/attention"
                self.assertEqual(client.get(path).status_code, 401)
                self.assertEqual(events, ["auth"])
                self.assertEqual(self.calls, [])
                self.assertEqual(client.get(path + "?owner=forged", headers={"authorization": "Bearer test-only"}).status_code, 400)
                self.assertEqual(client.get(path + "?profile=../other", headers={"authorization": "Bearer test-only"}).status_code, 400)
                result = client.get(path + "?profile=work&after=0", headers={"authorization": "Bearer test-only"})
                self.assertEqual(result.status_code, 200)
                self.assertEqual(events[-2:], ["auth", ("profile", "work")])
                self.assertEqual(result.json()["owner_key"], "native-owner")
                self.assertEqual(client.post(path, headers={"authorization": "Bearer test-only"}).status_code, 405)

    def test_companion_export_contains_both_real_hermes_surfaces_and_never_overwrites(self):
        destination = Path(self.temp.name) / "export"
        result = export_companion(destination)
        self.assertEqual(result["status"], "exported_not_enabled")
        self.assertTrue(all((destination / name).is_file() for name in ASSETS))
        self.assertEqual(json.loads((destination / "dashboard/manifest.json").read_text("utf-8"))["api"], "plugin_api.py")
        marker = destination / "keep.txt"
        marker.write_text("keep", encoding="utf-8")
        with self.assertRaises(ValueError):
            export_companion(destination)
        self.assertEqual(marker.read_text("utf-8"), "keep")


if __name__ == "__main__":
    unittest.main()
