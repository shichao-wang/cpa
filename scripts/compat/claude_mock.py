#!/usr/bin/env python3
"""隔离运行真实 Claude Code，经 CPA 访问 Responses mock；不读取真实凭证。"""
import argparse
import contextlib
import hashlib
import http.client
import json
import os
from pathlib import Path
import shutil
import socket
import subprocess
import tempfile
import threading
import time
import uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


def free_port():
    with socket.socket() as sock:
        sock.bind(('127.0.0.1', 0))
        return sock.getsockname()[1]


def run(args, env, cwd, stdin=None, timeout=45):
    result = subprocess.run(args, input=stdin, text=True, capture_output=True,
                            env=env, cwd=cwd, timeout=timeout)
    if result.returncode:
        raise RuntimeError(f'{args[0]} {args[1:3]} 失败：{result.stderr[-1500:]}')
    return result.stdout


class Recorder:
    def __init__(self, path):
        self.path = path
        self.lock = threading.Lock()
        self.run_id = str(uuid.uuid4())

    def write(self, stage, request, body, **extra):
        # 只允许记录无凭证的白名单 header。请求正文仅含本 harness 的测试内容。
        headers = {key: request.headers.get(key) for key in
                   ('anthropic-version', 'anthropic-beta', 'content-type', 'x-request-id')
                   if request.headers.get(key)}
        entry = dict(run_id=self.run_id, timestamp=time.time(), stage=stage, path=request.path, headers=headers,
                     body=json.loads(body) if body else None, **extra)
        with self.lock, self.path.open('a') as stream:
            stream.write(json.dumps(entry, ensure_ascii=False) + '\n')


def handler(recorder, proxy_port=None):
    class Handler(BaseHTTPRequestHandler):
        protocol_version = 'HTTP/1.1'

        def log_message(self, *_):
            pass

        def do_POST(self):
            body = self.rfile.read(int(self.headers.get('content-length', '0')))
            if proxy_port is not None:
                started = time.monotonic()
                recorder.write('claude-to-cpa', self, body)
                conn = http.client.HTTPConnection('127.0.0.1', proxy_port, timeout=40)
                headers = {k: v for k, v in self.headers.items()
                           if k.lower() not in ('host', 'connection', 'transfer-encoding')}
                conn.request('POST', self.path, body, headers)
                response = conn.getresponse()
                self.send_response(response.status)
                self.send_header('content-type', response.getheader('content-type', 'application/json'))
                self.send_header('connection', 'close')
                self.end_headers()
                try:
                    while chunk := response.read1(4096):
                        self.wfile.write(chunk)
                        self.wfile.flush()
                finally:
                    conn.close()
                    self.close_connection = True
                    recorder.write('cpa-to-claude-result', self, b'',
                                   status=response.status, duration_ms=round((time.monotonic() - started) * 1000, 2))
                return
            recorder.write('cpa-to-responses', self, body)
            payload = json.loads(body)
            response = {'id': 'resp_compat', 'object': 'response', 'created_at': 1,
                        'model': payload.get('model'), 'status': 'completed',
                        'output': [{'id': 'msg_compat', 'type': 'message', 'role': 'assistant',
                                    'status': 'completed', 'content': [
                                        {'type': 'output_text', 'text': 'CPA_COMPAT_OK', 'annotations': []}]}],
                        'usage': {'input_tokens': 20, 'output_tokens': 5, 'total_tokens': 25}}
            events = [
                ('response.created', {'type': 'response.created', 'response': dict(response, status='in_progress', output=[])}),
                ('response.output_item.added', {'type': 'response.output_item.added', 'output_index': 0,
                 'item': dict(response['output'][0], content=[])}),
                ('response.content_part.added', {'type': 'response.content_part.added', 'item_id': 'msg_compat',
                 'output_index': 0, 'content_index': 0, 'part': {'type': 'output_text', 'text': '', 'annotations': []}}),
                ('response.output_text.delta', {'type': 'response.output_text.delta', 'item_id': 'msg_compat',
                 'output_index': 0, 'content_index': 0, 'delta': 'CPA_COMPAT_OK'}),
                ('response.completed', {'type': 'response.completed', 'response': response}),
            ]
            self.send_response(200)
            self.send_header('content-type', 'text/event-stream')
            self.send_header('connection', 'close')
            self.end_headers()
            for event, data in events:
                self.wfile.write(f'event: {event}\ndata: {json.dumps(data)}\n\n'.encode())
                self.wfile.flush()
            self.close_connection = True
    return Handler


