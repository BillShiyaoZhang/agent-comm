"""The real Hermes profile/session APIs exercised only against temporary homes."""
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


class RealAttentionResumeHostTests(unittest.TestCase):
    def test_generated_recovery_calls_pass_connector_guard_and_read_the_referenced_source(self):
        code = r'''
import json, os, re
from datetime import datetime, timedelta, timezone
from pathlib import Path
from agent_comm_runtime import Store
from hermes_platform_agent_comm.collaboration.attention import _instruction
from hermes_platform_agent_comm.collaboration.hermes import _validate_args, profile_principal

owner = profile_principal() + '|attention'
store = Store(Path(os.environ['HERMES_HOME']) / 'collaboration.sqlite3', local_urn='urn:agent-comm:agent:self')
try:
    store.prepare_contact('friend', ['Peer'], 'urn:agent-comm:agent:peer', owner)
    now = datetime.now(timezone.utc)
    store.prepare_task('task-one', {
        'purpose': 'test exact generated calls', 'topic': 'test', 'capabilities': ['send_text'],
        'recipient_ids': ['self'], 'participant_ids': ['self'], 'resource_ids': [],
        'window_start': now.isoformat(), 'window_end': (now + timedelta(hours=1)).isoformat(),
        'expires_at': (now + timedelta(hours=1)).isoformat(),
        'max_duration_minutes': 1, 'max_candidates': 1, 'max_actions': 1}, owner)
    source_items = store.attention(owner)['items']
    assert len(source_items) == 2
    for item in source_items:  # covers a task and a no-task contact approval
        text = _instruction(item)
        calls = [json.loads(block) for block in re.findall(r'```json\n(.*?)\n```', text, re.S)]
        assert len(calls) == 3, calls
        for call in calls:
            _validate_args(call)  # real connector/runtime guard, never a permissive test stub
        assert [call['action'] for call in calls] == ['state', 'attention', 'confirm']
        state_call, attention_call, confirm_call = calls
        state = store.state(owner, **{key: value for key, value in state_call.items() if key != 'action'})
        feed = store.attention(owner, **{key: value for key, value in attention_call.items() if key != 'action'})
        assert any(row['attention_id'] == item['attention_id'] for row in feed['items'])
        assert any(row['approval_id'] == confirm_call['approval_id'] for row in state['pending_confirmations'])
        assert attention_call['after'] == max(0, item['revision'] - 1)
        assert attention_call['limit'] == 100
        for invalid in ({'action': 'attention', 'attention_id': item['attention_id'],
                         'target': item['target'], 'revision': item['revision']},
                        {**attention_call, 'task_id': 'task-one'}):
            try:
                _validate_args(invalid)
            except ValueError:
                pass
            else:
                raise AssertionError('Reference fields must remain rejected by the real guard')
    assert store._all('operation') == []
    assert all(item['status'] == 'pending' and 'token_hash' not in item for item in store._all('approval'))
finally:
    store.close()
print('generated state/attention/confirm JSON passes actual connector guard and reads actual pending source')
'''
        with tempfile.TemporaryDirectory(prefix="agent-comm-instruction-host-") as home:
            env = dict(os.environ, HERMES_HOME=home, HERMES_TEST_ISOLATION="1", PYTHONDONTWRITEBYTECODE="1")
            connector = str(Path(__file__).resolve().parents[1])
            env["PYTHONPATH"] = connector + os.pathsep + env.get("PYTHONPATH", "")
            result = subprocess.run([sys.executable, "-c", code], env=env, capture_output=True, text=True,
                                    encoding="utf-8", errors="replace", timeout=45)
            self.assertEqual(result.returncode, 0, result.stderr[-4000:] + result.stdout[-2000:])

    def test_real_session_db_rejects_generic_title_and_accepts_distinct_handling_sessions(self):
        code = r'''
import os
from pathlib import Path
from hermes_state import SessionDB

db = SessionDB(db_path=Path(os.environ['HERMES_HOME']) / 'state.db')
first, second = '20260915_174037_d3b406', '20260915_182526_a91a3f'
generic = '协作处理 · 协作委托需要你确认'
try:
    db.create_session(first, source='desktop')
    db.create_session(second, source='desktop')
    db.set_session_title(first, generic)
    try:
        db.set_session_title(second, generic)
    except ValueError as error:
        assert 'already in use' in str(error), type(error).__name__
    else:
        raise AssertionError('Expected the actual Hermes unique-title conflict')
    # The UI now obtains the host stored ID before assigning the final title;
    # JS tests separately cover the actual title generator and 100-char bound.
    for session_id in (first, second):
        title = generic + ' · ' + session_id
        assert len(title) <= SessionDB.MAX_TITLE_LENGTH
        db.set_session_title(session_id, title)
        db.set_session_title(session_id, title)  # explicit retry keeps the same row/title
        assert db.get_session_title(session_id) == title
        assert db.get_session(session_id)['source'] == 'desktop'
        assert db.get_session(session_id)['message_count'] == 0
    assert db.get_session_title(first) != db.get_session_title(second)
finally:
    db.close()
print('actual SessionDB generic-title conflict and unique native session titles passed')
'''
        with tempfile.TemporaryDirectory(prefix="agent-comm-title-host-") as home:
            env = dict(os.environ, HERMES_HOME=home, HERMES_TEST_ISOLATION="1", PYTHONDONTWRITEBYTECODE="1")
            result = subprocess.run([sys.executable, "-c", code], env=env, capture_output=True, text=True,
                                    encoding="utf-8", errors="replace", timeout=45)
            self.assertEqual(result.returncode, 0, result.stderr[-4000:] + result.stdout[-2000:])

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
