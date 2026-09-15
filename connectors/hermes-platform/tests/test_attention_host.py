"""Exercise the real Hermes auth/profile resolver against temporary Store data."""
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


class RealAttentionHostTests(unittest.TestCase):
    def test_real_dashboard_auth_profile_isolation_and_native_recovery(self):
        code = r'''
import json, os, sys
from pathlib import Path
from fastapi import FastAPI
from fastapi.testclient import TestClient
from hermes_platform_agent_comm.collaboration.hermes import profile_principal
from hermes_platform_agent_comm.collaboration.attention import create_router
from agent_comm_runtime.store import Store
from hermes_cli.web_server_profiles import _hermes_home_scope
from hermes_cli import web_server

base = Path(os.environ["HERMES_HOME"])
owners, approvals = {}, {}
for name, home in (("default", base), ("work", base / "profiles" / "work")):
    home.mkdir(parents=True, exist_ok=True)
    (home / "config.yaml").write_text("platforms:\n  agent_comm:\n    extra:\n      collaboration_enabled: true\n", encoding="utf-8")
    with _hermes_home_scope(home):
        owner = profile_principal()
        owners[name] = owner
        store = Store(home / "agent-comm" / "collaboration.sqlite3")
        request = store.prepare_contact("friend-" + name, [name], "urn:agent-comm:agent:peer-" + name, owner + "|native-original")
        approvals[name] = request["approval_id"]
        store.close()

app = FastAPI()
app.state.auth_required = False
app.include_router(create_router(), prefix="/api/plugins/agent-comm-attention")
headers = {"authorization": "Bearer " + web_server._SESSION_TOKEN}
with TestClient(app) as client:
    path = "/api/plugins/agent-comm-attention/attention"
    assert client.get(path).status_code == 401
    assert client.get(path, headers={"authorization":"Bearer invalid"}).status_code == 401
    default = client.get(path, headers=headers)
    assert default.status_code == 200, default.text
    work = client.get(path + "?profile=work", headers=headers)
    assert work.status_code == 200, work.text
    assert default.json()["owner_key"] == owners["default"]
    assert work.json()["owner_key"] == owners["work"]
    assert {i["target"]["id"] for i in default.json()["items"]} == {approvals["default"]}
    assert {i["target"]["id"] for i in work.json()["items"]} == {approvals["work"]}
    assert client.get(path + "?owner_session=forged", headers=headers).status_code == 400
    assert client.get(path + "?profile=../escape", headers=headers).status_code in (400, 404)
    assert client.post(path, headers=headers).status_code == 405
    old_cursor = default.json()["cursor"]
    # The projection didn't begin a lease. A later real native turn can show it.
    store = Store(base / "agent-comm" / "collaboration.sqlite3")
    lease = store.begin_confirmation(approvals["default"], owners["default"] + "|native-reopened")
    store.finish_confirmation(approvals["default"], lease["token"], owners["default"] + "|native-reopened", "同意")
    store.close()
    changed = client.get(path + "?after=" + str(old_cursor), headers=headers)
    assert changed.status_code == 200
    assert changed.json()["items"][0]["state"] == "resolved"
print("real auth, two-profile attention isolation, and pending-to-native recovery passed")
'''
        with tempfile.TemporaryDirectory(prefix="agent-comm-attention-host-") as home:
            env = dict(os.environ, HERMES_HOME=home, HERMES_TEST_ISOLATION="1", PYTHONDONTWRITEBYTECODE="1")
            connector = str(Path(__file__).resolve().parents[1])
            env["PYTHONPATH"] = connector + os.pathsep + env.get("PYTHONPATH", "")
            result = subprocess.run([sys.executable, "-c", code], env=env, capture_output=True, text=True,
                                    encoding="utf-8", errors="replace", timeout=90)
            self.assertEqual(result.returncode, 0, result.stderr[-6000:] + result.stdout[-1000:])


if __name__ == "__main__":
    unittest.main()
