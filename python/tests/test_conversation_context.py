"""Conversation provenance, durable notice recovery and isolation, no network."""
from datetime import datetime, timezone
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from agent_comm_runtime.remote import PROTOCOL, READ_METHODS, RemoteBridge
from agent_comm_runtime.store import Store
from agent_comm_runtime.runtime import validate_args

NOW = 2_000_000_000
AGENT = 'urn:agent-comm:agent:context-agent'
CONSOLE = 'urn:agent-comm:agent:context-console'
OTHER = 'urn:agent-comm:agent:context-other'


def stamp(n):
    return datetime.fromtimestamp(n, timezone.utc).isoformat()


class TestConversationContext(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.home = Path(self.temp.name)
        self.store = Store(self.home / 'store.sqlite3', clock=lambda: NOW, local_urn=AGENT)
        self.bridge = RemoteBridge(self.home / 'remote.sqlite3', self.store, AGENT, clock=lambda: NOW, conversations=True)
        methods = [*READ_METHODS, 'conversation.send', 'conversation.get', 'collaboration.execute']
        self.bridge.pair(CONSOLE, 'owner-a', methods, stamp(NOW + 1000))
        self.bridge.pair(OTHER, 'owner-b', methods, stamp(NOW + 1000))
        with self.store._transaction():
            self.store._put('contact', 'peer', {'contact_id': 'peer', 'aliases': ['Peer'],
                'urn': 'urn:agent-comm:agent:peer', 'owner_id': 'owner-a', 'status': 'connected'})

    def tearDown(self):
        self.bridge.close()
        self.store.close()
        self.temp.cleanup()

    def rpc(self, request_id, method, params=None, console=CONSOLE):
        packet = {'protocol': PROTOCOL, 'type': 'request', 'request_id': request_id, 'method': method,
            'params': params or {}, 'agent_urn': AGENT, 'console_urn': console, 'deadline': stamp(NOW + 120)}
        wire = {'message_id': request_id, 'sender_urn': console, 'kind': 'control.request',
            'conversation_id': 'control:' + request_id, 'deadline': packet['deadline'], 'text': json.dumps(packet)}
        return json.loads(self.bridge.handle(wire)['text'])

    def scope(self):
        return {'purpose': 'Coordinate one meeting', 'topic': 'Review', 'capabilities': ['send_text'],
            'recipient_ids': ['peer'], 'participant_ids': ['self', 'peer'], 'resource_ids': [],
            'window_start': stamp(NOW + 10), 'window_end': stamp(NOW + 500), 'expires_at': stamp(NOW + 600),
            'max_duration_minutes': 30, 'max_candidates': 2, 'max_actions': 4}

    def test_notices_have_stable_versions_safe_summaries_and_owner_isolation(self):
        self.rpc('send', 'conversation.send', {'conversation_id': 'chat', 'text': 'PRIVATE PROMPT'})
        submitted = self.rpc('notice-submitted', 'attention.list')['result']
        self.assertEqual(submitted['items'][0]['state'], 'resolved')
        self.assertEqual(submitted['items'][0]['source_revision'], 'submitted')
        job = self.bridge.claim_turn()
        self.bridge.finish_turn(job['turn_id'], response='PRIVATE RESPONSE')
        done = self.rpc('notice-done', 'attention.list')['result']
        self.assertEqual(len(done['items']), 1)
        item = done['items'][0]
        self.assertEqual(item['kind'], 'conversation_completed')
        self.assertEqual(item['target'], {'kind': 'conversation', 'id': 'chat', 'turn_id': job['turn_id']})
        self.assertNotIn('PRIVATE', json.dumps(done))
        detail = self.store.attention_detail('owner-a|native', item['attention_id'])
        self.assertFalse(detail['item']['details']['can_resume'])
        self.assertEqual(detail['item']['details']['conversation']['turn_id'], job['turn_id'])
        with self.assertRaisesRegex(ValueError, 'no native handling request'):
            self.store.attention_prepare_resume('owner-a|native', item['attention_id'], item['revision'], resolve_session=lambda _: None)
        self.assertEqual(self.store._all('attention_resume'), [])
        self.store.record_conversation_event(job)  # A stale running projection arrives late.
        self.assertEqual(self.store._get('conversation_turn', job['turn_id'])['status'], 'completed')
        again = self.rpc('notice-again', 'attention.list', {'after': done['cursor']})['result']
        self.assertEqual(again['cursor'], done['cursor'])
        self.assertEqual(again['items'], [])
        self.assertEqual(self.rpc('other-notice', 'attention.list', console=OTHER)['result']['items'], [])
        self.bridge.finish_turn(job['turn_id'], error='cannot change final result')
        self.assertEqual(self.bridge._get('turn', job['turn_id'])['status'], 'completed')

    def test_crash_between_terminal_commit_and_notice_does_not_replay(self):
        self.rpc('send', 'conversation.send', {'text': 'Perform a side effect'})
        job = self.bridge.claim_turn()
        with patch.object(self.store, 'record_conversation_event', side_effect=OSError('projection unavailable')):
            with self.assertRaises(OSError):
                self.bridge.finish_turn(job['turn_id'], response='Host completed')
        self.bridge.close()
        self.bridge = RemoteBridge(self.home / 'remote.sqlite3', self.store, AGENT, clock=lambda: NOW, conversations=True)
        self.bridge.recover_interrupted_turns()
        self.assertIsNone(self.bridge.claim_turn())
        self.assertEqual(self.bridge._get('turn', job['turn_id'])['status'], 'completed')
        self.assertEqual(self.rpc('repair-notice', 'attention.list')['result']['items'][0]['kind'], 'conversation_completed')

    def test_running_crash_is_uncertain_and_queued_claims_remain_serial(self):
        self.rpc('one', 'conversation.send', {'text': 'first'})
        self.rpc('two', 'conversation.send', {'text': 'second'})
        first = self.bridge.claim_turn()
        self.assertIsNone(self.bridge.claim_turn())
        self.bridge.recover_interrupted_turns()
        self.assertEqual(self.bridge._get('turn', first['turn_id'])['status'], 'interrupted')
        next_job = self.bridge.claim_turn()
        self.assertNotEqual(first['turn_id'], next_job['turn_id'])
        self.assertIsNone(self.bridge.claim_turn())
        notices = self.rpc('notices', 'attention.list')['result']['items']
        uncertain = next(i for i in notices if i['target']['turn_id'] == first['turn_id'])
        self.assertEqual(uncertain['kind'], 'conversation_failed')
        self.assertIn('不确定', uncertain['safe_summary'])

    def test_delayed_terminal_notice_preserves_read_watermark_and_monotonic_revision(self):
        now = [NOW]
        self.store.clock = lambda: now[0]
        self.bridge.clock = lambda: now[0]
        self.rpc('send', 'conversation.send', {'conversation_id': 'chat', 'text': 'request'})
        submitted = self.rpc('notice-submitted', 'attention.list')['result']
        now[0] += 1
        job = self.bridge.claim_turn()
        running = self.rpc('notice-running', 'attention.list')['result']
        self.assertGreater(running['cursor'], submitted['cursor'])
        now[0] += 1
        with patch.object(self.store, 'record_conversation_event', side_effect=OSError('projection unavailable')):
            with self.assertRaises(OSError):
                self.bridge.finish_turn(job['turn_id'], response='completed')
        completed = self.bridge._get('turn', job['turn_id'])
        now[0] += 60
        self.bridge.close()
        self.bridge = RemoteBridge(self.home / 'remote.sqlite3', self.store, AGENT,
                                   clock=lambda: now[0], conversations=True)
        self.bridge.recover_interrupted_turns()
        repaired = self.rpc('notice-repaired', 'attention.list', {'after': running['cursor']})['result']
        self.assertGreater(repaired['cursor'], running['cursor'])
        item = repaired['items'][0]
        self.assertEqual(item['kind'], 'conversation_completed')
        self.assertEqual(item['updated_at'], completed['updated_at'])
        self.assertEqual(item['created_at'], completed['created_at'])
        self.assertLess(item['updated_at'], now[0])
        turn = self.rpc('get', 'conversation.get', {'conversation_id': 'chat'})['result']['turns'][0]
        self.assertGreaterEqual(turn['updated_at'], item['updated_at'],
                                'reading the terminal turn must cover its delayed notification')
        self.store.record_conversation_event(job)  # A stale progress event must not reopen it.
        again = self.rpc('notice-again', 'attention.list', {'after': repaired['cursor']})['result']
        self.assertEqual(again['cursor'], repaired['cursor'])
        self.assertEqual(again['items'], [])
        self.assertIsNone(self.bridge.claim_turn())

    def test_trusted_host_links_records_and_keeps_original_provenance(self):
        self.rpc('send', 'conversation.send', {'conversation_id': 'chat', 'text': 'task FAKE-ID approval-FAKE'})
        job = self.bridge.claim_turn()
        owner = 'owner-a|remote-test'
        context = {'origin': 'paired_conversation', 'conversation_id': 'chat', 'turn_id': job['turn_id']}
        with self.store.bind_source_context(owner, context, CONSOLE):
            prepared = self.store.prepare_task('task-real', self.scope(), owner)
        result = self.rpc('get', 'conversation.get', {'conversation_id': 'chat'})['result']
        self.assertEqual({(i['kind'], i['id']) for i in result['turns'][0]['related']},
            {('task', 'task-real'), ('approval', prepared['approval_id'])})
        state = self.store.state(owner)
        self.assertEqual(state['tasks'][0]['source_context'], context)
        self.assertEqual(state['pending_confirmations'][0]['source_context'], context)
        other_context = {'origin': 'paired_control', 'request_id': 'later', 'conversation_id': 'chat'}
        with self.store.bind_source_context(owner, other_context, CONSOLE):
            self.store.revoke('task-real', owner)
        self.assertEqual(self.store.state(owner)['tasks'][0]['source_context'], context)
        self.assertEqual(self.rpc('other-get', 'conversation.get', {'conversation_id': 'chat'}, console=OTHER)['result']['turns'], [])
        with self.assertRaises(ValueError):
            validate_args({'action': 'state', 'source_context': context})
        self.assertEqual(self.rpc('forged-send', 'conversation.send', {'text': 'x', 'source_context': context})['error']['code'], 'invalid_params')

    def test_direct_rpc_source_is_verified_navigation_without_running_turn_authority(self):
        self.rpc('send', 'conversation.send', {'conversation_id': 'chat', 'text': 'hello'})
        captured = []
        def execute(params, owner, *, source_context, source_console_urn, **_):
            captured.append((params, source_context))
            with self.store.bind_source_context(owner, source_context, source_console_urn):
                return self.store.prepare_task('task-web', params['scope'], owner)
        self.bridge.register_handler('collaboration.execute', execute)
        params = {'action': 'prepare_task', 'scope': self.scope(), 'source_conversation_id': 'chat'}
        self.assertIn('result', self.rpc('execute', 'collaboration.execute', params))
        self.assertEqual(captured[0][1], {'origin': 'paired_control', 'request_id': 'execute', 'conversation_id': 'chat'})
        self.assertNotIn('source_conversation_id', captured[0][0])
        self.assertNotIn('turn_id', captured[0][1])
        denied = self.rpc('forged-execute', 'collaboration.execute', params, console=OTHER)
        self.assertEqual(denied['error']['code'], 'invalid_source_context')
        self.assertEqual(len(captured), 1)

    def test_recent_history_bound_is_explicit_and_old_records_remain_compatible(self):
        with self.bridge._transaction():
            for n in range(101):
                job = {'turn_id': f'turn-{n:03}', 'conversation_id': 'chat', 'console_urn': CONSOLE,
                    'owner_principal': 'owner-a', 'status': 'completed', 'text': str(n), 'response': 'reply',
                    'error': None, 'created_at': NOW + n, 'updated_at': NOW + n}
                self.bridge._put('turn', job['turn_id'], job)
        result = self.rpc('get', 'conversation.get', {'conversation_id': 'chat'})['result']
        self.assertEqual(result['history'], {'limit': 100, 'returned': 100, 'truncated': True})
        self.assertEqual(result['turns'][0]['turn_id'], 'turn-001')
        self.assertTrue(all(t['related'] == [] for t in result['turns']))


if __name__ == '__main__':
    unittest.main()
