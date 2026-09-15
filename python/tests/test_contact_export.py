"""Contact invitations must be concise, owner-scoped and free of side effects."""

from contextlib import redirect_stderr, redirect_stdout
from io import StringIO
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from agent_comm_runtime import AdapterRegistry, Runtime, Store
from agent_comm_runtime.contact_export import INTRODUCTION_URL, validate_platform_url
from agent_comm_runtime.reference import TerminalHost, main
from agent_comm_runtime.store import digest


SELF_URN = "urn:agent-comm:agent:me"
PEER_URN = "urn:hermes:agent:peer"
PLATFORM = "https://platform.example/base"
PEER_PLATFORM = "https://peer-platform.example:8443/"


class TestContactExport(unittest.TestCase):
    def setUp(self):
        self.folder = tempfile.TemporaryDirectory()
        self.addCleanup(self.folder.cleanup)
        self.path = Path(self.folder.name) / "state.sqlite3"
        self.store = Store(self.path, local_urn=SELF_URN)
        self.addCleanup(self.store.close)
        self.host = TerminalHost(self.folder.name)
        self.registry = AdapterRegistry().register(self.host)
        self.runtime = Runtime(self.store, self.registry, platform_url=PLATFORM)

    def call(self, **args):
        return self.runtime.dispatch({"action": "export_contact", **args}, context=self.host.begin_turn())

    def owner(self, host=None):
        host = host or self.host
        return host.capture(host.begin_turn()).owner_session

    def confirm_contact(self, host=None):
        owner = self.owner(host)
        pending = self.store.prepare_contact("friend", ["Private\nAlias", "Private second alias"], PEER_URN, owner)
        lease = self.store.begin_confirmation(pending["approval_id"], owner)
        self.store.finish_confirmation(pending["approval_id"], lease["token"], owner, "同意")

    def records(self):
        return self.store._db.execute("SELECT kind, id, body FROM collaboration_records ORDER BY kind, id").fetchall()

    def test_self_includes_actual_platform_and_newcomer_entry(self):
        result = self.call()
        self.assertEqual(result, {"status": "exported", "urn": SELF_URN, "platform_url": PLATFORM,
                                 "introduction_url": INTRODUCTION_URL,
                                 "text": f"加我为 agent 好友：{SELF_URN}；平台：{PLATFORM}；了解/接入：{INTRODUCTION_URL}。"})
        self.assertEqual(len(result["text"].splitlines()), 1)

    def test_explicit_self_platform_overrides_host_default(self):
        self.assertEqual(self.call(platform_url=PEER_PLATFORM)["platform_url"], PEER_PLATFORM)

    def test_missing_platform_requires_input_instead_of_guessing(self):
        self.runtime = Runtime(self.store, self.registry)
        result = self.call()
        self.assertEqual(result["status"], "not_executed")
        self.assertIn("platform_url", result["error"])
        self.assertEqual(self.call(platform_url=PLATFORM)["status"], "exported")

    def test_missing_or_invalid_local_urn_cannot_be_replaced_by_model(self):
        for urn in (None, "", "display name", "urn:agent-comm:agent:me\nprivate"):
            with self.subTest(urn=urn):
                self.store.local_urn = urn
                self.assertEqual(self.call()["status"], "not_executed")
        self.assertEqual(self.call(urn=PEER_URN)["status"], "not_executed")

    def test_friend_export_is_minimal_and_does_not_reveal_local_aliases(self):
        self.confirm_contact()
        result = self.call(contact_id="friend", platform_url=PEER_PLATFORM)
        self.assertEqual(result["status"], "exported")
        self.assertEqual(result["urn"], PEER_URN)
        self.assertEqual(result["platform_url"], PEER_PLATFORM)
        self.assertTrue(result["text"].startswith("加这位 agent 为好友："))
        self.assertNotIn("Private", str(result))
        self.assertNotIn("contact_id", result)
        self.assertEqual(len(result["text"].splitlines()), 1)

    def test_friend_never_inherits_self_platform(self):
        self.confirm_contact()
        result = self.call(contact_id="friend")
        self.assertEqual(result["status"], "not_executed")
        self.assertIn("explicitly", result["error"])

    def test_missing_unconfirmed_and_foreign_contacts_are_indistinguishable(self):
        missing = self.call(contact_id="friend", platform_url=PEER_PLATFORM)
        self.store.prepare_contact("friend", ["Private"], PEER_URN, self.owner())
        pending = self.call(contact_id="friend", platform_url=PEER_PLATFORM)
        self.confirm_contact(TerminalHost(Path(self.folder.name) / "other-owner"))
        foreign = self.call(contact_id="friend", platform_url=PEER_PLATFORM)
        self.assertEqual(missing["status"], "not_executed")
        self.assertEqual(missing, pending)
        self.assertEqual(missing, foreign)

    def test_same_owner_can_export_across_sessions(self):
        self.confirm_contact()
        host = TerminalHost(self.folder.name)
        runtime = Runtime(self.store, AdapterRegistry().register(host))
        result = runtime.dispatch({"action": "export_contact", "contact_id": "friend", "platform_url": PEER_PLATFORM},
                                  context=host.begin_turn())
        self.assertEqual(result["status"], "exported")

    def test_export_does_not_mutate_records_or_use_other_capabilities(self):
        self.confirm_contact()
        self.store.register_resource("private", "Private resource", "Never export this", self.owner())
        before = self.records()
        with patch.object(self.store, "state", side_effect=AssertionError("must not export state")), \
                patch.object(self.store, "dispatch", side_effect=AssertionError("must not send")), \
                patch.object(self.store, "begin_confirmation", side_effect=AssertionError("must not ask")):
            for args in ({}, {"contact_id": "friend", "platform_url": PEER_PLATFORM}):
                self.assertEqual(self.call(**args)["status"], "exported")
        self.assertEqual(before, self.records())

    def test_owner_spoofing_and_unknown_fields_are_rejected(self):
        for args in ({"owner_session": "other|session"}, {"introduction_url": "https://other.example"},
                     {"contact_id": None}, {"contact_id": "Private Alias"}):
            with self.subTest(args=args):
                self.assertEqual(self.call(**args)["status"], "not_executed")
        self.assertEqual(self.runtime.dispatch({"action": "export_contact"}, context={})["status"], "not_executed")

    def test_action_is_discoverable(self):
        result = self.runtime.dispatch({"action": "describe"}, context=self.host.begin_turn())
        self.assertIn("export_contact", result["actions"])

    def test_invalid_platform_urls_fail_without_echoing_credentials(self):
        for url in ("", None, 42, "example.com", "ftp://platform.example", "https:///missing-host",
                    "https://user:secret@platform.example", "https://@platform.example",
                    "https://platform.example/?secret=key", "https://platform.example/#section",
                    "https://platform.example?", "https://platform.example#", "https://platform.example:0",
                    "https://platform.example:65536", "https://platform.example:bad", "https://[broken",
                    "https://platform.example/\nInjected", "https://platform.example/\tInjected",
                    "https://platform.example/\u202eprivate", "https://platform.example/path with spaces",
                    "https://platform.example\\other", "https://platform.example/" + "x" * 2048,
                    "http://127.1", "http://2130706433", "http://0x7f000001", "http://0177.0.0.1",
                    "https://%00", "https://abc<def", "https://-invalid.example", "https://invalid..example",
                    "https://platform.example.."):
            with self.subTest(url=url):
                result = self.call(platform_url=url)
                self.assertEqual(result["status"], "not_executed")
                self.assertNotIn("secret", result["error"])

    def test_valid_explicit_platform_urls_are_not_rewritten(self):
        for url in (PLATFORM, PEER_PLATFORM, "http://192.0.2.10:8080/platform", "https://[2001:db8::1]/agent",
                    "http://192.168.1.50:8080", "http://10.0.0.5/platform", "http://platform.internal"):
            with self.subTest(url=url):
                self.assertEqual(validate_platform_url(url), url)

    def test_loopback_or_unspecified_addresses_are_not_shareable_platforms(self):
        for hostname in ("localhost", "LOCALHOST.", "helper.localhost", "ｌｏｃａｌｈｏｓｔ", "127.0.0.1", "127.10.2.3",
                         "0.0.0.0", "[::]", "[::1]", "[::ffff:127.0.0.1]"):
            with self.subTest(hostname=hostname):
                result = self.call(platform_url=f"http://{hostname}:45042")
                self.assertEqual(result["status"], "not_executed")
                self.assertIn("recipient", result["error"])

    def test_invalid_host_default_only_blocks_export_and_can_be_overridden(self):
        for url in ("https://user:secret@platform.example", "http://127.0.0.1:45042", 42):
            with self.subTest(url=url):
                self.runtime = Runtime(self.store, self.registry, platform_url=url)
                for action, expected_field in (("state", "contacts"), ("describe", "actions")):
                    result = self.runtime.dispatch({"action": action}, context=self.host.begin_turn())
                    self.assertIn(expected_field, result)
                self.assertEqual(self.call()["status"], "not_executed")
                self.assertEqual(self.call(platform_url=PLATFORM)["status"], "exported")

    def test_invalid_host_default_does_not_block_task_revocation(self):
        self.confirm_contact()
        owner = self.owner()
        scope = {"purpose": "Revoke this test task", "topic": "Slots", "capabilities": ["share_slots"],
                 "recipient_ids": ["friend"], "participant_ids": [], "resource_ids": [],
                 "window_start": "2099-01-01T10:00:00Z", "window_end": "2099-01-01T11:00:00Z",
                 "expires_at": "2099-01-01T09:00:00Z", "max_duration_minutes": 30,
                 "max_candidates": 2, "max_actions": 2}
        pending = self.store.prepare_task("test-task", scope, owner)
        lease = self.store.begin_confirmation(pending["approval_id"], owner)
        self.store.finish_confirmation(pending["approval_id"], lease["token"], owner, "同意")
        self.runtime = Runtime(self.store, self.registry, platform_url="invalid URL")
        result = self.runtime.dispatch({"action": "revoke", "task_id": "test-task"}, context=self.host.begin_turn())
        self.assertEqual(result["status"], "revoked")

    def legacy_self_binding(self, *, confirmed, urn=PEER_URN, host=None):
        """Seed the on-disk shape accepted before self became a reserved ID."""
        owner = self.owner(host)
        contact = {"contact_id": "self", "aliases": ["Old peer"], "urn": urn,
                   "owner_id": self.store._principal(owner)}
        with self.store._transaction():
            if confirmed:
                self.store._put("contact", "self", contact)
                return None
            return self.store._approval("contact", "self", owner, contact, "Legacy contact request", self.store.clock() + 900)

    def test_reserved_self_contact_cannot_be_prepared(self):
        before = self.records()
        result = self.runtime.dispatch({"action": "prepare_contact", "contact_id": "self",
                                        "aliases": ["Peer"], "urn": PEER_URN}, context=self.host.begin_turn())
        self.assertEqual(result["status"], "not_executed")
        self.assertIn("reserved", result["error"])
        self.assertEqual(before, self.records())
        self.assertEqual(self.call()["urn"], SELF_URN)

    def test_confirmed_legacy_self_binding_cannot_silently_export_local_identity(self):
        for urn in (PEER_URN, SELF_URN):
            with self.subTest(urn=urn):
                self.legacy_self_binding(confirmed=True, urn=urn)
                before = self.records()
                result = self.call(contact_id="self", platform_url=PEER_PLATFORM)
                self.assertEqual(result["status"], "not_executed")
                self.assertIn("different contact_id", result["error"])
                self.assertEqual(before, self.records())
        self.confirm_contact()
        self.assertEqual(self.call(contact_id="friend", platform_url=PEER_PLATFORM)["urn"], PEER_URN)

    def test_pending_legacy_self_binding_is_ambiguous_and_cannot_be_confirmed(self):
        pending = self.legacy_self_binding(confirmed=False)
        before = self.records()
        result = self.call(contact_id="self", platform_url=PEER_PLATFORM)
        self.assertEqual(result["status"], "not_executed")
        self.assertIn("ambiguous", result["error"])
        with self.assertRaisesRegex(ValueError, "reserved"):
            self.store.begin_confirmation(pending["approval_id"], self.owner())
        self.assertEqual(before, self.records())

    def test_inflight_legacy_self_confirmation_cannot_create_reserved_contact(self):
        pending = self.legacy_self_binding(confirmed=False)
        owner = self.owner()
        with self.store._transaction():
            approval = self.store._get("approval", pending["approval_id"])
            approval.update(status="presenting", token_hash=digest("legacy-token"), lease_until=self.store.clock() + 360)
            self.store._put("approval", pending["approval_id"], approval)
        result = self.store.finish_confirmation(pending["approval_id"], "legacy-token", owner, "同意")
        self.assertEqual(result["decision"], "deny")
        self.assertIsNone(self.store._get("contact", "self"))

    def test_foreign_legacy_self_records_do_not_block_this_owners_self(self):
        host = TerminalHost(Path(self.folder.name) / "other-owner")
        self.legacy_self_binding(confirmed=True, host=host)
        self.legacy_self_binding(confirmed=False, host=host)
        self.assertEqual(self.call()["urn"], SELF_URN)

    def cli(self, *args):
        output, errors = StringIO(), StringIO()
        with redirect_stdout(output), redirect_stderr(errors), patch("sys.stdin.isatty", return_value=False):
            status = main(["--state", str(self.path), *args])
        return status, output.getvalue(), errors.getvalue()

    def test_reference_cli_exports_self_without_interactive_or_transport_ports(self):
        status, output, errors = self.cli("--agent-urn", SELF_URN, "--platform-url", PLATFORM, "--export-contact")
        self.assertEqual(status, 0)
        self.assertEqual(output.strip(), self.call()["text"])
        self.assertEqual(errors, "")

    def test_reference_cli_exports_confirmed_friend_with_explicit_platform(self):
        self.confirm_contact()
        before = self.records()
        status, output, errors = self.cli("--platform-url", PEER_PLATFORM, "--export-contact", "friend")
        self.assertEqual(status, 0)
        self.assertIn(PEER_URN, output)
        self.assertEqual(errors, "")
        self.assertEqual(before, self.records())

    def test_reference_cli_returns_actionable_error(self):
        before = self.records()
        status, output, errors = self.cli("--agent-urn", SELF_URN, "--export-contact")
        self.assertEqual(status, 1)
        self.assertEqual(output, "")
        self.assertIn("platform_url", errors)
        self.assertEqual(before, self.records())

    def test_reference_cli_cannot_export_another_profiles_contact(self):
        self.confirm_contact(TerminalHost(Path(self.folder.name) / "other-profile"))
        before = self.records()
        status, output, errors = self.cli("--platform-url", PEER_PLATFORM, "--export-contact", "friend")
        self.assertEqual(status, 1)
        self.assertEqual(output, "")
        self.assertNotIn(PEER_URN, errors)
        self.assertEqual(before, self.records())


if __name__ == "__main__":
    unittest.main()
