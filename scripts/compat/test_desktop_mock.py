#!/usr/bin/env python3
"""Desktop harness 自检；HTTP 驱动不能证明真实 Desktop 已运行。"""
import json
import os
from pathlib import Path
import stat
import tempfile
import unittest
from unittest import mock
import urllib.error
import urllib.request

import desktop_mock as harness


class Request:
    def __init__(self, key='temporary-secret', path='/v1/messages'):
        self.path = path
        self.headers = {'Authorization': 'Bearer ' + key}


class EvidenceTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.path = Path(self.temp.name) / 'requests.jsonl'
        self.evidence = harness.Evidence(self.path)
        self.evidence.key = 'temporary-secret'

    def record(self, stage, body, key='temporary-secret', **extra):
        self.evidence.write(stage, Request(key), json.dumps(body).encode(), **extra)

    def test_private_json_has_restricted_permissions(self):
        path = Path(self.temp.name) / 'private.json'
        harness.private_json(path, {'test': True})
        self.assertEqual(json.loads(path.read_text()), {'test': True})
        self.assertEqual(stat.S_IMODE(path.stat().st_mode), 0o600)

    def test_no_requests_is_not_run(self):
        result = self.evidence.snapshot()
        self.assertEqual(result['workflow_status'], 'not_run')
        self.assertFalse(result['desktop_ui_verified'])

    def test_authentication_and_body_are_not_recorded(self):
        self.record('claude-to-cpa', {'model': 'public', 'messages': [{'role': 'user', 'content': 'private-body'}]})
        saved = self.path.read_text()
        self.assertNotIn('temporary-secret', saved)
        self.assertNotIn('private-body', saved)
        self.assertTrue(json.loads(saved)['auth_matches'])
        self.assertEqual(stat.S_IMODE(self.path.stat().st_mode), 0o600)

    def test_query_and_unknown_path_are_not_recorded(self):
        self.evidence.write('claude-to-cpa', Request(path='/v1/messages?api_key=temporary-secret'), b'{}')
        self.evidence.write('claude-to-cpa', Request(path='/temporary-secret?key=temporary-secret'), b'{}')
        records = [json.loads(line) for line in self.path.read_text().splitlines()]
        self.assertEqual([r['path'] for r in records], ['/v1/messages', 'other'])
        self.assertNotIn('temporary-secret', self.path.read_text())

    def test_chain_observed_does_not_claim_desktop_passed(self):
        self.record('claude-to-cpa', {'model': 'public', 'messages': []}, request_id='chat')
        self.record('cpa-to-responses', {'model': 'target', 'input': []})
        self.record('cpa-to-claude-result', {}, status=200, request_id='chat',
                    response_complete=True, reply_marker_present=True)
        result = self.evidence.snapshot()
        self.assertEqual(result['workflow_status'], 'observed')
        self.assertTrue(result['chat_request_chain_observed'])
        self.assertFalse(result['desktop_ui_verified'])

    def test_forwarded_request_without_client_response_is_not_a_completed_chain(self):
        self.record('claude-to-cpa', {'messages': []}, request_id='pending')
        self.record('cpa-to-responses', {'input': []})
        result = self.evidence.snapshot()
        self.assertEqual(result['response_count'], 0)
        self.assertFalse(result['chat_request_chain_observed'])
        self.assertEqual(result['workflow_status'], 'not_run')

    def test_unrelated_or_incomplete_reply_does_not_complete_chat(self):
        self.record('claude-to-cpa', {'messages': []}, request_id='chat')
        self.record('cpa-to-responses', {'input': []})
        self.record('cpa-to-claude-result', {}, status=200, request_id='other',
                    response_complete=True, reply_marker_present=True)
        self.assertFalse(self.evidence.snapshot()['chat_request_chain_observed'])
        self.record('cpa-to-claude-result', {}, status=200, request_id='chat',
                    response_complete=False, reply_marker_present=True)
        result = self.evidence.snapshot()
        self.assertFalse(result['chat_request_chain_observed'])
        self.assertEqual(result['workflow_status'], 'failed')

    def test_bad_auth_and_response_are_failures(self):
        self.record('claude-to-cpa', {'messages': []}, key='wrong')
        self.assertEqual(self.evidence.snapshot()['workflow_status'], 'failed')
        self.record('cpa-to-claude-result', {}, status=401)
        self.assertFalse(self.evidence.snapshot()['auth_verified'])

    def test_tool_requires_results_on_both_boundaries(self):
        self.record('claude-to-cpa', {'messages': [{'content': harness.TOOL_MARKER}]}, request_id='read')
        self.record('cpa-to-claude-result', {}, request_id='read', status=200,
                    response_complete=True, read_call_present=True)
        self.record('claude-to-cpa', {'messages': [{'content': [{'type': 'tool_result', 'tool_use_id': 'call_desktop_read', 'content': harness.MARKER}]}]}, request_id='result')
        self.record('cpa-to-claude-result', {}, request_id='result', status=200,
                    response_complete=True, reply_marker_present=True)
        self.assertFalse(self.evidence.snapshot()['code_tool_roundtrip_observed'])
        self.record('cpa-to-responses', {'input': [{'type': 'function_call_output', 'call_id': 'call_desktop_read', 'output': harness.MARKER}]})
        self.assertTrue(self.evidence.snapshot()['code_tool_roundtrip_observed'])

    def test_tool_result_without_earlier_read_response_is_not_a_roundtrip(self):
        self.record('claude-to-cpa', {'messages': [{'content': [{'type': 'tool_result',
                    'tool_use_id': 'call_desktop_read', 'content': harness.MARKER}]}]}, request_id='result')
        self.record('cpa-to-responses', {'input': [{'type': 'function_call_output',
                    'call_id': 'call_desktop_read', 'output': harness.MARKER}]})
        self.record('cpa-to-claude-result', {}, request_id='result', status=200,
                    response_complete=True, reply_marker_present=True)
        self.assertFalse(self.evidence.snapshot()['code_tool_roundtrip_observed'])
        self.record('cpa-to-claude-result', {}, request_id='late-read', status=200,
                    response_complete=True, read_call_present=True)
        self.assertFalse(self.evidence.snapshot()['code_tool_roundtrip_observed'])

    def test_unrelated_tool_results_do_not_count(self):
        self.record('claude-to-cpa', {'messages': [{'content': [{'type': 'tool_result', 'tool_use_id': 'wrong', 'content': harness.MARKER}]}]})
        self.record('cpa-to-responses', {'input': [{'type': 'function_call_output', 'call_id': 'wrong', 'output': harness.MARKER}]})
        self.assertFalse(self.evidence.snapshot()['code_tool_roundtrip_observed'])

    def test_token_count_does_not_count_as_chat(self):
        request = Request(path='/v1/messages/count_tokens')
        self.evidence.write('claude-to-cpa', request, b'{"messages":[]}')
        self.evidence.write('cpa-to-claude-result', request, b'', status=200)
        self.assertEqual(self.evidence.snapshot()['workflow_status'], 'not_run')


