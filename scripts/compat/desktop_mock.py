#!/usr/bin/env python3
"""为隔离 Claude Desktop 准备真实 CPA 和 loopback mock；本脚本不启动应用。"""
import argparse
import contextlib
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import signal
import shutil
import tempfile
import threading
import time
import uuid

_spec = importlib.util.spec_from_file_location('claude_mock', Path(__file__).with_name('claude_mock.py'))
base = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(base)
MARKER = 'CPA_DESKTOP_OK'
TOOL_MARKER = 'CPA_DESKTOP_READ'
MODELS = [('sonnet', 'claude-sonnet-4-6'), ('opus', 'claude-opus-4-6'),
          ('haiku', 'claude-haiku-4-5-20251001'), ('fable', 'claude-fable-5-1')]


def private_json(path, value):
    with open(path, 'w', opener=lambda p, f: os.open(p, f, 0o600)) as stream:
        json.dump(value, stream, ensure_ascii=False, indent=2)
        stream.write('\n')
    path.chmod(0o600)


class ResponseEvidence:
    """逐帧检查代理实际转发的回复；只保存合成标记，不保存正文。"""
    def __init__(self, streaming):
        self.streaming, self.buffer = streaming, b''
        self.complete = self.marker = self.read_call = self.invalid = False
        self.tail = ''
        self.read_call_timestamp = None

    def inspect(self, payload):
        if not isinstance(payload, dict):
            return
        kind = payload.get('type')
        if kind == 'message_stop':
            self.complete = True
        blocks = payload.get('content', []) if kind == 'message' else []
        if kind == 'message':
            self.complete = payload.get('stop_reason') in ('end_turn', 'tool_use', 'max_tokens', 'stop_sequence')
        if kind == 'content_block_start':
            blocks = [payload.get('content_block', {})]
        if kind == 'content_block_delta':
            blocks = [payload.get('delta', {})]
        for block in blocks:
            if not isinstance(block, dict):
                continue
            if block.get('type') == 'tool_use' and block.get('name') == 'Read' and block.get('id') == 'call_desktop_read':
                self.read_call = True
                self.read_call_timestamp = self.read_call_timestamp or time.time()
            text = block.get('text', '')
            if isinstance(text, str):
                self.tail += text
                self.marker = self.marker or MARKER in self.tail
                self.tail = self.tail[-len(MARKER):]

    def feed(self, chunk):
        if self.invalid:
            return
        self.buffer += chunk
        # 超大/畸形帧不构成成功证据，也不让观察器无限占用内存。
        if len(self.buffer) > 1024 * 1024:
            self.buffer = b''
            self.invalid, self.complete = True, False
            return
        if self.streaming:
            self.buffer = self.buffer.replace(b'\r\n', b'\n')
            while b'\n\n' in self.buffer:
                frame, self.buffer = self.buffer.split(b'\n\n', 1)
                data = b'\n'.join(line[5:].lstrip() for line in frame.splitlines() if line.startswith(b'data:'))
                try:
                    self.inspect(json.loads(data))
                except (ValueError, UnicodeDecodeError):
                    pass

    def finish(self):
        if not self.streaming and not self.invalid:
            try:
                self.inspect(json.loads(self.buffer))
            except (ValueError, UnicodeDecodeError):
                pass


