#!/usr/bin/env python3
"""Assemble client releases from tested artifacts and exact repository pins.

The deployment repository owns installation-bundle construction. Publishing
creates a new draft and addresses its immutable ID; existing releases are never
updated. No command here creates tags or changes remote refs.
"""
from __future__ import annotations

import argparse
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile
from urllib.error import HTTPError
from urllib.parse import quote, urlsplit
from urllib.request import Request, urlopen
import zipfile


SEMVER = re.compile(r"v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?\Z")
HELPERS = {
    "agent-comm-helper-linux-amd64": ("linux", "amd64", "agent-comm-helper-linux-amd64"),
    "agent-comm-helper-windows-amd64.exe": ("windows", "amd64", "agent-comm-helper.exe"),
    "agent-comm-helper-darwin-amd64": ("darwin", "amd64", "agent-comm-helper-darwin-amd64"),
    "agent-comm-helper-darwin-arm64": ("darwin", "arm64", "agent-comm-helper-darwin-arm64"),
}
PACKAGES = {"agent_comm_runtime": "python", "hermes_platform_agent_comm": "connectors/hermes-platform"}
BUNDLES = {f"agent-comm-early-access-{platform}.zip" for platform in
           ("windows-amd64", "linux-amd64", "macos-amd64", "macos-arm64")}
SOURCE_BUNDLE = "agent-comm-early-access-source.zip"
DOCS = {"README.md", "README_EN.md", "SKILL.md", "SKILL_EN.md", "python/README.md",
        "connectors/README.md", "connectors/hermes-platform/README.md",
        "connectors/openclaw-channel/README.md", "examples/README.md", "tests/README.md",
        "tools/README.md", "connectors/hermes-platform/hermes_platform_agent_comm/skills/personal-collaboration/SKILL.md"}
POLICY_TRUST_FIELDS = {"schema_version", "release", "platform_origin", "platform_peer_id",
                       "policy_root_public_key_hex", "verification_note"}
DEPLOYMENT_REMOTE = "https://github.com/BillShiyaoZhang/agent-collaboration-deploy.git"


def git(repo, *args):
    return subprocess.check_output(["git", "-C", str(repo), *args], text=True).strip()


def validate_tag(tag):
    match = SEMVER.fullmatch(tag)
    if not match or any(part.isdigit() and len(part) > 1 and part[0] == "0"
                        for part in (match.group(4) or "").split(".")):
        raise ValueError("release_tag must be an existing vMAJOR.MINOR.PATCH semantic version tag")
    return bool(match.group(4))


def resolve_deployment_sha(deployment_ref, event):
    """Resolve the deploy source once, before jobs can observe a newer main."""
    if event == "workflow_dispatch":
        if not re.fullmatch(r"[0-9a-f]{40}", deployment_ref):
            raise ValueError("Manual releases require an exact 40-character deployment commit SHA")
        return deployment_ref
    if deployment_ref != "main":
        raise ValueError("Tag-triggered releases must resolve deployment main")
    output = subprocess.check_output(
        ["git", "ls-remote", "--exit-code", DEPLOYMENT_REMOTE, "refs/heads/main"], text=True)
    match = re.fullmatch(r"([0-9a-f]{40})\trefs/heads/main\n?", output)
    if not match:
        raise ValueError("Could not resolve the exact deployment main commit")
    return match.group(1)


def resolve_release(repo, tag, deployment_ref, event, expected_sha):
    prerelease = validate_tag(tag)
    if event not in {"push", "workflow_dispatch"}:
        raise ValueError("Unsupported release event")
    sdk_sha = git(repo, "rev-parse", "--verify", f"refs/tags/{tag}^{{commit}}")
    if event == "push" and sdk_sha != git(repo, "rev-parse", "--verify", f"{expected_sha}^{{commit}}"):
        raise ValueError("Tag commit differs from the triggering push")
    deployment_sha = resolve_deployment_sha(deployment_ref, event)
    return {"tag": tag, "sdk_sha": sdk_sha, "prerelease": str(prerelease).lower(),
            "deployment_sha": deployment_sha}