class CleanupTests(unittest.TestCase):
    def test_stop_failure_preserves_state_and_allows_safe_retry(self):
        with tempfile.TemporaryDirectory() as output_root:
            session = harness.DesktopMock(__file__, Path(output_root) / 'evidence')
            session.root = Path(tempfile.mkdtemp(prefix='cpa-desktop-cleanup-test-'))
            self.addCleanup(lambda: harness.shutil.rmtree(session.root, ignore_errors=True))
            session.work = session.root
            session.env = {'XDG_CONFIG_HOME': str(session.root / 'xdg')}
            identity = session.root / 'runtime.json'
            identity.write_text('{"synthetic_identity":true}')
            session.started = True
            session.command = mock.Mock(side_effect=RuntimeError('停止失败'))
            session.__exit__(None, None, None)
            summary = json.loads((session.output / 'summary.json').read_text())
            self.assertFalse(summary['service_stopped'])
            self.assertFalse(summary['temporary_state_removed'])
            self.assertEqual(summary['recovery']['temporary_root'], str(session.root))
            self.assertEqual(summary['recovery']['config_home'], session.env['XDG_CONFIG_HOME'])
            self.assertTrue(identity.exists())
            self.assertTrue(session.started)
            session.command = mock.Mock(return_value='网关已停止')
            session.__exit__(None, None, None)
            self.assertFalse(session.root.exists())
            self.assertTrue(session.summary['service_stopped'])
            self.assertTrue(session.summary['temporary_state_removed'])
            self.assertNotIn('recovery', session.summary)


