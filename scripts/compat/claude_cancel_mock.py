#!/usr/bin/env python3
"""在隔离HOME中向真实Claude发送SIGINT，再恢复已完成工具历史。"""
import importlib.util
import json
import signal
import subprocess
import sys
import threading
import time
from pathlib import Path

spec = importlib.util.spec_from_file_location('compat', Path(__file__).with_name('claude_mock.py'))
c = importlib.util.module_from_spec(spec)
spec.loader.exec_module(c)
original_run, original_handler = c.run, c.handler
output = Path(sys.argv[sys.argv.index('--output') + 1])
fixture = {'work': None, 'sigint_sent': False, 'forced_kill': False, 'upstream_closed': False}
release = threading.Event()


def run(args, env, cwd, stdin=None, timeout=45):
    if '--bare' not in args:
        return original_run(args, env, cwd, stdin, timeout)
    fixture['work'] = Path(cwd)
    (Path(cwd) / 'cancel-demo.txt').write_text('CANCEL_TOOL_HISTORY\n')
    command = list(args)
    command.remove('--no-session-persistence')
    command[command.index('--tools') + 1] = 'Read'
    session_id = str(c.uuid.uuid4())
    command += ['--allowedTools', 'Read', '--session-id', session_id]
    command[command.index('-p') + 1] = 'Read cancel-demo.txt, then explain the file contents.'
    first = list(command)
    first[first.index('--output-format') + 1] = 'stream-json'
    first += ['--verbose', '--include-partial-messages']
    stdout_path, stderr_path = output / 'cancel-stdout.jsonl', output / 'cancel-stderr.txt'
    with stdout_path.open('w') as stdout, stderr_path.open('w') as stderr:
        process = subprocess.Popen(first, env=env, cwd=cwd, stdout=stdout, stderr=stderr,
                                   start_new_session=True)
        try:
            deadline = time.monotonic() + 15
            while time.monotonic() < deadline:
                if 'CANCEL_PARTIAL' in stdout_path.read_text():
                    fixture['first_text_received'] = True
                    process.send_signal(signal.SIGINT)
                    fixture['sigint_sent'] = True
                    break
                if process.poll() is not None:
                    raise RuntimeError('Claude在发送SIGINT之前退出')
                time.sleep(0.02)
            assert fixture['sigint_sent'], '未观察到Claude输出首个文本块'
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                fixture['forced_kill'] = True
                process.kill()
                process.wait(timeout=3)
            fixture['cancel_exit_code'] = process.returncode
        finally:
            if process.poll() is None:
                process.kill()
                process.wait(timeout=3)
            release.set()
    assert not fixture['forced_kill'], 'SIGINT后5秒仍未退出，外层强制终止'
    pos = command.index('--session-id')
    del command[pos:pos + 2]
    command += ['--resume', session_id]
    command[command.index('-p') + 1] = 'Continue the canceled session. Reply exactly CPA_COMPAT_OK.'
    result = original_run(command, env, cwd, stdin, 20)
    assert json.loads(result).get('result', '').strip() == 'CPA_COMPAT_OK'
    fixture['resume_verified'] = True
    return result