class Evidence:
    """只保留请求形状与合成标记；认证头、正文及工具参数不落盘。"""
    def __init__(self, path):
        self.path, self.lock, self.records = path, threading.Lock(), []
        self.key = None
        self.run_id = str(uuid.uuid4())
        path.touch(mode=0o600, exist_ok=False)

    def write(self, stage, request, body, **extra):
        payload = json.loads(body) if body else {}
        serialized = json.dumps(payload, ensure_ascii=False)
        auth = request.headers.get('Authorization', '')
        key = request.headers.get('x-api-key', '')
        path = request.path.split('?', 1)[0]
        safe_path = path if path in ('/v1/messages', '/v1/messages/count_tokens', '/v1/models', '/v1/responses', '/responses') else 'other'
        record = dict(run_id=self.run_id, stage=stage, path=safe_path, timestamp=time.time(),
                      model=payload.get('model'), stream=payload.get('stream'),
                      tool_names=[t.get('name') for t in payload.get('tools', []) if isinstance(t, dict)],
                      fixture_marker_present=MARKER in serialized,
                      tool_case_present=TOOL_MARKER in serialized,
                      **extra)
        if stage == 'claude-to-cpa':
            record['auth_matches'] = bool(self.key and (auth == 'Bearer ' + self.key or key == self.key))
            record['auth_scheme'] = 'bearer' if auth.startswith('Bearer ') else 'x-api-key' if key else 'none'
            record['tool_result_present'] = any(
                isinstance(block, dict) and block.get('type') == 'tool_result'
                and block.get('tool_use_id') == 'call_desktop_read'
                and MARKER in json.dumps(block.get('content'))
                for message in payload.get('messages', []) if isinstance(message, dict)
                for block in (message.get('content') if isinstance(message.get('content'), list) else []))
        if stage == 'cpa-to-responses':
            record['tool_result_present'] = any(
                isinstance(item, dict) and item.get('type') == 'function_call_output'
                and item.get('call_id') == 'call_desktop_read' and MARKER in json.dumps(item.get('output'))
                for item in (payload.get('input') if isinstance(payload.get('input'), list) else []))
        with self.lock, self.path.open('a') as stream:
            self.records.append(record)
            stream.write(json.dumps(record, ensure_ascii=False) + '\n')

    def snapshot(self):
        with self.lock:
            records = list(self.records)
        inbound = [r for r in records if r['stage'] == 'claude-to-cpa' and r['path'].split('?')[0] == '/v1/messages']
        outbound = [r for r in records if r['stage'] == 'cpa-to-responses']
        results = [r for r in records if r['stage'] == 'cpa-to-claude-result' and r['path'].split('?')[0] == '/v1/messages']
        successful = {r['request_id']: r for r in results if r.get('request_id') and r.get('status') == 200
                      and r.get('response_complete') and not r.get('transfer_error')}
        chat = any(not r['tool_case_present'] and r.get('request_id') in successful
                   and successful[r['request_id']].get('reply_marker_present') for r in inbound) and any(
                       not r['tool_case_present'] for r in outbound)
        read_results = [successful[r['request_id']] for r in inbound if r.get('request_id') in successful
                        and successful[r['request_id']].get('read_call_present')]
        tool = any(r.get('tool_result_present') and r.get('request_id') in successful
                   and successful[r['request_id']].get('reply_marker_present')
                   and any((read.get('read_call_timestamp') or read['timestamp']) < r['timestamp']
                           for read in read_results) for r in inbound) and any(
                       r.get('tool_result_present') for r in outbound)
        failed = any(not r['auth_matches'] for r in inbound) or any(
            r.get('status') != 200 or not r.get('response_complete') or r.get('transfer_error') for r in results)
        return dict(client_requests=len(inbound), upstream_requests=len(outbound),
                    response_count=len(results), auth_verified=bool(inbound) and all(r['auth_matches'] for r in inbound),
                    chat_request_chain_observed=chat, code_tool_roundtrip_observed=tool,
                    observed_models=sorted({r['model'] for r in inbound if r['model']}),
                    workflow_status='failed' if failed else 'observed' if chat or tool else 'not_run',
                    desktop_ui_verified=False)


def upstream_handler(evidence, fixture):
    original = base.handler(evidence)

    class Handler(original):
        def do_POST(self):
            body = self.rfile.read(int(self.headers.get('content-length', '0')))
            payload = json.loads(body)
            evidence.write('cpa-to-responses', self, body)
            inputs = payload.get('input', [])
            tool_case = TOOL_MARKER in json.dumps(inputs)
            has_result = any(isinstance(item, dict) and item.get('type') == 'function_call_output'
                             and item.get('call_id') == 'call_desktop_read' for item in inputs)
            read = next((t for t in payload.get('tools', []) if t.get('name') == 'Read'), None)
            if tool_case and not has_result and read:
                item = dict(id='fc_desktop_read', type='function_call', call_id='call_desktop_read',
                            name='Read', arguments=json.dumps({'file_path': str(fixture)}), status='completed')
                events = [('response.output_item.added', dict(type='response.output_item.added', output_index=0,
                          item=dict(item, arguments='', status='in_progress'))),
                          ('response.function_call_arguments.delta', dict(type='response.function_call_arguments.delta',
                           output_index=0, item_id=item['id'], delta=item['arguments'])),
                          ('response.function_call_arguments.done', dict(type='response.function_call_arguments.done',
                           output_index=0, item_id=item['id'], arguments=item['arguments'])),
                          ('response.output_item.done', dict(type='response.output_item.done', output_index=0, item=item))]
            else:
                item = dict(id='msg_desktop', type='message', role='assistant', status='completed',
                            content=[dict(type='output_text', text=MARKER, annotations=[])])
                events = [('response.output_item.added', dict(type='response.output_item.added', output_index=0,
                          item=dict(item, content=[]))),
                          ('response.content_part.added', dict(type='response.content_part.added', output_index=0,
                           item_id=item['id'], content_index=0, part=dict(type='output_text', text='', annotations=[]))),
                          ('response.output_text.delta', dict(type='response.output_text.delta', output_index=0,
                           item_id=item['id'], content_index=0, delta=MARKER))]
            response = dict(id='resp_desktop', object='response', created_at=1, status='completed',
                            model=payload.get('model'), output=[item],
                            usage=dict(input_tokens=20, output_tokens=5, total_tokens=25))
            events.insert(0, ('response.created', dict(type='response.created', response=dict(response, status='in_progress', output=[]))))
            events.append(('response.completed', dict(type='response.completed', response=response)))
            self.send_response(200)
            self.send_header('content-type', 'text/event-stream')
            self.send_header('connection', 'close')
            self.end_headers()
            for event, data in events:
                self.wfile.write(f'event: {event}\ndata: {json.dumps(data)}\n\n'.encode())
                self.wfile.flush()
            self.close_connection = True
    return Handler