class UpstreamTests(unittest.TestCase):
    def test_truncated_sse_is_not_reported_as_success(self):
        self.check_proxy_reply(complete=False)

    def test_completed_sse_records_only_synthetic_reply_evidence(self):
        self.check_proxy_reply(complete=True)

    def check_proxy_reply(self, complete):
        class FakeCPA(harness.base.BaseHTTPRequestHandler):
            def log_message(self, *_):
                pass

            def do_POST(self):
                self.rfile.read(int(self.headers['content-length']))
                self.send_response(200)
                self.send_header('content-type', 'text/event-stream')
                self.end_headers()
                frames = [dict(type='content_block_delta', delta=dict(type='text_delta', text='private-reply-')),
                          dict(type='content_block_delta', delta=dict(type='text_delta', text='CPA_DESKTOP_')),
                          dict(type='content_block_delta', delta=dict(type='text_delta', text='OK'))]
                if complete:
                    frames.append(dict(type='message_stop'))
                for frame in frames:
                    self.wfile.write(('data: ' + json.dumps(frame) + '\n\n').encode())
                    self.wfile.flush()

        with tempfile.TemporaryDirectory() as temp:
            evidence = harness.Evidence(Path(temp) / 'requests.jsonl')
            evidence.key = 'temporary-secret'
            with harness.base.server(FakeCPA) as cpa, harness.base.server(harness.observer_handler(evidence, cpa)) as observer:
                request = urllib.request.Request(f'http://127.0.0.1:{observer}/v1/messages',
                    b'{"messages":[],"stream":true}', {'Authorization': 'Bearer temporary-secret'})
                with urllib.request.urlopen(request, timeout=5) as response:
                    self.assertIn(b'private-reply-', response.read())
            records = [json.loads(line) for line in evidence.path.read_text().splitlines()]
            received = records[-1]
            self.assertEqual(received['request_id'], records[0]['request_id'])
            self.assertEqual(received['response_complete'], complete)
            self.assertTrue(received['reply_marker_present'])
            self.assertEqual(evidence.snapshot()['workflow_status'], 'not_run' if complete else 'failed')
            self.assertFalse(evidence.snapshot()['desktop_ui_verified'])
            self.assertNotIn('private-reply-', evidence.path.read_text())
            self.assertNotIn('temporary-secret', evidence.path.read_text())

    def test_unrelated_tool_result_still_emits_required_read(self):
        with tempfile.TemporaryDirectory() as temp:
            evidence = harness.Evidence(Path(temp) / 'requests.jsonl')
            fixture = Path(temp) / 'fixture.txt'
            fixture.write_text(harness.MARKER)
            with harness.base.server(harness.upstream_handler(evidence, fixture)) as port:
                payload = dict(model='target', input=[dict(type='message', content=harness.TOOL_MARKER),
                    dict(type='function_call_output', call_id='unrelated', output='already done')],
                    tools=[dict(type='function', name='Read')])
                request = urllib.request.Request(f'http://127.0.0.1:{port}/v1/responses',
                    json.dumps(payload).encode(), {'Content-Type': 'application/json'})
                with urllib.request.urlopen(request, timeout=5) as response:
                    frames = response.read().decode()
                completed = next(json.loads(line[6:]) for line in frames.splitlines()
                    if line.startswith('data: ') and json.loads(line[6:]).get('type') == 'response.completed')
                item = completed['response']['output'][0]
                self.assertEqual(item['type'], 'function_call')
                self.assertEqual(item['name'], 'Read')
                self.assertEqual(item['call_id'], 'call_desktop_read')
                self.assertEqual(json.loads(item['arguments'])['file_path'], str(fixture))


