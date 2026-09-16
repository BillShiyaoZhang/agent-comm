"""Adversarial release inputs must fail before replacing installed files."""
import hashlib
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
import zipfile

import release_manifest_fetch as fetch


class ReleaseSecurityTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.asset = {"name": "agent-comm-helper-linux-amd64", "sha256": hashlib.sha256(b"verified").hexdigest()}

    def test_unsafe_names_and_missing_digest_rejected_before_network(self):
        for name in ("../outside", "/outside", "..\\outside", "C:outside", "x:y", "NUL", "CON.md", "x."):
            with self.subTest(name=name), patch.object(fetch, "download_to_file") as download:
                with self.assertRaises(ValueError):
                    fetch.download_asset("https://example.com", {**self.asset, "name": name}, self.root)
                download.assert_not_called()
        for digest in (None, "", "bad", "A" * 64):
            with self.subTest(digest=digest), patch.object(fetch, "download_to_file") as download:
                with self.assertRaises(ValueError):
                    fetch.download_asset("https://example.com", {**self.asset, "sha256": digest}, self.root)
                download.assert_not_called()

    def test_install_name_cannot_escape_output(self):
        with self.assertRaises(ValueError):
            fetch.download_asset("https://example.com", self.asset, self.root, "../outside")

    def test_hash_mismatch_preserves_installed_binary_and_cleans_download(self):
        destination = self.root / self.asset["name"]
        destination.write_bytes(b"old installation")
        with patch.object(fetch, "download_to_file", side_effect=lambda url, path: path.write_bytes(b"tampered")):
            with self.assertRaises(SystemExit):
                fetch.download_asset("https://example.com", self.asset, self.root)
        self.assertEqual(destination.read_bytes(), b"old installation")
        self.assertEqual(list(self.root.iterdir()), [destination])

    def test_verified_download_atomically_installs(self):
        with patch.object(fetch, "download_to_file", side_effect=lambda url, path: path.write_bytes(b"verified")):
            destination = fetch.download_asset("https://example.com", self.asset, self.root)
        self.assertEqual(destination.read_bytes(), b"verified")
        self.assertEqual(list(self.root.iterdir()), [destination])

    def test_http_and_redirect_downgrades_rejected(self):
        for url in ("http://example.com/asset", "file:///etc/passwd", "https://u:p@example.com/a"):
            with self.subTest(url=url), self.assertRaises(ValueError):
                fetch.secure_url(url)
        with self.assertRaises(ValueError):
            fetch.SecureRedirect().redirect_request(None, None, 302, "", {}, "http://example.com/a")

    def test_document_archive_prevalidated_before_writes(self):
        archive = self.root / "docs.zip"
        for name in ("../outside.md", "/outside.md", "C:/outside.md", "docs/../../outside.md", "docs\\outside.md", "agent-comm.exe"):
            with self.subTest(name=name):
                with zipfile.ZipFile(archive, "w") as out:
                    out.writestr("README.md", "valid first entry")
                    member = zipfile.ZipInfo("placeholder.md")
                    member.filename = name
                    out.writestr(member, "unsafe")
                with self.assertRaises(ValueError):
                    fetch.extract_docs(archive, self.root)
                self.assertFalse((self.root / "README.md").exists())

    def test_document_archive_valid_nested_markdown(self):
        archive = self.root / "docs.zip"
        with zipfile.ZipFile(archive, "w") as out:
            out.writestr("docs/guide.md", "safe documentation")
        fetch.extract_docs(archive, self.root)
        self.assertEqual((self.root / "docs/guide.md").read_text(), "safe documentation")

    def test_document_archive_cannot_follow_existing_symlink(self):
        archive = self.root / "docs.zip"
        outside = self.root / "outside"
        outside.mkdir()
        output = self.root / "output"
        output.mkdir()
        try:
            (output / "docs").symlink_to(outside, target_is_directory=True)
        except (OSError, NotImplementedError):
            self.skipTest("symlink creation unavailable")
        with zipfile.ZipFile(archive, "w") as out:
            out.writestr("docs/secret.md", "escaped")
        with self.assertRaises(ValueError):
            fetch.extract_docs(archive, output)
        self.assertEqual(list(outside.iterdir()), [])


if __name__ == "__main__":
    unittest.main()