@contextlib.contextmanager
def server(handler_class):
    instance = ThreadingHTTPServer(('127.0.0.1', 0), handler_class)
    thread = threading.Thread(target=instance.serve_forever, daemon=True)
    thread.start()
    try:
        yield instance.server_port
    finally:
        instance.shutdown()
        instance.server_close()
        thread.join()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--cpa', default='bin/cpa')
    parser.add_argument('--output', required=True, help='脱敏证据目录，不能使用真实状态目录')
    parser.add_argument('--model', default='claude-sonnet-4-6')
    parser.add_argument('--effort')
    parser.add_argument('--target-model', default='gpt-6.1-sol')
    parser.add_argument('--settings-json', default='{}')
    args = parser.parse_args()
    binary = str(Path(args.cpa).resolve())
    claude = shutil.which('claude')
    if not claude:
        raise RuntimeError('未安装 Claude Code')
    output = Path(args.output).resolve()
    output.mkdir(mode=0o700, parents=True, exist_ok=True)
    trace = output / 'requests.jsonl'
    trace.write_text('')
    trace.chmod(0o600)
    recorder = Recorder(trace)
    with tempfile.TemporaryDirectory(prefix='cpa-compat-') as temp:
        root = Path(temp)
        config_dir, work = root / 'claude', root / 'work'
        config_dir.mkdir(mode=0o700)
        work.mkdir(mode=0o700)
        env = {'PATH': os.environ['PATH'], 'HOME': str(root),
               'TMPDIR': str(root), 'CLAUDE_CONFIG_DIR': str(config_dir),
               'DISABLE_TELEMETRY': '1', 'DISABLE_ERROR_REPORTING': '1',
               'DISABLE_AUTOUPDATER': '1', 'CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC': '1',
               'NO_PROXY': '127.0.0.1,localhost'}
        summary = {'claude_version': run([claude, '--version'], env, work).strip(),
                   'cpa_version': run([binary, 'version'], env, work).strip(),
                   'isolation': {'temporary_home': True, 'bare': True, 'setting_sources': [],
                                 'real_credentials': False, 'upstream': 'loopback-mock'},
                   'model': args.model, 'target_model': args.target_model, 'effort': args.effort, 'run_id': recorder.run_id,
                   'cpa_binary_sha256': hashlib.sha256(Path(binary).read_bytes()).hexdigest()}
        build_info = subprocess.run(['go', 'version', '-m', binary], capture_output=True, text=True, env=env).stdout
        summary['cpa_build_info'] = [line for line in build_info.splitlines()
                                     if any(key in line for key in ('go1.', 'CLIProxyAPI', 'vcs.'))]
        def cpa(*commands, stdin=None):
            return run([binary, '--state-dir', str(root / 'state'), *commands], env, work, stdin)
        with server(handler(recorder)) as upstream_port:
            models = root / 'models.yaml'
            models.write_text('models:\n  - id: ' + json.dumps(args.target_model) + '\n')
            cpa('connection', 'add', 'mock', '--protocol', 'responses', '--base-url',
                f'http://127.0.0.1:{upstream_port}', '--models', str(models), '--api-key-stdin',
                stdin='mock-credential-not-real\n')
            bindings = root / 'bindings.yaml'
            bindings.write_text('models:\n' + ''.join(
                f'  {slot}:\n    public_model: {model}\n    connection: mock\n    target_model: {json.dumps(args.target_model)}\n'
                for slot, model in [('opus', 'claude-opus-4-6'), ('sonnet', 'claude-sonnet-4-6'), ('haiku', 'claude-haiku-4-5-20251001')]))
            cpa('profile', 'create', 'compat', '--file', str(bindings))
            port = free_port()
            started = False
            applied_ok = False
            settings = config_dir / 'settings.json'
            original = {'env': {'CPA_COMPAT_UNRELATED': 'preserved'}, 'language': 'English'}
            settings.write_text(json.dumps(original))
            settings.chmod(0o600)
            try:
                cpa('server', 'start', '--listen', f'127.0.0.1:{port}')
                started = True
                cpa('profile', 'apply', 'compat', '--settings', str(settings))
                applied_ok = True
                applied = json.loads(settings.read_text())
                assert applied['env']['CPA_COMPAT_UNRELATED'] == 'preserved'
                assert applied['env']['ANTHROPIC_BASE_URL'] == f'http://127.0.0.1:{port}'
                summary['apply_readback'] = True
                # bare 不读取 keychain/OAuth，只允许 API_KEY 或 apiKeyHelper；将临时 profile key 通过 env 传入。
                with server(handler(recorder, port)) as observer_port:
                    claude_env = dict(env, **applied['env'])
                    claude_env['ANTHROPIC_BASE_URL'] = f'http://127.0.0.1:{observer_port}'
                    claude_env['ANTHROPIC_API_KEY'] = claude_env.pop('ANTHROPIC_AUTH_TOKEN')
                    isolated_settings = root / 'run-settings.json'
                    isolated_settings.write_text(args.settings_json)
                    command = [claude, '--bare', '--setting-sources', '', '--settings', str(isolated_settings),
                               '--strict-mcp-config', '--mcp-config', '{"mcpServers":{}}', '--tools', '',
                               '--no-session-persistence', '--model', args.model, '--output-format', 'json',
                               '-p', 'Reply exactly CPA_COMPAT_OK. Do not use tools.']
                    if args.effort:
                        command += ['--effort', args.effort]
                    result = run(command, claude_env, work, timeout=60)
                    (output / 'claude-result.json').write_text(result)
                    parsed = json.loads(result)
                    summary['claude_exit_success'] = not parsed.get('is_error', False)
                    summary['mock_reply_received'] = parsed.get('result', '').strip() == 'CPA_COMPAT_OK'
                    assert summary['mock_reply_received'], 'Claude 未收到预期 mock 回复'
            except Exception as error:
                summary['run_error_type'] = type(error).__name__
                raise
            finally:
                cleanup_errors = []
                if applied_ok:
                    try:
                        cpa('profile', 'restore', '--settings', str(settings))
                        summary['restore_readback'] = json.loads(settings.read_text()) == original
                    except Exception as error:
                        summary['restore_readback'] = False
                        cleanup_errors.append(str(error))
                if started:
                    try:
                        cpa('server', 'stop')
                        summary['service_stopped'] = True
                    except Exception as error:
                        summary['service_stopped'] = False
                        cleanup_errors.append(str(error))
                summary['cleanup_errors'] = cleanup_errors
                (output / 'summary.json').write_text(json.dumps(summary, ensure_ascii=False, indent=2) + '\n')
            if cleanup_errors:
                raise RuntimeError('临时环境清理失败，见 summary.json')
            assert summary.get('restore_readback'), 'restore 回读不一致'
        entries = [json.loads(line) for line in trace.read_text().splitlines()]
        assert any(item['stage'] == 'claude-to-cpa' for item in entries)
        assert any(item['stage'] == 'cpa-to-responses' for item in entries)
        summary['observed_both_boundaries'] = True
    summary['temporary_state_removed'] = not root.exists()
    (output / 'summary.json').write_text(json.dumps(summary, ensure_ascii=False, indent=2) + '\n')
    print(json.dumps(summary, ensure_ascii=False))


if __name__ == '__main__':
    main()