def observer_handler(evidence, cpa_port):
    original = base.handler(evidence, cpa_port)

    class Handler(original):
        def do_POST(self):
            body = self.rfile.read(int(self.headers.get('content-length', '0')))
            request_id = str(uuid.uuid4())
            evidence.write('claude-to-cpa', self, body, request_id=request_id)
            connection = base.http.client.HTTPConnection('127.0.0.1', cpa_port, timeout=40)
            started, status, transfer_error = time.monotonic(), None, None
            observed = ResponseEvidence(False)
            try:
                connection.request('POST', self.path, body, headers={k: v for k, v in self.headers.items()
                                   if k.lower() not in ('host', 'connection', 'transfer-encoding')})
                response = connection.getresponse()
                status = response.status
                content_type = response.getheader('content-type', 'application/json')
                observed = ResponseEvidence('text/event-stream' in content_type)
                self.send_response(status)
                self.send_header('content-type', content_type)
                self.send_header('connection', 'close')
                self.end_headers()
                while chunk := response.read1(4096):
                    observed.feed(chunk)
                    self.wfile.write(chunk)
                    self.wfile.flush()
                observed.finish()
            except (OSError, base.http.client.HTTPException) as error:
                transfer_error = type(error).__name__
            finally:
                connection.close()
                self.close_connection = True
                evidence.write('cpa-to-claude-result', self, b'', request_id=request_id, status=status,
                               response_complete=observed.complete, reply_marker_present=observed.marker,
                               read_call_present=observed.read_call, read_call_timestamp=observed.read_call_timestamp,
                               transfer_error=transfer_error,
                               duration_ms=round((time.monotonic() - started) * 1000, 2))

        def do_GET(self):
            # 模型目录供 Desktop 配置窗口发现；不把凭证写到证据。
            connection = base.http.client.HTTPConnection('127.0.0.1', cpa_port, timeout=10)
            try:
                connection.request('GET', self.path, headers={k: v for k, v in self.headers.items()
                                   if k.lower() not in ('host', 'connection', 'transfer-encoding')})
                response = connection.getresponse()
                self.send_response(response.status)
                self.send_header('content-type', response.getheader('content-type', 'application/json'))
                self.send_header('connection', 'close')
                self.end_headers()
                self.wfile.write(response.read())
                self.close_connection = True
            finally:
                connection.close()
    return Handler