def handler(recorder, proxy_port=None):
    if proxy_port is not None:
        return original_handler(recorder, proxy_port)

    class Handler(original_handler(recorder)):
        rounds = 0

        def do_POST(self):
            body = self.rfile.read(int(self.headers.get('content-length', '0')))
            payload = json.loads(body)
            recorder.write('cpa-to-responses', self, body)
            Handler.rounds += 1
            n = Handler.rounds
            if n == 1:
                item = {'id': 'fc_cancel_read', 'type': 'function_call', 'call_id': 'call_cancel_read',
                        'name': 'Read', 'arguments': json.dumps({'file_path': str(fixture['work'] / 'cancel-demo.txt')})}
                events = [
                    ('response.output_item.added', {'type': 'response.output_item.added', 'output_index': 0, 'item': dict(item, arguments='')}),
                    ('response.function_call_arguments.delta', {'type': 'response.function_call_arguments.delta', 'output_index': 0, 'item_id': item['id'], 'delta': item['arguments']}),
                    ('response.output_item.done', {'type': 'response.output_item.done', 'output_index': 0, 'item': item}),
                ]
            else:
                text = 'CANCEL_PARTIAL' if n == 2 else 'CPA_COMPAT_OK'
                item = {'id': f'msg_cancel_{n}', 'type': 'message', 'role': 'assistant', 'status': 'completed',
                        'content': [{'type': 'output_text', 'text': text, 'annotations': []}]}
                events = [
                    ('response.output_item.added', {'type': 'response.output_item.added', 'output_index': 0, 'item': dict(item, content=[])}),
                    ('response.content_part.added', {'type': 'response.content_part.added', 'output_index': 0, 'content_index': 0, 'part': {'type': 'output_text', 'text': ''}}),
                    ('response.output_text.delta', {'type': 'response.output_text.delta', 'output_index': 0, 'content_index': 0, 'delta': text}),
                ]
            response = {'id': f'resp_cancel_{n}', 'object': 'response', 'status': 'completed',
                        'model': payload['model'], 'output': [item],
                        'usage': {'input_tokens': 20, 'output_tokens': 5, 'total_tokens': 25}}
            events.insert(0, ('response.created', {'type': 'response.created', 'response': dict(response, status='in_progress', output=[])}))
            if n != 2:
                events.append(('response.completed', {'type': 'response.completed', 'response': response}))
            self.send_response(200)
            self.send_header('content-type', 'text/event-stream')
            self.send_header('connection', 'close')
            self.end_headers()
            try:
                for event, data in events:
                    self.wfile.write(f'event: {event}\ndata: {json.dumps(data)}\n\n'.encode())
                    self.wfile.flush()
                if n == 2:
                    # 保持上游流打开，用心跳观察CLI取消是否传至上游。
                    until = time.monotonic() + 8
                    while time.monotonic() < until:
                        self.wfile.write(b': heartbeat\n\n')
                        self.wfile.flush()
                        time.sleep(0.05)
                        if release.is_set():
                            until = min(until, time.monotonic() + 0.2)
            except (BrokenPipeError, ConnectionResetError):
                if n == 2:
                    fixture['upstream_closed'] = True
            finally:
                self.close_connection = True
    return Handler


c.run, c.handler = run, handler
c.main()
entries = [json.loads(line) for line in (output / 'requests.jsonl').read_text().splitlines()]
rounds = [entry for entry in entries if entry['stage'] == 'cpa-to-responses']
assert len(rounds) == 3, f'预期工具、被取消流、resume三个请求，实际{len(rounds)}'
history = rounds[-1]['body'].get('input', [])
fixture['resumed_tool_history_verified'] = any(
    item.get('type') == 'function_call_output' and item.get('call_id') == 'call_cancel_read'
    and 'CANCEL_TOOL_HISTORY' in str(item.get('output')) for item in history)
assert fixture['resumed_tool_history_verified'], 'resume请求未保留取消前已完成的Read结果'
fixture.pop('work', None)
events = [json.loads(line) for line in (output / 'cancel-stdout.jsonl').read_text().splitlines() if line.strip()]
fixture['aborted_marker_seen'] = any(event.get('aborted') is True for event in events)
fixture['user_interrupt_marker_seen'] = any('[Request interrupted by user]' in json.dumps(event) for event in events)
assert fixture['aborted_marker_seen'] and fixture['user_interrupt_marker_seen']
fixture['signal_semantics'] = 'Popen.send_signal(SIGINT)，没有操作交互终端Ctrl+C键'
fixture['requests'] = len(rounds)
(output / 'cancel-summary.json').write_text(json.dumps(fixture, ensure_ascii=False, indent=2) + '\n')
print(json.dumps(fixture, ensure_ascii=False))
