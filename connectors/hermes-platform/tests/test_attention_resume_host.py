"""The real Hermes profile/session APIs exercised only against temporary homes."""
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


class RealAttentionResumeHostTests(unittest.TestCase):
    def test_authenticated_native_session_recovery_and_idempotent_submission(self):
        code = r'''
import json, os
from pathlib import Path
from unittest.mock import patch
from fastapi import FastAPI
from fastapi.testclient import TestClient
from hermes_platform_agent_comm.collaboration.hermes import profile_principal
from hermes_platform_agent_comm.collaboration import attention
from agent_comm_runtime import Store
from hermes_cli.web_server_profiles import _hermes_home_scope
from hermes_cli import web_server
from hermes_state import SessionDB

base = Path(os.environ['HERMES_HOME'])
owners, approvals = {}, {}
for name, home in (('default', base), ('work', base / 'profiles' / 'work')):
    home.mkdir(parents=True, exist_ok=True)
    (home / 'config.yaml').write_text('platforms:\n  agent_comm:\n    extra:\n      collaboration_enabled: true\n', encoding='utf-8')
    with _hermes_home_scope(home):
        owner = profile_principal()
        owners[name] = owner
        store = Store(home / 'agent-comm' / 'collaboration.sqlite3')
        origin = 'original' if name == 'default' else 'remote:paired-console'
        req = store.prepare_contact('friend-' + name, ['Private ' + name], 'urn:agent-comm:agent:peer-' + name, owner + '|' + origin)
        approvals[name] = req['approval_id']
        store.close()
        db = SessionDB(db_path=home / 'state.db')
        db.create_session('original' if name == 'default' else 'work-new', source='desktop')
        db.create_session('non-native', source='telegram')
        db.close()

app = FastAPI()
app.state.auth_required = False
app.include_router(attention.create_router(), prefix='/api/plugins/agent-comm-attention')
headers = {'authorization': 'Bearer ' + web_server._SESSION_TOKEN}
path = '/api/plugins/agent-comm-attention/attention'
with TestClient(app) as client:
    def post(action, body, profile=''):
        return client.post(path + '/' + action + ('?profile=' + profile if profile else ''), json=body, headers=headers)
    assert client.post(path + '/prepare-resume', json={}).status_code == 401
    items = client.get(path, headers=headers).json()['items']
    item = items[0]
    assert item['resume']['stored_session_id'] == 'original', item
    assert item['resume']['session_state'] == 'available'
    assert 'Private default' in item['details']['question']
    assert 'Private work' not in json.dumps(item)
    detail_path = path + '/' + item['attention_id'] + '/detail'
    assert client.get(detail_path).status_code == 401
    detail = client.get(detail_path, headers=headers)
    assert detail.status_code == 200, detail.text
    assert detail.json()['item']['details']['question'] == item['details']['question']
    assert client.get(detail_path + '?profile=work', headers=headers).status_code == 404
    body = {'attention_id':item['attention_id'], 'revision':item['revision']}
    prepared = post('prepare-resume', body)
    assert prepared.status_code == 200, prepared.text
    prepared = prepared.json()
    rid = prepared['resume_id']
    assert prepared['owner_key'] == owners['default']
    assert prepared['submission_state'] == 'ready'
    assert '"approval_id": "' + approvals['default'] in prepared['instruction']
    assert post('prepare-resume', {**body, 'owner': 'forged'}).status_code == 400
    assert post('prepare-resume', body, 'work').status_code == 409
    assert post('prepare-resume', {**body, 'revision': True}).status_code == 409
    assert post('claim-submit', {'resume_id':rid, 'stored_session_id':'work-new'}).status_code == 409
    claim = post('claim-submit', {'resume_id':rid, 'stored_session_id':'original'}).json()
    assert claim['claimed'] is True
    assert post('claim-submit', {'resume_id':rid, 'stored_session_id':'original'}).json()['claimed'] is False
    assert post('finish-submit', {'resume_id':rid, 'claim_token':'forged', 'outcome':'submitted'}).status_code == 409
    finished = post('finish-submit', {'resume_id':rid, 'claim_token':claim['claim_token'], 'outcome':'submitted'})
    assert finished.json()['submission_state'] == 'submitted'
    assert post('prepare-resume', body).json()['submission_state'] == 'submitted'
    # Host I/O errors are neither "missing" nor permission to create a duplicate.
    with patch.object(attention, '_resolve_native_session', side_effect=TimeoutError('simulated unavailable host')):
        assert post('prepare-resume', body).status_code == 503
    assert post('prepare-resume', body).json()['resume_id'] == rid

    work = client.get(path + '?profile=work', headers=headers).json()['items'][0]
    wbody = {'attention_id':work['attention_id'], 'revision':work['revision']}
    wprepare = post('prepare-resume', wbody, 'work').json()
    assert wprepare['session_state'] == 'none'
    wrid = wprepare['resume_id']
    assert post('bind-session', {'resume_id':wrid, 'stored_session_id':'original'}, 'work').status_code == 409
    assert post('bind-session', {'resume_id':wrid, 'stored_session_id':'non-native'}, 'work').status_code == 409
    assert post('bind-session', {'resume_id':wrid, 'stored_session_id':'work-new'}, 'work').json()['stored_session_id'] == 'work-new'
    wclaim = post('claim-submit', {'resume_id':wrid, 'stored_session_id':'work-new'}, 'work').json()
    assert wclaim['claimed'] is True
    assert post('finish-submit', {'resume_id':wrid, 'claim_token':wclaim['claim_token'], 'outcome':'uncertain'}, 'work').json()['submission_state'] == 'uncertain'
    assert post('prepare-resume', wbody, 'work').json()['submission_state'] == 'uncertain'
    db = SessionDB(db_path=base / 'profiles' / 'work' / 'state.db')
    db.delete_session('work-new')
    db.close()
    replaced = post('prepare-resume', wbody, 'work').json()
    assert replaced['session_state'] == 'missing'
    assert replaced['submission_state'] == 'ready'
    assert replaced['resume_id'] != wrid

    store = Store(base / 'agent-comm' / 'collaboration.sqlite3')
    assert store.state(owners['default'] + '|attention')['contacts'] == []
    assert store.state(owners['default'] + '|attention')['pending_confirmations'][0]['status'] == 'pending'
    lease = store.begin_confirmation(approvals['default'], owners['default'] + '|original')
    store.finish_confirmation(approvals['default'], lease['token'], owners['default'] + '|original', '拒绝')
    assert store.state(owners['default'] + '|attention')['contacts'] == []
    store.close()
    assert post('prepare-resume', body).status_code == 409
    assert client.get(path, headers=headers).json()['items'][0]['details']['can_resume'] is False
    assert client.get(detail_path, headers=headers).json()['item']['details']['can_resume'] is False
print('real auth/profile/session DB, no-task remote/contact recovery, claim-once, missing fallback, native-only decision passed')
'''
        with tempfile.TemporaryDirectory(prefix="agent-comm-resume-host-") as home:
            env = dict(os.environ, HERMES_HOME=home, HERMES_TEST_ISOLATION="1", PYTHONDONTWRITEBYTECODE="1")
            connector = str(Path(__file__).resolve().parents[1])
            env["PYTHONPATH"] = connector + os.pathsep + env.get("PYTHONPATH", "")
            result = subprocess.run([sys.executable, "-c", code], env=env, capture_output=True, text=True,
                                    encoding="utf-8", errors="replace", timeout=90)
            self.assertEqual(result.returncode, 0, result.stderr[-6000:] + result.stdout[-2000:])


if __name__ == "__main__":
    unittest.main()
