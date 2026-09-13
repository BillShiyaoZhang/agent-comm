"""Exercise real Hermes user-plugin discovery/config propagation in a clean subprocess."""
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest


class TestDiscovery(unittest.TestCase):
    def test_user_plugin_discovery_and_nested_config(self):
        with tempfile.TemporaryDirectory(prefix="agent-comm-discovery-") as directory:
            home = Path(directory)
            source = Path(__file__).resolve().parents[1] / "hermes_platform_agent_comm"
            shutil.copytree(source, home / "plugins" / "agent_comm")
            (home / "config.yaml").write_text(json.dumps({
                "plugins": {"enabled": ["agent_comm"]},
                "platforms": {"agent_comm": {"enabled": True, "extra": {
                    "platform_url": "http://127.0.0.1:45042",
                    "allow_from": ["urn:agent-comm:agent:chosen-peer"],
                    "state_path": str(home / "receipts.sqlite3"),
                }}},
            }), encoding="utf-8")
            env = {key: value for key, value in os.environ.items() if key.upper() in {
                "SYSTEMROOT", "WINDIR", "PATH", "COMSPEC", "PATHEXT", "PYTHONPATH", "TEMP", "TMP",
            }}
            env.update(HERMES_HOME=str(home), HOME=str(home), USERPROFILE=str(home),
                       LOCALAPPDATA=str(home), APPDATA=str(home), PYTHONDONTWRITEBYTECODE="1")
            script = r'''
import json, socket, sys

def audit(event, args):
    if event == "socket.connect":
        raise RuntimeError("Plugin discovery test forbids network access")
sys.addaudithook(audit)
from hermes_cli.plugins import discover_plugins, get_plugin_manager
from gateway.config import Platform, load_gateway_config
from gateway.platform_registry import platform_registry
discover_plugins()
plugins = [p for p in get_plugin_manager().list_plugins() if p["name"] == "agent_comm"]
assert len(plugins) == 1 and plugins[0]["enabled"] and not plugins[0]["error"], plugins
config = load_gateway_config()
cfg = config.platforms[Platform("agent_comm")]
assert cfg.enabled
assert cfg.extra["platform_url"] == "http://127.0.0.1:45042", cfg.extra
assert cfg.extra["allow_from"] == ["urn:agent-comm:agent:chosen-peer"], cfg.extra
adapter = platform_registry.create_adapter("agent_comm", cfg)
assert adapter.__class__.__name__ == "AgentCommAdapter"
assert Platform("agent_comm") in config.get_connected_platforms()
assert platform_registry.get("agent_comm").allow_update_command is False
print(json.dumps({"discovered": True, "config_loaded": True, "adapter_created": True}))
'''
            result = subprocess.run([sys.executable, "-c", script], cwd=home, env=env,
                                    capture_output=True, text=True, timeout=45)
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            self.assertIn('"adapter_created": true', result.stdout)


if __name__ == "__main__":
    unittest.main()
