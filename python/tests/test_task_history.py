"""Owner-scoped task mentions, navigation, and retained evidence."""

from datetime import datetime, timezone
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from agent_comm_runtime.remote import PROTOCOL, READ_METHODS, RemoteBridge
from agent_comm_runtime.store import Store


NOW = 2_000_000_000
AGENT = "urn:agent-comm:agent:task-agent"
CONSOLE = "urn:agent-comm:agent:task-console"
OTHER = "urn:agent-comm:agent:task-other"


def stamp(value):
    return datetime.fromtimestamp(value, timezone.utc).isoformat()


class TaskHistoryTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        path = Path(self.tmp.name)
        self.store = Store(path / "store.sqlite3", clock=lambda: NOW, local_urn=AGENT)
        self.bridge = RemoteBridge(path / "remote.sqlite3", self.store, AGENT, clock=lambda: NOW,
                                   conversations=True, peer_content_safety=True)
        self.bridge.pair(CONSOLE, "owner-a", [*READ_METHODS, "conversation.send", "conversation.get"], stamp(NOW + 1000))
        self.bridge.pair(OTHER, "owner-b", [*READ_METHODS, "conversation.send", "conversation.get"], stamp(NOW + 1000))
        with self.store._transaction():
            self.store._put("contact", "peer", {"contact_id": "peer", "aliases": ["Peer"],
                "urn": "urn:agent-comm:agent:peer", "owner_id": "owner-a", "status": "connected"})
        self.scope = {"purpose": "Coordinate a review", "topic": "Review", "capabilities": ["send_text"],
            "recipient_ids": ["peer"], "participant_ids": ["self", "peer"], "resource_ids": [],
            "window_start": stamp(NOW + 10), "window_end": stamp(NOW + 500),
            "expires_at": stamp(NOW + 600), "max_duration_minutes": 30,
            "max_candidates": 2, "max_actions": 4}

    def tearDown(self):
        self.bridge.close()
        self.store.close()
        self.tmp.cleanup()

    def rpc(self, request_id, method, params=None, console=CONSOLE):
        packet = {"protocol": PROTOCOL, "type": "request", "request_id": request_id, "method": method,
            "params": params or {}, "agent_urn": AGENT, "console_urn": console, "deadline": stamp(NOW + 120)}
        wire = {"message_id": request_id, "sender_urn": console, "kind": "control.request",
            "conversation_id": "control:" + request_id, "deadline": packet["deadline"], "text": json.dumps(packet)}
        return json.loads(self.bridge.handle(wire)["text"])

    def task(self, task_id):
        return self.store.prepare_task(task_id, self.scope, "owner-a|native")

    def test_mentions_are_verified_multi_task_context_not_authorization(self):
        first = self.task("review-a")
        self.task("review-b")
        mentions = [{"kind": "task", "task_id": "review-a"}, {"kind": "task", "task_id": "review-b"}]
        sent = self.rpc("send", "conversation.send", {"conversation_id": "chat", "text": "Compare these", "mentions": mentions})
        self.assertEqual(sent["result"]["mentions"], mentions)
        turn = self.rpc("get", "conversation.get", {"conversation_id": "chat"})["result"]["turns"][0]
        self.assertEqual(turn["mentions"], mentions)
        self.assertEqual(turn["related"], [], "mention must not impersonate an actual operation")
        self.assertEqual(self.store._get("task", "review-a")["status"], "pending")
        self.assertEqual(self.store._get("approval", first["approval_id"])["status"], "pending")
        self.assertEqual(self.rpc("other-send", "conversation.send", {"text": "Read it", "mentions": mentions[:1]}, OTHER)["error"]["code"], "unknown_task")
        self.assertEqual(self.rpc("duplicate", "conversation.send", {"text": "x", "mentions": mentions[:1] * 2})["error"]["code"], "invalid_params")
        self.assertEqual(self.rpc("invented", "conversation.send", {"text": "x", "mentions": [{"kind": "task", "task_id": "missing"}]})["error"]["code"], "unknown_task")
        self.assertEqual(self.rpc("forged-kind", "conversation.send", {"text": "x", "mentions": [{"kind": "approval", "task_id": "review-a"}]})["error"]["code"], "invalid_params")

    def test_discovery_detail_and_paginated_evidence_are_owner_scoped(self):
        prepared = self.task("review-a")
        self.task("review-b")
        listing = self.rpc("list", "task.list", {"query": "review", "limit": 1})["result"]
        self.assertEqual([i["task_id"] for i in listing["items"]], ["review-a"])
        self.assertEqual(self.rpc("list-next", "task.list", {"query": "review", "limit": 1,
                         "cursor": listing["next_cursor"]})["result"]["items"][0]["task_id"], "review-b")
        self.assertEqual(self.rpc("other-list", "task.list", console=OTHER)["result"]["items"], [])
        detail = self.rpc("detail", "task.detail", {"task_id": "review-a"})["result"]
        self.assertEqual(detail["task"]["task_id"], "review-a")
        self.assertEqual(detail["approvals"][0]["approval_id"], prepared["approval_id"])
        self.assertIn("Coordinate a review", detail["approvals"][0]["question"])
        self.assertFalse(detail["coverage"]["complete"])
        self.assertIn("error", self.rpc("other-detail", "task.detail", {"task_id": "review-a"}, OTHER))

        sent = self.rpc("send", "conversation.send", {"conversation_id": "chat", "text": "Check review",
            "mentions": [{"kind": "task", "task_id": "review-a"}]})["result"]
        detail = self.rpc("detail-linked", "task.detail", {"task_id": "review-a"})["result"]
        self.assertEqual(detail["conversation_refs"], [{"conversation_id": "chat", "turn_id": sent["turn_id"], "relation": "mention"}])
        second = self.rpc("send-second-chat", "conversation.send", {"conversation_id": "other-chat",
            "text": "Follow up on the same task", "mentions": [{"kind": "task", "task_id": "review-a"}]})["result"]
        detail = self.rpc("detail-two-chats", "task.detail", {"task_id": "review-a"})["result"]
        self.assertEqual({(ref["conversation_id"], ref["turn_id"]) for ref in detail["conversation_refs"]},
                         {("chat", sent["turn_id"]), ("other-chat", second["turn_id"])})
        with self.store.bind_source_context("owner-a|native", {"origin": "paired_conversation",
                "conversation_id": "chat", "turn_id": sent["turn_id"]}, CONSOLE):
            self.store.revoke("review-a", "owner-a|native")
        linked = self.rpc("detail-acted", "task.detail", {"task_id": "review-a"})["result"]
        self.assertEqual({ref["relation"] for ref in linked["conversation_refs"]}, {"mention", "related"})
        seen, cursor = [], None
        for n in range(10):
            page = self.rpc("events-" + str(n), "task.events", {"task_id": "review-a", "limit": 1,
                            **({"cursor": cursor} if cursor else {})})["result"]
            seen.extend(page["items"])
            cursor = page["next_cursor"]
            if cursor is None:
                break
        self.assertEqual(len(seen), len({item["event_id"] for item in seen}))
        self.assertTrue(any(item["kind"] == "owner_conversation" for item in seen))
        self.assertTrue(any(item["kind"] == "approval" for item in seen))
        self.assertFalse(page["coverage"]["complete"])
        self.assertIn("error", self.rpc("other-events", "task.events", {"task_id": "review-a"}, OTHER))

    def test_old_pairing_does_not_gain_task_reads(self):
        self.task("review-a")
        self.bridge.pair(CONSOLE, "owner-a", ["capabilities", "conversation.send", "conversation.get"], stamp(NOW + 1000))
        methods = {item["name"]: item["available"] for item in self.rpc("caps", "capabilities")["result"]["methods"]}
        self.assertFalse(methods["task.list"])
        self.assertEqual(self.rpc("denied", "task.list")["error"]["code"], "method_not_allowed")
        self.assertEqual(self.rpc("denied-detail", "task.detail", {"task_id": "review-a"})["error"]["code"], "method_not_allowed")

    def test_protocol_timeline_excludes_raw_peer_wire(self):
        self.task("review-a")
        packet = {"collaboration_id": "shared-1", "event_id": "event-1", "sender_urn":
                  "urn:agent-comm:agent:peer", "kind": "receipt", "payload":
                  {"reason": "unreviewed peer secret marker"}}
        with self.store._transaction():
            self.store._put("v2_event", "peer-event", {"owner_id": "owner-a", "task_id": "review-a",
                "packet": packet, "digest": "digest-1", "recorded_at": NOW + 1,
                "status": "applied", "outgoing": False})
        with patch.object(self.store, "collaborations", return_value={"collaborations":
                [{"collaboration_id": "shared-1"}]}):
            events = self.store.task_events("owner-a|native", "review-a")
        protocol = next(item for item in events["items"] if item["kind"] == "protocol_event")
        self.assertEqual(protocol["details"]["status"], "applied")
        self.assertNotIn("unreviewed peer secret marker", json.dumps(events))

    def test_hidden_peer_proposal_redacts_detail_and_immutable_history(self):
        self.task("review-a")
        revision = self.store._get("task", "review-a")["revision"]
        marker = "blocked peer proposal secret marker"
        with self.store._transaction():
            self.store._put("proposal", self.store._proposal_key("review-a", "peer-proposal"),
                {"task_id": "review-a", "payload": {"proposal_id": "peer-proposal"},
                 "source_message_id": "unavailable-peer-message"})
            self.store._put("operation", "hidden-operation", {"operation_id": "hidden-operation",
                "task_id": "review-a", "owner_session": "owner-a|native", "revision": revision,
                "status": "accepted", "action": {"capability": "propose_meeting",
                    "payload": {"proposal_id": "peer-proposal", "topic": marker}},
                "text": marker, "deliveries": [{"status": "accepted", "text": marker}],
                "decision": {"decision": "allow", "reasons": []}})
            self.store._put("approval", "hidden-approval", {"approval_id": "hidden-approval",
                "kind": "operation", "subject_id": "hidden-operation", "owner_session": "owner-a|native",
                "status": "approved", "question": marker, "fingerprint": "hidden-approval",
                "created_at": NOW, "expires_at": NOW + 1000})
        detail = self.rpc("hidden-detail", "task.detail", {"task_id": "review-a"})["result"]
        operation = next(item for item in detail["operations"] if item["operation_id"] == "hidden-operation")
        self.assertTrue(operation["content_redacted"])
        self.assertEqual(operation["reasons"], ["peer_content_unavailable"])
        approval = next(item for item in detail["approvals"] if item["approval_id"] == "hidden-approval")
        self.assertTrue(approval["question_redacted"])
        events = self.rpc("hidden-events", "task.events", {"task_id": "review-a", "limit": 20})["result"]
        self.assertNotIn(marker, json.dumps(detail) + json.dumps(events))
        with self.store._transaction():
            self.store._put("v2_operation", "hidden-v2-operation", {"operation_id": "hidden-v2-operation",
                "owner_id": "owner-a", "task_id": "review-a", "kind": "accept", "status": "denied",
                "payload": {"text": marker}, "text": marker,
                "deliveries": [{"status": "accepted", "text": marker}]})
        journal = "".join(row[0] for row in self.store._db.execute("SELECT body FROM task_history"))
        self.assertNotIn(marker, journal)

    def test_large_history_remains_pageable_with_explicit_truncation(self):
        self.task("review-a")
        with self.store._transaction():
            for number in range(25):
                approval_id = f"large-approval-{number:02}"
                self.store._put("approval", approval_id, {"approval_id": approval_id, "kind": "task",
                    "subject_id": "review-a", "owner_session": "owner-a|native", "status": "denied",
                    "question": "Q" * 18000, "fingerprint": approval_id,
                    "created_at": NOW + number, "expires_at": NOW + 1000})
        detail = self.rpc("large-detail", "task.detail", {"task_id": "review-a"})["result"]
        self.assertTrue(detail["coverage"]["truncated"]["approvals"])
        self.assertLess(len(json.dumps(detail).encode()), 250000)
        first = self.store.task_events("owner-a|native", "review-a", limit=20)
        self.assertIsNotNone(first["next_cursor"], "byte budget must still provide a resume cursor")
        huge_turn = {"turn_id": "turn-huge", "conversation_id": "chat", "status": "completed",
                     "created_at": NOW + 100, "text": "prompt", "response": "\\" * 190000,
                     "mentions": [{"kind": "task", "task_id": "review-a"}]}
        page = self.store.task_events("owner-a|native", "review-a", limit=20,
                                      cursor=first["next_cursor"], turns=[huge_turn])
        while page["next_cursor"] and not any(i["kind"] == "owner_conversation" for i in page["items"]):
            page = self.store.task_events("owner-a|native", "review-a", limit=20,
                                          cursor=page["next_cursor"], turns=[huge_turn])
        turn = next(i for i in page["items"] if i["kind"] == "owner_conversation")
        self.assertTrue(turn["details"]["response_truncated"])
        self.assertLess(len(json.dumps(page).encode()), 250000)


if __name__ == "__main__":
    unittest.main()