def source_identity(sdk, deployment, sdk_sha):
    if not re.fullmatch(r"[0-9a-f]{40}", sdk_sha):
        raise ValueError("Expected a full SDK commit SHA")
    platform = deployment / "agent-comm-platform"
    nested = platform / "agent-comm"
    for repo in (sdk, nested):
        if Path(git(repo, "rev-parse", "--show-toplevel")).resolve() != repo.resolve():
            raise ValueError(f"Missing exact SDK checkout: {repo}")
        if git(repo, "rev-parse", "HEAD") != sdk_sha:
            raise ValueError("Deployment SDK pin differs from the release tag commit")
        if git(repo, "status", "--porcelain", "--untracked-files=all"):
            raise ValueError("SDK release source must be committed and clean")
    if (git(deployment, "rev-parse", "HEAD:agent-comm-platform") != git(platform, "rev-parse", "HEAD")
            or git(platform, "rev-parse", "HEAD:agent-comm") != sdk_sha):
        raise ValueError("Committed deployment gitlinks differ from the release checkout")
    return git(deployment, "rev-parse", "HEAD")


def sha(path):
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def require_regular(path):
    if path.is_symlink() or not path.is_file():
        raise ValueError(f"Expected a regular release asset: {path}")
    return path


def reviewed_policy_trust(deployment, tag):
    """Read public anchors from the exact committed deployment source, never the Platform."""
    validate_tag(tag)
    relative = f"tools/release/trust/{tag}.json"
    require_regular(deployment / relative)
    raw = subprocess.check_output(["git", "-c", "safe.directory=" + deployment.resolve().as_posix(),
                                   "-C", str(deployment), "show", f"HEAD:{relative}"])
    if len(raw) > 8192:
        raise ValueError("Reviewed policy trust file is too large")
    try:
        trust = json.loads(raw)
    except (ValueError, UnicodeDecodeError) as exc:
        raise ValueError("Invalid reviewed policy trust JSON") from exc
    if (not isinstance(trust, dict) or set(trust) != POLICY_TRUST_FIELDS
            or trust.get("schema_version") != 1 or trust.get("release") != tag):
        raise ValueError("Policy trust schema or release tag mismatch")
    root = trust["policy_root_public_key_hex"]
    peer = trust["platform_peer_id"]
    origin = trust["platform_origin"]
    note = trust["verification_note"]
    if not isinstance(root, str) or not re.fullmatch(r"[0-9a-f]{64}", root):
        raise ValueError("Invalid reviewed policy root public key")
    if not isinstance(peer, str) or not re.fullmatch(r"[1-9A-HJ-NP-Za-km-z]{32,80}", peer):
        raise ValueError("Invalid reviewed Platform Peer ID")
    if not isinstance(origin, str):
        raise ValueError("Invalid reviewed Platform origin")
    parsed = urlsplit(origin)
    if (parsed.scheme != "https" or not parsed.hostname or parsed.username or parsed.password
            or parsed.path or parsed.query or parsed.fragment or origin != f"https://{parsed.netloc}"):
        raise ValueError("Invalid reviewed Platform origin")
    if (not isinstance(note, str) or not note.strip() or note != note.strip() or len(note) > 512
            or any(ord(char) < 32 or ord(char) == 127 for char in note)):
        raise ValueError("Invalid reviewed trust provenance")
    metadata = {"release": tag, "file": "policy-trust.json",
                "file_sha256": hashlib.sha256(raw).hexdigest(),
                "root_public_key_sha256": hashlib.sha256(bytes.fromhex(root)).hexdigest(),
                "platform_peer_id": peer, "platform_origin": origin}
    return raw, metadata


def verify_bundle_policy_trust(bundle, expected, tag):
    """Check the installed bytes and inner checksum in every platform ZIP."""
    with zipfile.ZipFile(require_regular(bundle)) as archive:
        names = archive.namelist()
        if names.count("policy-trust.json") != 1 or names.count("SHA256SUMS.json") != 1:
            raise ValueError(f"Missing or duplicate policy trust metadata: {bundle.name}")
        actual = archive.read("policy-trust.json")
        if actual != expected:
            raise ValueError(f"Policy trust bytes differ from reviewed source: {bundle.name}")
        checksums = json.loads(archive.read("SHA256SUMS.json"))
        if (not isinstance(checksums, dict) or checksums.get("release") != tag
                or not isinstance(checksums.get("files"), dict)
                or checksums["files"].get("policy-trust.json") != hashlib.sha256(actual).hexdigest()):
            raise ValueError(f"Policy trust is not covered by this bundle checksum: {bundle.name}")


