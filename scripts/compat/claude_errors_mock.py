#!/usr/bin/env python3
"""观察真实Claude对mock错误的重试，最多三个客户端请求、20秒外层期限。"""
import importlib.util
import json
import subprocess
import sys
import threading
import time
from pathlib import Path

case_pos = sys.argv.index('--case')
case = sys.argv[case_pos + 1]
del sys.argv[case_pos:case_pos + 2]
assert case in ('429', '500', 'disconnect')
output = Path(sys.argv[sys.argv.index('--output') + 1])
spec = importlib.util.spec_from_file_location('compat', Path(__file__).with_name('claude_mock.py'))
c = importlib.util.module_from_spec(spec)
spec.loader.exec_module(c)
original_run, original_handler = c.run, c.handler
fixture = {'case': case, 'client_requests': 0, 'upstream_requests': 0, 'harness_stopped': False,
           'stop_reason': None, 'outer_timeout_seconds': 20, 'request_budget': 3}
lock = threading.Lock()
budget = threading.Event()


class ObservationComplete(Exception):
    pass


def run(args, env, cwd, stdin=None, timeout=45):
    if '--bare' not in args:
        return original_run(args, env, cwd, stdin, timeout)
    fixture['temporary_home'] = env['HOME']
    command = list(args)
    command[command.index('--output-format') + 1] = 'stream-json'
    command += ['--verbose', '--include-partial-messages']
    started = time.monotonic()
    with (output / 'error-stdout.txt').open('w') as stdout, (output / 'error-stderr.txt').open('w') as stderr:
        process = subprocess.Popen(command, env=env, cwd=cwd, stdout=stdout, stderr=stderr,
                                   start_new_session=True)
        try:
            while process.poll() is None:
                if budget.is_set():
                    # 三个响应都已经发送；若客户端尚未退出，由harness终止，不能记为客户端自行停止。
                    try:
                        process.wait(timeout=0.1)
                    except subprocess.TimeoutExpired:
                        fixture['harness_stopped'] = True
                        fixture['stop_reason'] = 'request_budget_3'
                        process.terminate()
                    break
                if time.monotonic() - started >= 20:
                    fixture['harness_stopped'] = True
                    fixture['stop_reason'] = 'outer_timeout_20s'
                    process.terminate()
                    break
                time.sleep(0.01)
            try:
                process.wait(timeout=3)
            except subprocess.TimeoutExpired:
                fixture['forced_kill'] = True
                process.kill()
                process.wait(timeout=3)
            fixture['exit_code'] = process.returncode
        finally:
            if process.poll() is None:
                process.kill()
                process.wait(timeout=3)
    fixture['duration_seconds'] = round(time.monotonic() - started, 3)
    raise ObservationComplete('错误行为已记录，不将外层终止当作Claude正常完成')


def handler(recorder, proxy_port=None):
    Base = original_handler(recorder, proxy_port)
    if proxy_port is not None:
        class Observer(Base):
            def do_POST(self):
                with lock:
                    fixture['client_requests'] += 1
                    count = fixture['client_requests']
                if count > 3:
                    self.close_connection = True
                    raise RuntimeError('harness请求预算超出')
                try:
                    super().do_POST()
                finally:
                    if count == 3:
                        budget.set()
        return Observer

    class Upstream(Base):
        def do_POST(self):
            body = self.rfile.read(int(self.headers.get('content-length', '0')))
            recorder.write('cpa-to-responses', self, body)
            with lock:
                fixture['upstream_requests'] += 1
            self.send_response(200 if case == 'disconnect' else int(case))
            self.send_header('content-type', 'text/event-stream' if case == 'disconnect' else 'application/json')
            self.send_header('connection', 'close')
            self.end_headers()
            if case == 'disconnect':
                payload = json.loads(body)
                events = [
                    {'type': 'response.created', 'response': {'id': 'resp_error', 'model': payload['model'], 'output': []}},
                    {'type': 'response.output_item.added', 'output_index': 0, 'item': {'id': 'msg_error', 'type': 'message', 'role': 'assistant', 'content': []}},
                    {'type': 'response.content_part.added', 'output_index': 0, 'content_index': 0, 'part': {'type': 'output_text', 'text': ''}},
                    {'type': 'response.output_text.delta', 'output_index': 0, 'content_index': 0, 'delta': 'ERROR_PARTIAL'},
                ]
                for event in events:
                    self.wfile.write(f'data: {json.dumps(event)}\n\n'.encode())
                    self.wfile.flush()
            else:
                self.wfile.write(json.dumps({'error': {'message': f'CPA_MOCK_ERROR_{case}', 'type': 'compat_error', 'code': 'compat_error'}}).encode())
                self.wfile.flush()
            self.close_connection = True
    return Upstream


c.run, c.handler = run, handler
try:
    c.main()
except ObservationComplete:
    fixture['observation_complete'] = True
entries = [json.loads(line) for line in (output / 'requests.jsonl').read_text().splitlines()]
client = [entry for entry in entries if entry['stage'] == 'claude-to-cpa']
upstream = [entry for entry in entries if entry['stage'] == 'cpa-to-responses']
fixture['temporary_state_removed'] = not Path(fixture.pop('temporary_home')).exists()
stdout_events = []
for line in (output / 'error-stdout.txt').read_text().splitlines():
    try:
        stdout_events.append(json.loads(line))
    except json.JSONDecodeError:
        pass
fixture['retry_events'] = [{key: entry.get(key) for key in ('attempt', 'max_retries', 'retry_delay_ms', 'error_status', 'error')} for entry in stdout_events if entry.get('subtype') == 'api_retry']
fixture['client_intervals_seconds'] = [round(b['timestamp'] - a['timestamp'], 3) for a, b in zip(client, client[1:])]
fixture['upstream_intervals_seconds'] = [round(b['timestamp'] - a['timestamp'], 3) for a, b in zip(upstream, upstream[1:])]
fixture['client_stream_flags'] = [entry['body'].get('stream') for entry in client]
fixture['upstream_stream_flags'] = [entry['body'].get('stream') for entry in upstream]
fixture['client_statuses'] = [entry['status'] for entry in entries if entry['stage'] == 'cpa-to-claude-result']
summary = json.loads((output / 'summary.json').read_text())
fixture['cleanup_verified'] = summary['restore_readback'] and summary['service_stopped'] and not summary['cleanup_errors']
assert fixture['temporary_state_removed']
assert fixture['cleanup_verified']
assert fixture['client_requests'] <= 3
(output / 'error-summary.json').write_text(json.dumps(fixture, ensure_ascii=False, indent=2) + '\n')
print(json.dumps(fixture, ensure_ascii=False))
