#!/usr/bin/env python3

"""Fetch agent-comm release assets from a release manifest.

The helper downloads the matching platform binary and SHA256SUMS file,
verifies their hashes against the manifest, and can optionally fetch the docs
bundle for offline use.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import platform
import re
import stat
import sys
from pathlib import Path
from urllib.error import URLError
from urllib.request import Request, urlopen

DOCS_ASSET_NAME = "agent-comm-docs.zip"
CHECKSUM_ASSET_NAME = "SHA256SUMS"
DEFAULT_INSTALL_NAME = "agent-comm.exe" if os.name == "nt" else "agent-comm"

PLATFORM_ALIASES = {
    "linux": "linux",
    "windows": "windows",
    "darwin": "darwin",
}

ARCH_ALIASES = {
    "x86_64": "amd64",
    "amd64": "amd64",
    "arm64": "arm64",
    "aarch64": "arm64",
}

BINARY_RE = re.compile(r"^agent-comm-(linux|windows|darwin)-(amd64|arm64)(?:\.exe)?$")


def is_url(value: str) -> bool:
    return value.startswith("http://") or value.startswith("https://")


def read_text(source: str) -> str:
    if is_url(source):
        try:
            with urlopen(Request(source, headers={"User-Agent": "agent-comm-release-helper/1.0"})) as response:
                return response.read().decode("utf-8-sig")
        except URLError as err:
            raise SystemExit(f"failed to read manifest {source}: {err}") from err

    return Path(source).read_text(encoding="utf-8-sig")


def download_bytes(url: str) -> bytes:
    try:
        with urlopen(Request(url, headers={"User-Agent": "agent-comm-release-helper/1.0"})) as response:
            return response.read()
    except URLError as err:
        raise SystemExit(f"failed to download {url}: {err}") from err


def sha256_hex(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def normalize_platform() -> tuple[str, str]:
    system_name = PLATFORM_ALIASES.get(platform.system().lower())
    arch_name = ARCH_ALIASES.get(platform.machine().lower())

    if not system_name:
        raise SystemExit(f"unsupported operating system: {platform.system()}")
    if not arch_name:
        raise SystemExit(f"unsupported architecture: {platform.machine()}")

    return system_name, arch_name


def classify_asset(name: str) -> str:
    if name == CHECKSUM_ASSET_NAME:
        return "checksum"
    if name == DOCS_ASSET_NAME:
        return "docs"
    if name.endswith(".py"):
        return "helper"
    if BINARY_RE.match(name):
        return "binary"
    return "other"


def normalize_asset(asset: dict[str, object]) -> dict[str, object]:
    normalized = dict(asset)
    normalized.setdefault("kind", classify_asset(str(normalized["name"])))
    if normalized["kind"] == "binary" and "platform" not in normalized:
        match = BINARY_RE.match(str(normalized["name"]))
        if match:
            normalized["platform"] = {"os": match.group(1), "arch": match.group(2)}
    return normalized


def load_manifest(source: str) -> dict[str, object]:
    manifest = json.loads(read_text(source))
    if not isinstance(manifest, dict):
        raise SystemExit("manifest must be a JSON object")

    assets = manifest.get("assets")
    if not isinstance(assets, list):
        raise SystemExit("manifest is missing an assets array")

    manifest["assets"] = [normalize_asset(asset) for asset in assets if isinstance(asset, dict)]
    return manifest


def resolve_release_base(manifest: dict[str, object], manifest_source: str, repo_override: str | None) -> str:
    release_url = manifest.get("release_url")
    if isinstance(release_url, str) and release_url:
        return release_url.rstrip("/")

    repository = repo_override or manifest.get("repository")
    version = manifest.get("version")
    if isinstance(repository, str) and isinstance(version, str) and repository and version:
        return f"https://github.com/{repository}/releases/download/{version}"

    if is_url(manifest_source):
        return manifest_source.rsplit("/", 1)[0]

    raise SystemExit("manifest does not provide enough repository metadata; pass --repo or use a manifest with release_url")


def select_asset(assets: list[dict[str, object]], kind: str, desired_os: str | None = None, desired_arch: str | None = None) -> dict[str, object]:
    candidates = [asset for asset in assets if asset.get("kind") == kind]

    if kind == "binary":
        candidates = [
            asset
            for asset in candidates
            if isinstance(asset.get("platform"), dict)
            and asset["platform"].get("os") == desired_os
            and asset["platform"].get("arch") == desired_arch
        ]

    if not candidates:
        if kind == "binary":
            raise SystemExit(f"no binary asset found for {desired_os}/{desired_arch}")
        raise SystemExit(f"no {kind} asset found in manifest")

    return candidates[0]


def download_asset(base_url: str, asset: dict[str, object], output_dir: Path, target_name: str | None = None) -> Path:
    name = str(asset["name"])
    destination_name = target_name or name
    destination = output_dir / destination_name
    temp_path = output_dir / f".{destination_name}.download"
    url = f"{base_url.rstrip('/')}/{name}"
    payload = download_bytes(url)

    temp_path.write_bytes(payload)
    expected_sha = str(asset.get("sha256", ""))
    actual_sha = sha256_hex(payload)
    if expected_sha and actual_sha != expected_sha:
        temp_path.unlink(missing_ok=True)
        raise SystemExit(f"sha256 mismatch for {name}: expected {expected_sha}, got {actual_sha}")

    temp_path.replace(destination)
    return destination


def set_executable(path: Path) -> None:
    if os.name == "nt":
        return

    mode = path.stat().st_mode
    path.chmod(mode | stat.S_IRUSR | stat.S_IWUSR | stat.S_IXUSR | stat.S_IXGRP | stat.S_IXOTH)


def print_asset_list(manifest: dict[str, object]) -> None:
    version = manifest.get("version", "unknown")
    print(f"agent-comm release {version}")
    for asset in manifest["assets"]:
        platform_info = asset.get("platform")
        platform_text = ""
        if isinstance(platform_info, dict):
            platform_text = f" [{platform_info.get('os')}/{platform_info.get('arch')}]"
        print(f"- {asset['name']} ({asset.get('kind', 'unknown')}){platform_text}")


def main() -> int:
    parser = argparse.ArgumentParser(description="Download agent-comm release assets from a manifest.")
    parser.add_argument("--manifest", required=True, help="Path or URL to release-manifest.json")
    parser.add_argument("--output-dir", default=".", help="Directory to store the downloaded assets")
    parser.add_argument("--install-name", default=DEFAULT_INSTALL_NAME, help="Name to use for the installed binary")
    parser.add_argument("--repo", help="Override the repository if the manifest omits release_url metadata")
    parser.add_argument("--include-docs", action="store_true", help="Download the optional docs bundle too")
    parser.add_argument("--list-assets", action="store_true", help="List manifest assets and exit")
    args = parser.parse_args()

    manifest = load_manifest(args.manifest)
    if args.list_assets:
        print_asset_list(manifest)
        return 0

    desired_os, desired_arch = normalize_platform()
    assets = manifest["assets"]
    output_dir = Path(args.output_dir).expanduser().resolve()
    output_dir.mkdir(parents=True, exist_ok=True)
    release_base = resolve_release_base(manifest, args.manifest, args.repo)

    binary_asset = select_asset(assets, "binary", desired_os, desired_arch)
    checksum_asset = select_asset(assets, "checksum")

    binary_path = download_asset(release_base, binary_asset, output_dir, args.install_name)
    checksum_path = download_asset(release_base, checksum_asset, output_dir)

    docs_path = None
    if args.include_docs:
        docs_asset = select_asset(assets, "docs")
        docs_path = download_asset(release_base, docs_asset, output_dir)
        try:
            import zipfile
            with zipfile.ZipFile(docs_path, 'r') as zip_ref:
                zip_ref.extractall(output_dir)
            docs_path.unlink(missing_ok=True)
            print("extracted documentation files directly into output directory")
        except Exception as err:
            print(f"failed to extract documentation zip: {err}")

    set_executable(binary_path)

    print(f"downloaded binary: {binary_path}")
    print(f"downloaded checksums: {checksum_path}")
    if docs_path:
        print("extracted documentation files")
    print(f"platform: {desired_os}/{desired_arch}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())