@unittest.skipUnless(os.environ.get('CPA_DESKTOP_TEST_BINARY'), '显式设置临时 CPA binary 才运行 loopback 自检')
class HTTPIntegrationTests(unittest.TestCase):
    def test_real_cpa_roundtrip_cleanup_and_no_desktop_claim(self):
        with tempfile.TemporaryDirectory() as output_root:
            output = Path(output_root) / 'evidence'
            with harness.DesktopMock(os.environ['CPA_DESKTOP_TEST_BINARY'], output) as session:
                session.summary['client_identity'] = 'http-self-test'
                config = next(p for p in (session.user_data / 'configLibrary').glob('*.json') if p.name != '_meta.json')
                values = json.loads(config.read_text())
                self.assertEqual(values['inferenceGatewayBaseUrl'], session.endpoint)
                self.assertTrue(values['chatTabEnabled'])
                self.assertFalse(values['coworkTabEnabled'])
                self.assertFalse(values['modelDiscoveryEnabled'])
                self.assertEqual(stat.S_IMODE(config.stat().st_mode), 0o600)
                self.assertTrue(Path(session.env['XDG_CONFIG_HOME']).is_relative_to(session.root))
                key = values['inferenceGatewayApiKey']
                def post(payload):
                    req = urllib.request.Request(session.endpoint + '/v1/messages', json.dumps(payload).encode(),
                        {'Authorization': 'Bearer ' + key, 'Content-Type': 'application/json'})
                    with urllib.request.urlopen(req, timeout=15) as response:
                        return json.load(response)
                payload = dict(model=harness.MODELS[0][1], max_tokens=512, stream=False,
                               messages=[dict(role='user', content='Reply exactly ' + harness.MARKER)])
                self.assertEqual(post(payload)['content'][0]['text'], harness.MARKER)
                payload['stream'] = True
                req = urllib.request.Request(session.endpoint + '/v1/messages', json.dumps(payload).encode(),
                    {'Authorization': 'Bearer ' + key, 'Content-Type': 'application/json'})
                with urllib.request.urlopen(req, timeout=15) as response:
                    frames = response.read().decode()
                self.assertIn('message_stop', frames)
                self.assertIn(harness.MARKER, frames)
                result = json.loads(session.evidence.path.read_text().splitlines()[-1])
                self.assertTrue(result['response_complete'])
                self.assertTrue(result['reply_marker_present'])
                payload['stream'] = False
                payload['messages'] = [dict(role='user', content=harness.TOOL_MARKER + ': Read fixture.txt')]
                payload['tools'] = [dict(name='Read', description='读取临时测试文件',
                                        input_schema=dict(type='object', properties=dict(file_path=dict(type='string')), required=['file_path']))]
                tool = post(payload)['content'][0]
                self.assertEqual(tool['type'], 'tool_use')
                self.assertEqual(tool['input']['file_path'], str(session.fixture))
                payload['messages'] += [dict(role='assistant', content=[tool]),
                                        dict(role='user', content=[dict(type='tool_result', tool_use_id=tool['id'],
                                                                      content=session.fixture.read_text())])]
                self.assertEqual(post(payload)['content'][0]['text'], harness.MARKER)
                snapshot = session.evidence.snapshot()
                self.assertTrue(snapshot['chat_request_chain_observed'])
                self.assertTrue(snapshot['code_tool_roundtrip_observed'])
                self.assertFalse(snapshot['desktop_ui_verified'])
                root = session.root
            summary = json.loads((output / 'summary.json').read_text())
            self.assertTrue(summary['service_stopped'])
            self.assertTrue(summary['temporary_state_removed'])
            self.assertFalse(root.exists())
            for path in output.iterdir():
                self.assertNotIn(key, path.read_text())


if __name__ == '__main__':
    unittest.main()