class DesktopMock:
    def __init__(self, binary, output):
        self.binary, self.output = str(Path(binary).resolve()), Path(output).resolve()
        if self.output.exists():
            raise ValueError('证据目录必须尚不存在')
        self.output.mkdir(mode=0o700, parents=True)
        self.evidence = Evidence(self.output / 'requests.jsonl')
        self.summary = dict(run_id=self.evidence.run_id, real_credentials=False, real_paid_calls=False,
                            client_identity='unverified', desktop_started_by_harness=False,
                            cpa_binary_sha256=hashlib.sha256(Path(self.binary).read_bytes()).hexdigest())
        self.stack, self.started = contextlib.ExitStack(), False

    def command(self, *args, stdin=None):
        return base.run([self.binary, *args], self.env, self.work, stdin)

    def __enter__(self):
        try:
            self.root = Path(tempfile.mkdtemp(prefix='cpa-desktop-mock-'))
            self.work = self.root / 'work'
            self.work.mkdir(mode=0o700)
            self.user_data = self.root / 'Claude-test-3p'
            self.user_data.mkdir(mode=0o700)
            library = self.user_data / 'configLibrary'
            library.mkdir(mode=0o700)
            self.env = {'PATH': os.environ['PATH'], 'HOME': str(self.root), 'TMPDIR': str(self.root),
                        'XDG_CONFIG_HOME': str(self.root / 'xdg'), 'CLAUDE_CONFIG_DIR': str(self.root / 'claude'),
                        'NO_PROXY': '127.0.0.1,localhost,::1'}
            self.fixture = self.work / 'fixture.txt'
            self.fixture.write_text(MARKER + '\n')
            self.fixture.chmod(0o600)
            upstream = self.stack.enter_context(base.server(upstream_handler(self.evidence, self.fixture)))
            model_file = self.root / 'models.yaml'
            model_file.write_text('models:\n  - id: gpt-6.1-sol\n')
            self.command('connection', 'add', 'desktop-mock', '--protocol', 'responses', '--base-url',
                         f'http://127.0.0.1:{upstream}', '--models', str(model_file), '--api-key-stdin', stdin='mock-not-real\n')
            bindings = self.root / 'bindings.yaml'
            bindings.write_text('models:\n' + ''.join(
                f'  {slot}:\n    public_model: {model}\n    connection: desktop-mock\n    target_model: gpt-6.1-sol\n'
                for slot, model in MODELS))
            self.command('profile', 'create', 'desktop-test', '--file', str(bindings))
            state = json.loads((self.root / 'xdg/cpa/state.json').read_text())
            profile = next(p for p in state['profiles'].values() if p['name'] == 'desktop-test')
            self.evidence.key = state['secrets'][profile['keyRef']]
            port = base.free_port()
            # start 可能先派生进程再失败；只有 stop 成功才能删除其身份与状态。
            self.started = True
            self.command('server', 'start', '--listen', f'127.0.0.1:{port}')
            observer = self.stack.enter_context(base.server(observer_handler(self.evidence, port)))
            self.endpoint = f'http://127.0.0.1:{observer}'
            config_id = str(uuid.uuid4())
            private_json(library / f'{config_id}.json', dict(inferenceProvider='gateway', inferenceCredentialKind='static',
                         inferenceGatewayBaseUrl=self.endpoint, inferenceGatewayApiKey=self.evidence.key,
                         inferenceGatewayAuthScheme='bearer', chatTabEnabled=True, coworkTabEnabled=False,
                         modelDiscoveryEnabled=False, inferenceModels=[dict(name=model,
                         labelOverride=f'CPA 测试 {slot}', anthropicFamilyTier=slot) for slot, model in MODELS]))
            private_json(library / '_meta.json', dict(appliedId=config_id, entries=[dict(id=config_id, name='CPA 隔离测试')]))
            private_json(self.user_data / 'claude_desktop_config.json', dict(deploymentMode='3p'))
            self.manifest = dict(run_id=self.evidence.run_id, temporary_root=str(self.root), user_data=str(self.user_data),
                                 workspace=str(self.work), fixture=str(self.fixture), gateway=self.endpoint,
                                 stop_file=str(self.root / 'STOP'),
                                 chat_prompt=f'Reply exactly {MARKER}. Do not use tools.',
                                 code_prompt=f'{TOOL_MARKER}: Use Read to read fixture.txt, then reply exactly its contents.')
            private_json(self.output / 'session.json', self.manifest)
            self.save_summary()
            return self
        except BaseException:
            self.__exit__(None, None, None)
            raise

    def stop_cpa(self):
        if self.started:
            try:
                self.command('server', 'stop')
                self.summary['service_stopped'] = True
                self.started = False
                self.summary.pop('cleanup_error_type', None)
                self.summary.pop('recovery', None)
            except Exception as error:
                self.summary['service_stopped'] = False
                self.summary['cleanup_error_type'] = type(error).__name__
                self.summary['recovery'] = dict(temporary_root=str(self.root), cpa_binary=self.binary,
                    config_home=self.env['XDG_CONFIG_HOME'], arguments=['server', 'stop'],
                    instruction='保留临时状态；使用该 config_home 作为 XDG_CONFIG_HOME 执行停止命令，确认停止后再删除临时根目录。')

    def save_summary(self):
        self.summary.update(self.evidence.snapshot())
        private_json(self.output / 'summary.json', self.summary)

    def __exit__(self, *_):
        try:
            self.stop_cpa()
            self.stack.close()
        finally:
            if hasattr(self, 'root') and not self.started and self.root.exists():
                shutil.rmtree(self.root)
            self.summary['temporary_state_removed'] = hasattr(self, 'root') and not self.root.exists()
            self.save_summary()
        return False


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--cpa', default='bin/cpa')
    parser.add_argument('--output', required=True, help='尚不存在的脱敏证据目录')
    parser.add_argument('--duration', type=float, default=900, help='最长等待秒数；创建 STOP 文件可提前结束')
    args = parser.parse_args()
    stopped = threading.Event()
    for name in (signal.SIGINT, signal.SIGTERM):
        signal.signal(name, lambda *_: stopped.set())
    with DesktopMock(args.cpa, args.output) as session:
        print(json.dumps(session.manifest, ensure_ascii=False), flush=True)
        end = time.monotonic() + args.duration
        while not stopped.is_set() and time.monotonic() < end and not (session.root / 'STOP').exists():
            stopped.wait(0.5)
            session.save_summary()
    print(json.dumps(session.summary, ensure_ascii=False), flush=True)
    return 1 if session.summary['workflow_status'] == 'failed' or not session.summary.get('service_stopped') else 0


if __name__ == '__main__':
    raise SystemExit(main())
