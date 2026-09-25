"""Persisted upgrade guard for pre-v0.9.2 digest-only accept approvals."""

from contextlib import closing
import json
from pathlib import Path
import sqlite3
import tempfile
import unittest

from agent_comm_runtime.store import Store, canonical


NOW = 1_780_000_000
OWNER = "owner|native"


def operation(operation_id, status, *, kind="accept", reviewed=False):
    value = {"operation_id": operation_id, "kind": kind, "status": status,
             "owner_id": "owner", "task_id": "task", "collaboration_id": "collaboration",
             "decision": {"decision": "ask", "reasons": ["explicit_protocol_decision"]},
             "approval_id": "approval-" + operation_id, "expires_at": NOW + 3600,
             "hash": "old-hash", "text": "{}", "deliveries": []}
    if reviewed:
        value["owner_review_version"] = 2
    return value


def approval(operation_id, status):
    value = {"approval_id": "approval-" + operation_id, "kind": "collaboration_v2",
             "subject_id": operation_id, "owner_session": OWNER, "payload": {"hash": "old-hash"},
             "fingerprint": "old-fingerprint-" + operation_id, "question": "仅含条款摘要的旧卡",
             "expires_at": NOW + 900, "created_at": NOW, "status": status}
    if status == "presenting":
        value.update(token_hash="stale-native-token", lease_until=NOW + 300)
    return value


class LegacyAcceptUpgradeTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.path = Path(self.temp.name) / "collaboration.sqlite3"

    def seed(self, entries):
        store = Store(self.path, clock=lambda: NOW)
        with store._transaction():
            for op, card in entries:
                store._db.execute("INSERT INTO collaboration_records VALUES (?,?,?)",
                                  ("v2_operation", op["operation_id"], canonical(op)))
                store._db.execute("INSERT INTO collaboration_records VALUES (?,?,?)",
                                  ("approval", card["approval_id"], canonical(card)))
        store.close()

    def stored(self, kind, key):
        with closing(sqlite3.connect(self.path)) as db:
            row = db.execute("SELECT body FROM collaboration_records WHERE kind=? AND id=?", (kind, key)).fetchone()
            return json.loads(row[0]) if row else None

    def audit_count(self):
        with closing(sqlite3.connect(self.path)) as db:
            rows = db.execute("SELECT body FROM collaboration_records WHERE kind='audit'").fetchall()
            return sum(json.loads(row[0]).get("event") == "legacy_accept_approval_invalidated" for row in rows)

    def test_upgrade_persists_terminal_old_cards_without_touching_reserved_sends(self):
        entries = [(operation("pending", "awaiting_approval"), approval("pending", "pending")),
                   (operation("presenting", "awaiting_approval"), approval("presenting", "presenting")),
                   (operation("approved", "ready"), approval("approved", "approved")),
                   (operation("new", "awaiting_approval", reviewed=True), approval("new", "pending")),
                   (operation("sending", "sending"), approval("sending", "approved")),
                   (operation("accepted", "accepted"), approval("accepted", "approved")),
                   (operation("proposal", "awaiting_approval", kind="proposal"), approval("proposal", "pending"))]
        self.seed(entries)
        store = Store(self.path, clock=lambda: NOW)
        self.addCleanup(store.close)
        for name in ("pending", "presenting", "approved"):
            op = self.stored("v2_operation", name)
            card = self.stored("approval", "approval-" + name)
            self.assertEqual(op["status"], "denied")
            self.assertEqual(card["status"], "superseded")
            self.assertEqual(op["invalidation_reason"], "upgrade_requires_full_terms_review")
            self.assertEqual(card["invalidation_reason"], "upgrade_requires_full_terms_review")
            self.assertNotIn("token_hash", card)
            self.assertNotIn("lease_until", card)
            self.assertEqual(store.dispatch_collaboration(name, OWNER, object())["decision"], "deny")
            with self.assertRaisesRegex(ValueError, "superseded"):
                store.remote_mutation("approval.respond", {"approval_id": card["approval_id"],
                                                         "decision": "approve"}, OWNER,
                                      request_key="a" * 64, fingerprint="b" * 64, valid_until=NOW + 1000)
        for name, original in (("new", "awaiting_approval"), ("sending", "sending"),
                               ("accepted", "accepted"), ("proposal", "awaiting_approval")):
            self.assertEqual(self.stored("v2_operation", name)["status"], original)
        self.assertEqual(self.audit_count(), 3)
        store.close()
        again = Store(self.path, clock=lambda: NOW)
        again.close()
        self.assertEqual(self.audit_count(), 3)
        self.assertEqual(self.stored("approval", "approval-approved")["status"], "superseded")

    def test_migration_rolls_back_all_records_if_a_card_write_fails(self):
        self.seed([(operation("pending", "awaiting_approval"), approval("pending", "pending"))])

        class FailedCardWrite(Store):
            def _put(self, kind, key, body):
                if kind == "approval" and key == "approval-pending":
                    raise RuntimeError("injected migration failure")
                return super()._put(kind, key, body)

        with self.assertRaisesRegex(RuntimeError, "injected migration failure"):
            FailedCardWrite(self.path, clock=lambda: NOW)
        self.assertEqual(self.stored("v2_operation", "pending")["status"], "awaiting_approval")
        self.assertEqual(self.stored("approval", "approval-pending")["status"], "pending")
        self.assertEqual(self.audit_count(), 0)
        store = Store(self.path, clock=lambda: NOW)
        store.close()
        self.assertEqual(self.stored("v2_operation", "pending")["status"], "denied")
        self.assertEqual(self.stored("approval", "approval-pending")["status"], "superseded")


if __name__ == "__main__":
    unittest.main()
