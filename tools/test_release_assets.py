"""Publication guards and compatibility checks; all remote calls are mocked."""
import ast
import hashlib
import json
from pathlib import Path
import re
import subprocess
import tempfile
import tomllib
import unittest
from unittest.mock import Mock, patch
from urllib.error import HTTPError

import release_assets as release
import release_manifest_fetch as fetch


class ReleaseAssetsTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.sdk_sha = "a" * 40
        self.deploy_sha = "b" * 40
        self.early = {"source_heads": {".": self.deploy_sha, "agent-comm-platform/agent-comm": self.sdk_sha},
                      "packages": {"agent-comm-runtime": "0.1.4", "hermes-platform-agent-comm": "1.5.5"}}

    def test_semver_tag_gate(self):
        self.assertFalse(release.validate_tag("v0.7.0"))
        self.assertTrue(release.validate_tag("v0.7.0-rc.1"))
        for tag in ("main", "v1", "v01.2.3", "v1.2.3-01", "v1.2.3\n", "v1.2.3/escape", "v1.2.3;echo bad"):
            with self.subTest(tag=tag), self.assertRaises(ValueError):
                release.validate_tag(tag)

    def test_resolve_requires_existing_tag_and_matching_push(self):
        subprocess.run(["git", "init", "-q", str(self.root)], check=True)
        subprocess.run(["git", "-C", str(self.root), "-c", "user.name=Release test", "-c", "user.email=release@example.invalid",
                        "commit", "--allow-empty", "-qm", "fixture"], check=True)
        commit = release.git(self.root, "rev-parse", "HEAD")
        subprocess.run(["git", "-C", str(self.root), "tag", "v0.7.0"], check=True)
        result = release.resolve_release(self.root, "v0.7.0", "main", "push", commit)
        self.assertEqual(result["sdk_sha"], commit)
        self.assertEqual(result["prerelease"], "false")
        with self.assertRaises(subprocess.CalledProcessError):
            release.resolve_release(self.root, "v0.7.1", "main", "workflow_dispatch", commit)
        with self.assertRaises(ValueError):
            release.resolve_release(self.root, "main", "main", "workflow_dispatch", commit)
        with patch.object(release, "git", side_effect=[commit, "f" * 40]), self.assertRaises(ValueError):
            release.resolve_release(self.root, "v0.7.0", "main", "push", commit)
        with self.assertRaises(ValueError):
            release.resolve_release(self.root, "v0.7.0", "main\nother=value", "push", commit)

    def test_source_identity_checks_committed_gitlinks_not_just_worktree(self):
        sdk, deploy = self.root / "sdk", self.root / "deploy"
        platform = deploy / "agent-comm-platform"
        nested = platform / "agent-comm"
        values = [str(sdk), self.sdk_sha, "", str(nested), self.sdk_sha, "", "c" * 40, "c" * 40, self.sdk_sha, self.deploy_sha]
        with patch.object(release, "git", side_effect=values):
            self.assertEqual(release.source_identity(sdk, deploy, self.sdk_sha), self.deploy_sha)
        for changed in (4, 6, 8):
            altered = values.copy()
            altered[changed] = "f" * 40
            with self.subTest(field=changed), patch.object(release, "git", side_effect=altered), self.assertRaises(ValueError):
                release.source_identity(sdk, deploy, self.sdk_sha)

    def test_helper_rejects_wrong_revision_architecture_or_dirty_build(self):
        helper = self.root / "helper"
        helper.write_bytes(b"fixture")
        info = "helper: go1.26.8\n\tpath\tgithub.com/BillShiyaoZhang/agent-comm/cmd/helper\n" + "".join(f"\tbuild\t{key}={value}\n" for key, value in {
            "GOOS": "linux", "GOARCH": "amd64", "CGO_ENABLED": "0",
            "vcs.revision": self.sdk_sha, "vcs.modified": "false"}.items())
        with patch.object(release.subprocess, "check_output", return_value=info):
            release.verify_helper(helper, "linux", "amd64", self.sdk_sha)
        for invalid in (info.replace("amd64", "arm64"), info.replace(self.sdk_sha, "f" * 40),
                        info.replace("modified=false", "modified=true"), "helper: no build info"):
            with self.subTest(info=invalid), patch.object(release.subprocess, "check_output", return_value=invalid), self.assertRaises(ValueError):
                release.verify_helper(helper, "linux", "amd64", self.sdk_sha)

    def manifest_fixture(self):
        output = self.root / "dist"
        output.mkdir()
        names = set(release.HELPERS) | release.BUNDLES | {release.SOURCE_BUNDLE,
                "agent_comm_runtime-0.1.4-py3-none-any.whl", "hermes_platform_agent_comm-1.5.5-py3-none-any.whl",
                "agent-comm-docs.zip", "release_manifest_fetch.py", "early-access-manifest.json"}
        for name in names:
            (output / name).write_bytes(name.encode())
        manifest = release.write_manifest(output, tag="v0.7.0", repository="owner/agent-comm",
                                          sdk_sha=self.sdk_sha, deployment_sha=self.deploy_sha, early=self.early)
        return output, manifest

    def test_manifest_remains_compatible_with_existing_downloader(self):
        output, manifest = self.manifest_fixture()
        loaded = fetch.load_manifest(str(output / "release-manifest.json"))
        self.assertEqual(len(loaded["assets"]), 15)
        self.assertEqual(len(list(output.iterdir())), 16)
        for name, (operating_system, architecture, _) in release.HELPERS.items():
            selected = fetch.select_asset(loaded["assets"], "helper-binary", operating_system, architecture)
            self.assertEqual(selected["name"], name)
        with self.assertRaises(SystemExit):
            fetch.select_asset(loaded["assets"], "binary", "linux", "amd64")
        for asset in manifest["assets"]:
            path = output / asset["name"]
            self.assertEqual(asset["size"], path.stat().st_size)
            self.assertEqual(asset["sha256"], hashlib.sha256(path.read_bytes()).hexdigest())
        with self.assertRaises(ValueError):
            release.classify("unexpected-secret.txt")
        with self.assertRaises(ValueError):
            release.write_manifest(output, tag="v0.7.0", repository="owner/agent-comm",
                                   sdk_sha=self.sdk_sha, deployment_sha=self.deploy_sha, early=self.early)

    def test_unpublished_guard_fails_closed_except_for_exact_404(self):
        with patch.dict("os.environ", {"GH_TOKEN": "test-token"}):
            for status in (401, 403, 429, 500):
                with self.subTest(status=status), patch.object(release, "urlopen", side_effect=HTTPError("url", status, "error", {}, None)), self.assertRaises(HTTPError):
                    release.ensure_unpublished("owner/agent-comm", "v0.7.0")
            with patch.object(release, "urlopen", side_effect=HTTPError("url", 404, "missing", {}, None)):
                release.ensure_unpublished("owner/agent-comm", "v0.7.0")
            response = Mock()
            response.__enter__ = Mock(return_value=response)
            response.__exit__ = Mock(return_value=False)
            with patch.object(release, "urlopen", return_value=response), self.assertRaises(ValueError):
                release.ensure_unpublished("owner/agent-comm", "v0.7.0")

    def test_publish_only_changes_new_id_after_all_uploads_verified(self):
        output, _ = self.manifest_fixture()
        notes = self.root / "notes.md"
        notes.write_text("A reviewed release.")
        calls = []

        def api(repo, endpoint, **kwargs):
            calls.append((endpoint, kwargs))
            if endpoint.startswith("git/ref/tags/"):
                return {"object": {"type": "commit", "sha": self.sdk_sha}}
            if endpoint == "releases":
                self.assertTrue(kwargs["payload"]["draft"])
                self.assertEqual(kwargs["payload"]["tag_name"], "v0.7.0")
                return {"id": 123, "draft": True}
            if "upload" in kwargs:
                return {"name": kwargs["upload"].name}
            if endpoint.endswith("?per_page=100"):
                return [{"name": p.name, "size": p.stat().st_size, "digest": "sha256:" + release.sha(p),
                         "state": "uploaded"} for p in output.iterdir()]
            self.assertEqual(endpoint, "releases/123")
            self.assertEqual(kwargs, {"method": "PATCH", "payload": {"draft": False}})
            return {"id": 123, "draft": False}

        with patch.object(release, "ensure_unpublished"), patch.object(release, "github_request", side_effect=api):
            release.publish(output, "v0.7.0", self.sdk_sha, "owner/agent-comm", notes)
        self.assertEqual(calls[-1][0], "releases/123")
        self.assertEqual(sum("upload" in kwargs for _, kwargs in calls), 16)
        calls.clear()
        with patch.object(release, "ensure_unpublished"), patch.object(release, "github_request", side_effect=[{"object": {"type": "commit", "sha": self.sdk_sha}}, {"id": 123, "draft": True}, RuntimeError("upload failed")]) as remote:
            with self.assertRaises(RuntimeError):
                release.publish(output, "v0.7.0", self.sdk_sha, "owner/agent-comm", notes)
            self.assertFalse(any(call.kwargs.get("method") == "PATCH" for call in remote.call_args_list))
        with patch.object(release, "ensure_unpublished"), patch.object(release, "verify_remote_tag"), patch.object(release, "github_request", side_effect=HTTPError("url", 422, "already exists", {}, None)) as remote:
            with self.assertRaises(HTTPError):
                release.publish(output, "v0.7.0", self.sdk_sha, "owner/agent-comm", notes)
            self.assertEqual(remote.call_count, 1)

    def test_tampered_assets_never_create_draft(self):
        output, _ = self.manifest_fixture()
        (output / "agent-comm-helper-linux-amd64").write_bytes(b"tampered")
        with patch.object(release, "github_request") as remote, self.assertRaises(ValueError):
            release.publish(output, "v0.7.0", self.sdk_sha, "owner/agent-comm", self.root / "notes.md")
        remote.assert_not_called()

    def test_remote_tag_is_peeled_and_must_still_match_the_release(self):
        annotated = {"object": {"type": "tag", "sha": "d" * 40}}
        commit = {"object": {"type": "commit", "sha": self.sdk_sha}}
        with patch.object(release, "github_request", side_effect=[annotated, commit]) as remote:
            release.verify_remote_tag("owner/agent-comm", "v0.7.0", self.sdk_sha)
            self.assertEqual(remote.call_args_list[1].args[1], "git/tags/" + "d" * 40)
        with patch.object(release, "github_request", return_value=commit), self.assertRaises(ValueError):
            release.verify_remote_tag("owner/agent-comm", "v0.7.0", "f" * 40)
        with patch.object(release, "github_request", side_effect=HTTPError("url", 404, "missing tag", {}, None)), self.assertRaises(HTTPError):
            release.verify_remote_tag("owner/agent-comm", "v0.7.0", self.sdk_sha)

    def test_incomplete_or_corrupt_github_assets_leave_draft_unpublished(self):
        output, _ = self.manifest_fixture()
        notes = self.root / "notes.md"
        notes.write_text("A reviewed release.")
        inventory = [{"name": path.name, "size": path.stat().st_size,
                      "digest": "sha256:" + release.sha(path), "state": "uploaded"}
                     for path in output.iterdir()]
        corrupt = [dict(asset) for asset in inventory]
        corrupt[0]["digest"] = "sha256:" + "0" * 64
        for uploaded in (inventory[:-1], corrupt):
            def api(repo, endpoint, **kwargs):
                if endpoint == "releases":
                    return {"id": 123, "draft": True}
                if "upload" in kwargs:
                    return {}
                return uploaded
            with self.subTest(inventory=uploaded), patch.object(release, "ensure_unpublished"), patch.object(release, "verify_remote_tag"), patch.object(release, "github_request", side_effect=api) as remote:
                with self.assertRaises(ValueError):
                    release.publish(output, "v0.7.0", self.sdk_sha, "owner/agent-comm", notes)
                self.assertFalse(any(call.kwargs.get("method") == "PATCH" for call in remote.call_args_list))

    def test_python_package_versions_are_consistent(self):
        sdk = Path(__file__).resolve().parents[1]
        runtime = tomllib.loads((sdk / "python/pyproject.toml").read_text())["project"]["version"]
        init = ast.parse((sdk / "python/agent_comm_runtime/__init__.py").read_text())
        versions = [ast.literal_eval(node.value) for node in init.body if isinstance(node, ast.Assign)
                    and any(isinstance(target, ast.Name) and target.id == "__version__" for target in node.targets)]
        self.assertEqual(versions, [runtime])
        setup = ast.parse((sdk / "connectors/hermes-platform/setup.py").read_text())
        call = next(node for node in ast.walk(setup) if isinstance(node, ast.Call)
                    and isinstance(node.func, ast.Name) and node.func.id == "setup")
        metadata = {kw.arg: ast.literal_eval(kw.value) for kw in call.keywords if kw.arg in {"version", "install_requires"}}
        plugin = (sdk / "connectors/hermes-platform/hermes_platform_agent_comm/plugin.yaml").read_text()
        self.assertEqual(re.search(r"^version:\s*(\S+)", plugin, re.MULTILINE).group(1), metadata["version"])
        self.assertIn(f"agent-comm-runtime>={runtime},<0.2", metadata["install_requires"])


if __name__ == "__main__":
    unittest.main()