def verify_helper(path, operating_system, architecture, sdk_sha):
    require_regular(path)
    info = subprocess.check_output(["go", "version", "-m", str(path)], text=True)
    if not re.search(r"^\s*path\s+github\.com/BillShiyaoZhang/agent-comm/cmd/helper$", info, re.MULTILINE):
        raise ValueError(f"Artifact is not the helper program: {path.name}")
    fields = {}
    for line in info.splitlines():
        match = re.fullmatch(r"\s*build\s+([^=]+)=(.*)", line)
        if match:
            fields[match.group(1)] = match.group(2)
    expected = {"GOOS": operating_system, "GOARCH": architecture, "CGO_ENABLED": "0",
                "vcs.revision": sdk_sha, "vcs.modified": "false"}
    if any(fields.get(key) != value for key, value in expected.items()):
        raise ValueError(f"Helper platform or source provenance mismatch: {path.name}")


def package_docs(sdk, output):
    names = DOCS | {name for name in git(sdk, "ls-files", "docs", "references").splitlines()
                    if name.endswith(".md")}
    with zipfile.ZipFile(output, "w", compression=zipfile.ZIP_DEFLATED) as archive:
        for name in sorted(names):
            archive.write(require_regular(sdk / name), arcname=name)


def classify(name):
    if name in HELPERS:
        operating_system, architecture, _ = HELPERS[name]
        return {"kind": "helper-binary", "platform": {"os": operating_system, "arch": architecture}}
    if name in BUNDLES:
        return {"kind": "bundle"}
    if name == SOURCE_BUNDLE:
        return {"kind": "source"}
    if name.endswith(".whl") and any(name.startswith(package + "-") for package in PACKAGES):
        return {"kind": "wheel"}
    known = {"agent-comm-docs.zip": "docs", "release_manifest_fetch.py": "helper",
             "early-access-manifest.json": "metadata", "SHA256SUMS": "checksum"}
    if name not in known:
        raise ValueError(f"Unexpected release file: {name}")
    return {"kind": known[name]}


def write_manifest(output, *, tag, repository, sdk_sha, deployment_sha, early, policy_trust=None):
    validate_tag(tag)
    if not re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", repository):
        raise ValueError("Invalid GitHub repository")
    assets = []
    for path in sorted(output.iterdir()):
        require_regular(path)
        if path.name in {"release-manifest.json", "SHA256SUMS"}:
            raise ValueError("Refusing to overwrite existing release metadata")
        assets.append({"name": path.name, "sha256": sha(path), "size": path.stat().st_size,
                       **classify(path.name)})
    checksum = output / "SHA256SUMS"
    checksum.write_text("".join(f"{asset['sha256']}  {asset['name']}\n" for asset in assets), encoding="utf-8")
    assets.append({"name": checksum.name, "sha256": sha(checksum), "size": checksum.stat().st_size,
                   "kind": "checksum"})
    manifest = {"schema_version": 1, "name": "agent-comm", "version": tag, "repository": repository,
                "release_url": f"https://github.com/{repository}/releases/download/{tag}",
                "source_commit": sdk_sha, "deployment_commit": deployment_sha,
                "source_heads": early["source_heads"], "packages": early["packages"],
                "generated_at": datetime.now(timezone.utc).isoformat().replace("+00:00", "Z"),
                "checksum_file": "SHA256SUMS", "assets": assets}
    if policy_trust is not None:
        manifest["policy_trust"] = policy_trust
    (output / "release-manifest.json").write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")
    return manifest


