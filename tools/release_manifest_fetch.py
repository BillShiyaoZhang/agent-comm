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
import tempfile
from pathlib import Path, PurePosixPath
from urllib.error import URLError
from urllib.parse import urlsplit
from urllib.request import HTTPRedirectHandler, Request, build_opener

DOCS_ASSET_NAME = "agent-comm-docs.zip"
CHECKSUM_ASSET_NAME = "SHA256SUMS"
DEFAULT_INSTALL_NAME = "agent-comm.exe" if os.name == "nt" else "agent-comm"
MAX_ASSET_BYTES = 256 * 1024 * 1024


def secure_url(value: str) -> str:
    parsed = urlsplit(value)
    if parsed.scheme != "https" or not parsed.hostname or parsed.username or parsed.password or parsed.fragment:
        raise ValueError("Release downloads require an HTTPS URL without credentials or fragments")
    return value


class SecureRedirect(HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        secure_url(newurl)
        return super().redirect_request(req, fp, code, msg, headers, newurl)


def open_download(url: str):
    return build_opener(SecureRedirect()).open(
        Request(secure_url(url), headers={"User-Agent": "agent-comm-release-helper/1.0"}), timeout=30)


def safe_name(value: object) -> str:
    if (not isinstance(value, str) or not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._-]{0,199}", value)
            or value.endswith(".") or value.split(".")[0].upper() in
            {"CON", "PRN", "AUX", "NUL", *(f"COM{i}" for i in range(10)), *(f"LPT{i}" for i in range(10))}):
        raise ValueError("Release asset and install names must be plain portable filenames")
    return value

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
HELPER_BINARY_RE = re.compile(r"^agent-comm-helper-(linux|windows|darwin)-(amd64|arm64)(?:\.exe)?$")


def is_url(value: str) -> bool:
    return value.startswith("http://") or value.startswith("https://")


def read_text(source: str) -> str:
    if is_url(source):
        try:
            with open_download(source) as response:
                payload = response.read(1_000_001)
                if len(payload) > 1_000_000:
                    raise ValueError("Release manifest exceeds the size limit")
                return payload.decode("utf-8-sig")
        except URLError as err:
            raise SystemExit(f"failed to read manifest {source}: {err}") from err

    return Path(source).read_text(encoding="utf-8-sig")


def download_bytes(url: str) -> bytes:
    try:
        with open_download(url) as response:
            payload = response.read(MAX_ASSET_BYTES + 1)
            if len(payload) > MAX_ASSET_BYTES:
                raise ValueError("Release asset exceeds the size limit")
            return payload
    except URLError as err:
        raise SystemExit(f"failed to download {url}: {err}") from err


def download_to_file(url: str, dest_path: Path) -> None:
    try:
        with open_download(url) as response:
            total_size = int(response.headers.get("content-length", 0))
            if total_size > MAX_ASSET_BYTES:
                raise ValueError("Release asset exceeds the size limit")
            downloaded = 0
            chunk_size = 1024 * 64  # 64KB
            
            with open(dest_path, "wb") as f:
                while True:
                    chunk = response.read(chunk_size)
                    if not chunk:
                        break
                    downloaded += len(chunk)
                    if downloaded > MAX_ASSET_BYTES:
                        raise ValueError("Release asset exceeds the size limit")
                    f.write(chunk)
                    if total_size > 0:
                        percent = (downloaded / total_size) * 100
                        sys.stdout.write(f"\r[Fetch] Downloading {url.split('/')[-1]}: {percent:.1f}% ({downloaded}/{total_size} bytes)")
                        sys.stdout.flush()
            if total_size > 0:
                print()
    except Exception as err:
        dest_path.unlink(missing_ok=True)
        raise SystemExit(f"failed to download {url}: {err}") from err


def sha256_file(filepath: Path) -> str:
    h = hashlib.sha256()
    try:
        with open(filepath, "rb") as f:
            chunk_size = 1024 * 64
            while True:
                chunk = f.read(chunk_size)
                if not chunk:
                    break
                h.update(chunk)
        return h.hexdigest()
    except Exception as err:
        raise SystemExit(f"failed to calculate hash for {filepath}: {err}") from err


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
    if HELPER_BINARY_RE.match(name):
        return "helper-binary"
    return "other"


