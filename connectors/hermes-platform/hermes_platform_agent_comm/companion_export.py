"""Export the packaged Hermes companion into an explicit new directory.

This never edits configuration, enables a plugin, restarts Hermes, or sends a
notification. Use a staging path to inspect the artifact before installation.
"""
import argparse
from importlib.resources import files
import json
from pathlib import Path


ASSETS = ("__init__.py", "plugin.yaml", "dashboard/manifest.json",
          "dashboard/plugin_api.py", "dashboard/index.js", "desktop/plugin.js")


def export_companion(output):
    target = Path(output).expanduser().resolve()
    if target.exists():
        raise ValueError("Output already exists; choose a new directory to preserve the installed plugin")
    source = files("hermes_platform_agent_comm").joinpath("companion")
    # Read every bundled file before any destination mutation.
    payloads = {name: source.joinpath(name).read_bytes() for name in ASSETS}
    target.mkdir(parents=True)
    for name, data in payloads.items():
        destination = target / name
        destination.parent.mkdir(parents=True, exist_ok=True)
        destination.write_bytes(data)
    return {"status": "exported_not_enabled", "plugin": "agent-comm-attention", "path": str(target),
            "instruction": "Install this directory as a trusted Hermes user plugin; enable its backend and Desktop companion explicitly. Configuration was not changed."}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", required=True, help="New directory for the companion package")
    args = parser.parse_args()
    try:
        print(json.dumps(export_companion(args.output), ensure_ascii=False))
    except (ValueError, OSError) as exc:
        parser.exit(1, str(exc) + "\n")


if __name__ == "__main__":
    main()