def assemble(sdk, deployment, artifacts, output, tag, sdk_sha, repository):
    validate_tag(tag)
    require_regular(sdk / "docs" / "releases" / f"{tag}.md")
    deployment_sha = source_identity(sdk, deployment, sdk_sha)
    trust_bytes, trust_metadata = reviewed_policy_trust(deployment, tag)
    if output.exists() and any(output.iterdir()):
        raise ValueError("Release output must be empty")
    wheels = {}
    for package in PACKAGES:
        found = list(artifacts.glob(f"{package}-*.whl"))
        if len(found) != 1:
            raise ValueError(f"Expected exactly one wheel for {package}")
        wheels[package] = require_regular(found[0])
    expected = set(HELPERS) | {path.name for path in wheels.values()}
    if {path.name for path in artifacts.iterdir()} != expected:
        raise ValueError("Build artifacts must contain exactly four helpers and two wheels")
    for name, (operating_system, architecture, _) in HELPERS.items():
        verify_helper(artifacts / name, operating_system, architecture, sdk_sha)
    helpers = deployment / "build" / "release-helpers"
    helpers.mkdir(parents=True, exist_ok=True)
    for name, (_, _, install_name) in HELPERS.items():
        shutil.copyfile(artifacts / name, helpers / install_name)
    nested = deployment / "agent-comm-platform" / "agent-comm"
    for package, wheel in wheels.items():
        destination = nested / PACKAGES[package] / "dist"
        destination.mkdir(exist_ok=True)
        shutil.copyfile(wheel, destination / wheel.name)
    bundles = deployment / "build" / "github-bundles"
    if bundles.exists() and any(bundles.iterdir()):
        raise ValueError("Installation bundle output must be empty")
    with tempfile.TemporaryDirectory(prefix="release-trust-", dir=deployment / "build") as temporary:
        trust_path = Path(temporary) / f"{tag}.json"
        trust_path.write_bytes(trust_bytes)
        subprocess.run([sys.executable, str(deployment / "tools/release/build_early_access.py"),
                        "--release", tag, "--helper-dir", str(helpers), "--output-dir", str(bundles),
                        "--policy-trust", str(trust_path)], check=True)
    early = json.loads((bundles / "release-manifest.json").read_text(encoding="utf-8"))
    if (early.get("release") != tag or early.get("source_heads", {}).get(".") != deployment_sha
            or early.get("source_heads", {}).get("agent-comm-platform/agent-comm") != sdk_sha):
        raise ValueError("Installation bundle source identity mismatch")
    if set(early.get("files", {})) != BUNDLES | {SOURCE_BUNDLE}:
        raise ValueError("Expected four complete installation ZIPs and one source ZIP")
    for name in BUNDLES:
        verify_bundle_policy_trust(bundles / name, trust_bytes, tag)
    early["policy_trust"] = trust_metadata
    output.mkdir(parents=True, exist_ok=True)
    for name in sorted(expected):
        shutil.copyfile(artifacts / name, output / name)
    for name, entry in early["files"].items():
        path = require_regular(bundles / name)
        if sha(path) != entry["sha256"] or path.stat().st_size != entry["bytes"]:
            raise ValueError(f"Installation bundle checksum mismatch: {name}")
        shutil.copyfile(path, output / name)
    (output / "early-access-manifest.json").write_text(json.dumps(early, indent=2) + "\n", encoding="utf-8")
    shutil.copyfile(require_regular(sdk / "tools/release_manifest_fetch.py"), output / "release_manifest_fetch.py")
    package_docs(sdk, output / "agent-comm-docs.zip")
    manifest = write_manifest(output, tag=tag, repository=repository, sdk_sha=sdk_sha,
                              deployment_sha=deployment_sha, early=early, policy_trust=trust_metadata)
    print(json.dumps({"tag": tag, "sdk_sha": sdk_sha, "deployment_sha": deployment_sha,
                      "packages": early["packages"], "assets": [a["name"] for a in manifest["assets"]]}, indent=2))


def ensure_unpublished(repository, tag):
    validate_tag(tag)
    if not re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", repository):
        raise ValueError("Invalid GitHub repository")
    request = Request(f"https://api.github.com/repos/{repository}/releases/tags/{tag}",
                      headers={"Accept": "application/vnd.github+json", "User-Agent": "agent-comm-release",
                               "Authorization": "Bearer " + os.environ["GH_TOKEN"]})
    try:
        with urlopen(request, timeout=30):
            raise ValueError("This tag already has a release; existing assets must not be replaced")
    except HTTPError as error:
        if error.code != 404:
            raise


def github_request(repository, endpoint, *, method="GET", payload=None, upload=None):
    origin = "https://uploads.github.com" if upload is not None else "https://api.github.com"
    headers = {"Accept": "application/vnd.github+json", "User-Agent": "agent-comm-release",
               "Authorization": "Bearer " + os.environ["GH_TOKEN"]}
    data = None
    if upload is not None:
        headers["Content-Type"] = "application/octet-stream"
        data = upload.read_bytes()
    elif payload is not None:
        headers["Content-Type"] = "application/json"
        data = json.dumps(payload).encode()
    request = Request(f"{origin}/repos/{repository}/{endpoint}", data=data, headers=headers, method=method)
    with urlopen(request, timeout=300) as response:
        return json.load(response)


def verify_remote_tag(repository, tag, sdk_sha):
    if not re.fullmatch(r"[0-9a-f]{40}", sdk_sha):
        raise ValueError("Expected a full SDK commit SHA")
    ref = github_request(repository, f"git/ref/tags/{quote(tag, safe='')}")["object"]
    for _ in range(8):
        if ref.get("type") == "commit":
            if ref.get("sha") != sdk_sha:
                raise ValueError("Remote release tag no longer matches the verified SDK commit")
            return
        if ref.get("type") != "tag" or not re.fullmatch(r"[0-9a-f]{40}", ref.get("sha", "")):
            break
        ref = github_request(repository, f"git/tags/{ref['sha']}")["object"]
    raise ValueError("Release tag does not resolve to a commit")