def normalize_asset(asset: dict[str, object]) -> dict[str, object]:
    normalized = dict(asset)
    safe_name(normalized.get("name"))
    if not isinstance(normalized.get("sha256"), str) or not re.fullmatch(r"[0-9a-f]{64}", normalized["sha256"]):
        raise ValueError("Each release asset requires a lowercase SHA256 digest")
    normalized.setdefault("kind", classify_asset(str(normalized["name"])))
    if normalized["kind"] == "binary" and "platform" not in normalized:
        match = BINARY_RE.match(str(normalized["name"]))
        if match:
            normalized["platform"] = {"os": match.group(1), "arch": match.group(2)}
    elif normalized["kind"] == "helper-binary" and "platform" not in normalized:
        match = HELPER_BINARY_RE.match(str(normalized["name"]))
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

    if kind in ("binary", "helper-binary"):
        candidates = [
            asset
            for asset in candidates
            if isinstance(asset.get("platform"), dict)
            and asset["platform"].get("os") == desired_os
            and asset["platform"].get("arch") == desired_arch
        ]

    if not candidates:
        if kind in ("binary", "helper-binary"):
            raise SystemExit(f"no binary asset found for {desired_os}/{desired_arch} of kind {kind}")
        raise SystemExit(f"no {kind} asset found in manifest")

    return candidates[0]


def download_asset(base_url: str, asset: dict[str, object], output_dir: Path, target_name: str | None = None) -> Path:
    asset = normalize_asset(asset)
    name = safe_name(asset["name"])
    destination_name = safe_name(target_name or name)
    destination = output_dir / destination_name
    url = secure_url(f"{base_url.rstrip('/')}/{name}")
    # Never follow a preexisting predictable .download symlink.
    with tempfile.NamedTemporaryFile(prefix=".agent-comm-", suffix=".download", dir=output_dir, delete=False) as temporary:
        temp_path = Path(temporary.name)
    try:
        download_to_file(url, temp_path)
        expected_sha = asset["sha256"]
        actual_sha = sha256_file(temp_path)
        if actual_sha != expected_sha:
            raise SystemExit(f"sha256 mismatch for {name}: expected {expected_sha}, got {actual_sha}")
        temp_path.replace(destination)
    finally:
        temp_path.unlink(missing_ok=True)
    return destination


def extract_docs(archive_path: Path, output_dir: Path) -> None:
    """Only the release's Markdown documentation may be unpacked."""
    import zipfile
    root = output_dir.resolve()
    with zipfile.ZipFile(archive_path) as archive:
        entries = archive.infolist()
        if len(entries) > 1000 or sum(entry.file_size for entry in entries) > 32 * 1024 * 1024:
            raise ValueError("Documentation archive exceeds extraction limits")
        destinations = []
        for entry in entries:
            # ZipInfo normalizes backslashes on Windows; validate original bytes.
            raw_name = entry.orig_filename
            path = PurePosixPath(raw_name)
            if (path.is_absolute() or "\\" in raw_name or "\x00" in raw_name or not path.parts
                    or any(part in {".", ".."} or ":" in part for part in raw_name.split("/"))
                    or stat.S_ISLNK(entry.external_attr >> 16)
                    or (not entry.is_dir() and path.suffix.lower() != ".md")
                    or entry.file_size > 8 * 1024 * 1024):
                raise ValueError("Unsafe documentation archive entry")
            for part in path.parts:
                safe_name(part)
            destination = (root / Path(*path.parts)).resolve()
            if not destination.is_relative_to(root):
                raise ValueError("Documentation archive escapes output directory")
            destinations.append((entry, destination))
        # Validate the entire archive before writing any entries.
        for entry, destination in destinations:
            if entry.is_dir():
                destination.mkdir(parents=True, exist_ok=True)
            else:
                destination.parent.mkdir(parents=True, exist_ok=True)
                destination.write_bytes(archive.read(entry))


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
    parser.add_argument("--helper", action="store_true", help="Download the companion agent-comm-helper binary instead of agent-comm")
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

    kind = "helper-binary" if args.helper else "binary"
    binary_asset = select_asset(assets, kind, desired_os, desired_arch)
    checksum_asset = select_asset(assets, "checksum")

    install_name = args.install_name
    if install_name == DEFAULT_INSTALL_NAME and args.helper:
        install_name = "agent-comm-helper.exe" if os.name == "nt" else "agent-comm-helper"

    binary_path = download_asset(release_base, binary_asset, output_dir, install_name)
    checksum_path = download_asset(release_base, checksum_asset, output_dir)

    docs_path = None
    if args.include_docs:
        docs_asset = select_asset(assets, "docs")
        docs_path = download_asset(release_base, docs_asset, output_dir)
        try:
            extract_docs(docs_path, output_dir)
            docs_path.unlink(missing_ok=True)
            print("extracted documentation files directly into output directory")
        except Exception as err:
            raise SystemExit(f"failed to extract documentation zip: {err}") from err

    set_executable(binary_path)

    print(f"downloaded binary: {binary_path}")
    print(f"downloaded checksums: {checksum_path}")
    if docs_path:
        print("extracted documentation files")
    print(f"platform: {desired_os}/{desired_arch}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
