"""Existing Web-generated identities must remain usable without key rotation."""
import base64
import hashlib
import json
from pathlib import Path
import tempfile
import unittest

from agent_comm_runtime.identity import validate_urn
from agent_comm_runtime.store import Store


class TestNetworkIdentity(unittest.TestCase):
    def test_web_derived_fixture_binds_and_receives_with_original_namespace(self):
        fixture = json.loads((Path(__file__).parent / "fixtures/web-console-identity.json").read_text(encoding="utf-8"))
        # Same SHA256[:16] + base58 derivation as Web crypto.ts, using the existing
        # signed interoperability fixture. Signature verification remains upstream.
        fingerprint = hashlib.sha256(base64.b64decode(fixture["ed25519_public_key_base64"])).digest()[:16]
        alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"
        number, encoded = int.from_bytes(fingerprint, "big"), ""
        while number:
            number, remainder = divmod(number, 58)
            encoded = alphabet[remainder] + encoded
        encoded = "1" * (len(fingerprint) - len(fingerprint.lstrip(b"\0"))) + encoded
        self.assertEqual(fixture["urn"], "urn:hermes:agent:" + encoded)
        with tempfile.TemporaryDirectory() as folder:
            store = Store(Path(folder) / "state.sqlite3")
            try:
                owner = "test-owner|native-session"
                pending = store.prepare_contact("web-owner", ["主人工作台"], fixture["urn"], owner)
                lease = store.begin_confirmation(pending["approval_id"], owner)
                store.finish_confirmation(pending["approval_id"], lease["token"], owner, "同意")
                store.ingest_message({"message_id": "from-real-web-namespace", "sender_urn": fixture["urn"], "text": "A bounded peer statement"})
                self.assertEqual(store.resolve_contact("主人工作台", owner)["contacts"][0]["urn"], fixture["urn"])
                self.assertEqual(store.inbox(owner)["messages"][0]["sender_urn"], fixture["urn"])
            finally:
                store.close()

    def test_namespace_is_not_a_hostname_or_authority_claim(self):
        for value in ("urn:agent-comm:agent:old-peer", "urn:hermes:agent:BewGwPDweP6xa1niibe5NW", "urn:example:custom-agent:abc123"):
            self.assertEqual(validate_urn(value), value)
        for value in (None, "", "old-peer", "https://example.com", "urn:hermes:agent:",
                      "urn:bad namespace:abc", "urn:hermes:agent:abc\n", "urn:hermes:agent:abc|owner", "urn:x:" + "a" * 256):
            with self.subTest(value=value), self.assertRaises(ValueError):
                validate_urn(value)


if __name__ == "__main__":
    unittest.main()