def publish(output, tag, sdk_sha, repository, notes):
    prerelease = validate_tag(tag)
    if not re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", repository):
        raise ValueError("Invalid GitHub repository")
    manifest_path = require_regular(output / "release-manifest.json")
    manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    if (manifest.get("version") != tag or manifest.get("source_commit") != sdk_sha
            or manifest.get("repository") != repository):
        raise ValueError("Release manifest identity does not match publication")
    files = {}
    for asset in manifest["assets"]:
        name = asset["name"]
        if Path(name).name != name or name in files:
            raise ValueError("Invalid or duplicate asset name")
        classify(name)
        path = require_regular(output / name)
        if sha(path) != asset["sha256"] or path.stat().st_size != asset["size"]:
            raise ValueError(f"Release asset changed after verification: {name}")
        files[name] = path
    files[manifest_path.name] = manifest_path
    if {path.name for path in output.iterdir()} != set(files):
        raise ValueError("Unlisted release files")
    body = require_regular(notes).read_text(encoding="utf-8").strip()
    if not body:
        raise ValueError("Release notes must not be empty")
    verify_remote_tag(repository, tag, sdk_sha)
    ensure_unpublished(repository, tag)
    # POST is intentionally create-only. A competing creation returns 422;
    # no subsequent step may resolve this tag and overwrite somebody's release.
    release = github_request(repository, "releases", method="POST", payload={
        "tag_name": tag, "target_commitish": sdk_sha, "name": tag, "body": body,
        "draft": True, "prerelease": prerelease})
    release_id = release["id"]
    if not isinstance(release_id, int) or release_id <= 0 or release.get("draft") is not True:
        raise ValueError("GitHub did not return the newly created draft")
    print(f"Created draft release {release_id}; upload failures leave it unpublished.", flush=True)
    expected = {name: (path.stat().st_size, sha(path)) for name, path in files.items()}
    for name, path in files.items():
        github_request(repository, f"releases/{release_id}/assets?name={quote(name, safe='')}",
                       method="POST", upload=path)
    uploaded = github_request(repository, f"releases/{release_id}/assets?per_page=100")
    if len(uploaded) != len(expected) or {asset["name"] for asset in uploaded} != set(expected):
        raise ValueError("Draft asset inventory mismatch; leaving draft unpublished")
    for asset in uploaded:
        size, digest = expected[asset["name"]]
        if (asset.get("state") != "uploaded" or asset.get("size") != size
                or asset.get("digest") != "sha256:" + digest):
            raise ValueError("GitHub asset verification failed; leaving draft unpublished")
    verify_remote_tag(repository, tag, sdk_sha)
    published = github_request(repository, f"releases/{release_id}", method="PATCH", payload={"draft": False})
    if published.get("id") != release_id or published.get("draft") is not False:
        raise ValueError("GitHub did not confirm publication")
    print(published.get("html_url", f"Release {release_id} published"))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    commands.add_parser("resolve")
    build = commands.add_parser("assemble")
    for option in ("sdk", "deployment", "artifacts", "output"):
        build.add_argument("--" + option, required=True, type=lambda value: Path(value).resolve())
    for option in ("tag", "sdk-sha", "repository"):
        build.add_argument("--" + option, required=True)
    check = commands.add_parser("ensure-unpublished")
    check.add_argument("--tag", required=True)
    check.add_argument("--repository", required=True)
    release = commands.add_parser("publish")
    for option in ("output", "notes"):
        release.add_argument("--" + option, required=True, type=lambda value: Path(value).resolve())
    for option in ("tag", "sdk-sha", "repository"):
        release.add_argument("--" + option, required=True)
    args = vars(parser.parse_args())
    command = args.pop("command")
    if command == "resolve":
        result = resolve_release(Path.cwd(), os.environ["RELEASE_TAG"], os.environ["DEPLOYMENT_REF"],
                                 os.environ["RELEASE_EVENT"], os.environ["RELEASE_SHA"])
        with open(os.environ["GITHUB_OUTPUT"], "a", encoding="utf-8") as stream:
            for key, value in result.items():
                stream.write(f"{key}={value}\n")
        print(json.dumps(result))
    elif command == "assemble":
        assemble(**args)
    elif command == "publish":
        publish(**args)
    else:
        ensure_unpublished(**args)


if __name__ == "__main__":
    main()
