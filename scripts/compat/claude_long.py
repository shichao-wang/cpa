#!/usr/bin/env python3
"""由隔离 Go harness 启动；只记录合成结果与会话压缩元数据。"""
import argparse
import json
import hashlib
import os
from pathlib import Path
import re
import shutil
import subprocess
import time
import uuid
import atexit
import queue
import threading
import pty
import select
import termios
import fcntl
import struct
from types import SimpleNamespace


def read_session_records(config_dir, session_id):
    """仅扫描会话目录，避免遍历客户端会创建、删除的锁目录。"""
    records = []
    for file in (config_dir / 'projects').glob('*/' + session_id + '.jsonl'):
        try:
            lines = file.read_text().splitlines()
        except FileNotFoundError:
            continue
        for line in lines:
            try:
                records.append(json.loads(line))
            except json.JSONDecodeError:
                # 活跃会话最后一条记录可能尚未写完，下次读取再检查。
                pass
    return records


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--case', required=True)
    parser.add_argument('--output', required=True)
    parser.add_argument('--mock', action='store_true')
    parser.add_argument('--mock-tools', action='store_true')
    parser.add_argument('--recover', action='store_true')
    parser.add_argument('--diagnose-only', action='store_true')
    parser.add_argument('--persistent', action='store_true')
    parser.add_argument('--interactive', action='store_true')
    parser.add_argument('--capacity', action='store_true')
    parser.add_argument('--separate-processes', action='store_true')
    parser.add_argument('--compact-format', choices=['json', 'stream-json'], default='stream-json')
    args = parser.parse_args()
    args.persistent = args.persistent or (not args.separate_processes and not args.case.startswith(('long-', 'capacity-')))
    output = Path(args.output)
    output.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    sid = str(uuid.uuid4())
    facts = {k: os.urandom(8).hex() for k in ('FIRST', 'MIDDLE', 'LAST')}
    rows = []
    compact = []
    base = [shutil.which('claude'), '--bare', '--setting-sources', '',
            '--strict-mcp-config', '--mcp-config', '{"mcpServers":{}}',
            '--model', 'claude-sonnet-4-6[1m]' if args.capacity else 'claude-sonnet-4-6', '--effort', 'low',
            '--output-format', 'json', '--max-turns', '6']

    def save():
        output.write_text(json.dumps({'case': args.case, 'session_id': sid,
                                     'facts': facts, 'steps': rows,
                                     'compact_boundaries': compact}, indent=2))
        output.chmod(0o600)

    session_process = None
    session_events = None

    def close_session():
        nonlocal session_process
        if session_process is not None:
            session_process.stdin.close()
            try:
                exit_code = session_process.wait(timeout=30)
                if exit_code != 0:
                    raise RuntimeError('持久会话退出失败')
            except subprocess.TimeoutExpired:
                session_process.terminate()
                session_process.wait(timeout=5)
                raise RuntimeError('持久会话没有正常退出')
            session_process = None

    def cleanup_session():
        if session_process is not None and session_process.poll() is None:
            session_process.terminate()
            try:
                session_process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                session_process.kill()
                session_process.wait(timeout=5)
    atexit.register(cleanup_session)

    def run_session(command, prompt, name):
        nonlocal session_process, session_events
        if session_process is None:
            command = list(command)
            command += ['--input-format', 'stream-json']
            session_process = subprocess.Popen(command, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                               stderr=subprocess.DEVNULL, text=True, env=os.environ.copy())
            session_events = queue.Queue()
            process = session_process
            events = session_events
            def read_lines():
                for line in process.stdout:
                    events.put(line)
                events.put(None)
            threading.Thread(target=read_lines, daemon=True).start()
        request_uuid = str(uuid.uuid4())
        session_process.stdin.write(json.dumps({'type': 'user', 'uuid': request_uuid, 'session_id': sid,
                                               'message': {'role': 'user', 'content': prompt}}) + '\n')
        session_process.stdin.flush()
        lines = []
        while True:
            line = session_events.get(timeout=180)
            if line is None:
                raise RuntimeError('持久会话在结果返回前退出')
            lines.append(line)
            event = json.loads(line)
            if event.get('type') == 'result':
                bound = event.get('user_message_uuid')
                bounds = event.get('user_message_uuids', [])
                if bound and bound != request_uuid:
                    continue
                if bounds and request_uuid not in bounds:
                    continue
                break
        if name in ('after_compact', 'continue_task', 'restart_resume'):
            close_session()
        return SimpleNamespace(returncode=0, stdout=''.join(lines), stderr='')

    terminal = None
    terminal_fd = None
    terminal_capture = bytearray()
    terminal_controls = bytearray()

    def flush_terminal_controls():
        while terminal_controls:
            try:
                written = os.write(terminal_fd, terminal_controls)
            except BlockingIOError:
                return
            del terminal_controls[:written]

    def queue_terminal_control(data):
        terminal_controls.extend(data)
        flush_terminal_controls()

    def terminal_records():
        return read_session_records(Path(os.environ['CLAUDE_CONFIG_DIR']), sid)

    def drain_terminal():
        while select.select([terminal_fd], [], [], 0)[0]:
            try:
                chunk = os.read(terminal_fd, 65536)
            except OSError:
                break
            if not chunk:
                break
            terminal_capture.extend(chunk)
            if b'\x1b[6n' in chunk:
                queue_terminal_control(b'\x1b[1;1R')
            if b'\x1b[c' in chunk:
                queue_terminal_control(b'\x1b[?1;2c')
            if len(terminal_capture) > 200000:
                del terminal_capture[:-100000]
        flush_terminal_controls()

    def stop_terminal():
        nonlocal terminal
        if terminal is not None:
            if terminal.poll() is None:
                queue_terminal_control(b'\x04')
                time.sleep(.3)
                queue_terminal_control(b'\x04')
                limit = time.monotonic() + 15
                while terminal.poll() is None and time.monotonic() < limit:
                    drain_terminal()
                    time.sleep(.1)
                if terminal.poll() is None:
                    terminal.terminate()
            exit_code = terminal.wait(timeout=5)
            terminal = None
            os.close(terminal_fd)
            if exit_code != 0:
                raise RuntimeError('交互终端没有正常退出')
    atexit.register(stop_terminal)

    def run_terminal(command, prompt, name):
        nonlocal terminal, terminal_fd
        if name == 'restart_resume':
            stop_terminal()
        if terminal is None:
            command = list(command)
            command.remove('-p')
            output_index = command.index('--output-format')
            del command[output_index:output_index + 2]
            if '--verbose' in command:
                command.remove('--verbose')
            tools_index = command.index('--tools')
            command[tools_index + 1] = 'Read,Edit,Bash'
            command += ['--allowedTools', 'Read,Edit,Bash', '--permission-mode', 'acceptEdits']
            terminal_fd, slave = pty.openpty()
            os.set_blocking(terminal_fd, False)
            fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack('HHHH', 40, 160, 0, 0))
            terminal = subprocess.Popen(command, stdin=slave, stdout=slave, stderr=slave,
                                        env=dict(os.environ, TERM='xterm-256color'), start_new_session=True)
            os.close(slave)
            terminal_capture.clear()
            terminal_controls.clear()
            deadline = time.monotonic() + 40
            handled = set()
            while time.monotonic() < deadline:
                drain_terminal()
                text = re.sub(r'\x1b\[[0-?]*[ -/]*[@-~]', '', terminal_capture.decode(errors='ignore'))
                plain = re.sub(r'\s+', '', text).lower()
                if 'choosethetextstyle' in plain and 'theme' not in handled:
                    queue_terminal_control(b'\r')
                    handled.add('theme')
                    time.sleep(.3)
                elif 'doyouwanttousethisapikey' in plain and 'key' not in handled:
                    queue_terminal_control(b'\x1b[A\r')
                    handled.add('key')
                    time.sleep(.3)
                elif 'pressentertocontinue' in plain and 'security' not in handled:
                    queue_terminal_control(b'\r')
                    handled.add('security')
                    time.sleep(.3)
                elif ('trustthisfolder' in plain or 'trustthisworkspace' in plain or 'yes,itrustthisfolder' in plain) and 'trust' not in handled:
                    queue_terminal_control(b'\x1b[B\r')
                    handled.add('trust')
                    time.sleep(.3)
                elif 'forshortcuts' in plain or ('❯' in text and 'apiusagebilling' in plain and 'accepteditson' in plain):
                    break
                time.sleep(.1)
            else:
                raise RuntimeError('交互终端未完成临时环境初始化')
        before = len(terminal_records())
        encoded = ('\x1b[200~' + prompt + '\x1b[201~').encode()
        offset = 0
        write_deadline = time.monotonic() + 30
        while offset < len(encoded):
            drain_terminal()
            try:
                if terminal_controls:
                    raise BlockingIOError()
                offset += os.write(terminal_fd, encoded[offset:offset+2048])
            except BlockingIOError:
                if time.monotonic() > write_deadline:
                    raise RuntimeError('交互终端输入阻塞')
                time.sleep(.01)
        time.sleep(.5)
        queue_terminal_control(b'\r')
        deadline = time.monotonic() + 180
        result_text = ''
        while time.monotonic() < deadline:
            drain_terminal()
            if terminal.poll() is not None:
                raise RuntimeError('交互终端提前退出')
            records = terminal_records()
            recent = records[before:]
            if name.startswith('compact'):
                if any(r.get('subtype') == 'compact_boundary' for r in recent):
                    result_text = 'compact'
                    break
            else:
                texts = []
                for record in recent:
                    if record.get('type') == 'assistant':
                        texts += [c.get('text', '') for c in record.get('message', {}).get('content', []) if c.get('type') == 'text']
                result_text = '\n'.join(texts)
                expected = 'READY' if name == 'seed' else 'DONE' if name == 'continue_task' else facts['LAST']
                if expected in result_text:
                    break
            time.sleep(.2)
        else:
            # TUI 可能包含凭证片段；超时时只报告错误，不保存原始终端内容。
            raise RuntimeError('交互终端等待验收结果超时')
        time.sleep(1)
        value = {'type': 'result', 'session_id': sid, 'result': result_text,
                 'local_command': 'compact' if name.startswith('compact') else None}
        if name == 'restart_resume':
            stop_terminal()
        return SimpleNamespace(returncode=0, stdout=json.dumps(value) + '\n', stderr='')

    def invoke(name, prompt, first=False, tools='', expect_failure=False):
        command = base + ['--tools', tools]
        if tools:
            command += ['--allowedTools', tools, '--permission-mode', 'acceptEdits']
        command += ['--session-id' if first else '--resume', sid, '-p']
        stream = args.persistent or (name.startswith('compact') and args.compact_format == 'stream-json')
        if stream:
            command[command.index('--output-format') + 1] = 'stream-json'
            command.append('--verbose')
        started = time.monotonic()
        try:
            result = run_terminal(command, prompt, name) if args.interactive else run_session(command, prompt, name) if args.persistent else subprocess.run(
                command, input=prompt, text=True, capture_output=True, timeout=180, env=os.environ.copy())
            # 不保存 stderr：客户端首次启动可能显示临时 API key 的片段。
            events = [json.loads(line) for line in result.stdout.splitlines()] if stream else []
            value = next((event for event in reversed(events) if event.get('type') == 'result'), {}) if stream else json.loads(result.stdout)
            row = {'step': name, 'exit_code': result.returncode,
                   'is_error': value.get('is_error'), 'subtype': value.get('subtype'), 'returned_session': value.get('session_id'),
                   'result_user_uuid': value.get('user_message_uuid'), 'result_index': value.get('result_index'),
                   'local_command': value.get('local_command'), 'result': value.get('result'),
                   'command_diagnostics': '\n'.join(line for line in result.stderr.replace(os.environ.get('ANTHROPIC_API_KEY', 'not-a-key'), '[REDACTED]').splitlines() if not re.search(r'key|token|authorization', line, re.I))[-3000:] if name == 'compact' else '',
                   'usage': value.get('usage'), 'modelUsage': value.get('modelUsage'),
                   'duration_s': round(time.monotonic() - started, 2)}
            row['input_sha256'] = hashlib.sha256(prompt.encode()).hexdigest()
            row['input_bytes'] = len(prompt.encode())
            row['compact_events'] = [{key: event.get(key) for key in ['type','subtype','status','metadata','compact_metadata'] if key in event} for event in events if event.get('type') == 'system' and event.get('subtype') != 'init']
            rows.append(row)
            records=read_session_records(Path(os.environ['CLAUDE_CONFIG_DIR']),sid)
            row['session_record_types']=[{key:record.get(key) for key in ['type','subtype','isCompactSummary','compactMetadata'] if key in record} for record in records]
            for record in records:
                if record.get('subtype')=='compact_boundary':
                    entry={'subtype':record['subtype'],'metadata':record.get('compactMetadata')}
                    if entry not in compact:
                        compact.append(entry)
            save()
            if (result.returncode or value.get('is_error')) and not expect_failure:
                raise RuntimeError('客户端返回错误，停止本用例')
            return value
        except Exception as error:
            rows.append({'step': name, 'failure_type': type(error).__name__})
            save()
            raise

    def document(size):
        # " a" 使用 o200k_base 约为一token，最终以实际上游 usage 为准。
        part = ' a' * (size // 2)
        return ('<document>\nCPA_FACT_FIRST=' + facts['FIRST'] + '\n' + part +
                '\nCPA_FACT_MIDDLE=' + facts['MIDDLE'] + '\n' + part +
                '\nCPA_FACT_LAST=' + facts['LAST'] + '\n</document>\n')

    recall = ('Return only a JSON object with keys FIRST, MIDDLE, LAST and the exact '
              'CPA_FACT values from the document. No tools, no explanation.')
    if args.case.startswith(('long-', 'capacity-')):
        size = int(args.case.split('-')[1]) * 1000
        value = invoke('recall', document(size) + recall, first=True)
        match = re.search(r'\{[^{}]*\}', value.get('result', ''))
        actual = json.loads(match.group()) if match else None
        rows[-1]['recall_exact'] = actual == facts
        save()
        if actual != facts:
            raise RuntimeError('首中尾随机事实召回不匹配')
    else:
        auto = args.case == 'auto'
        if auto:
            settings = Path('auto-settings.json')
            settings.write_text(json.dumps({'autoCompactEnabled': True}))
            base.extend(['--settings', str(settings.resolve()), '--autocompact', '200000'])
        Path('fixture.txt').write_text('compat_before\n')
        Path('protected.txt').write_text('do_not_change\n')
        size = 192000 if auto else 48000
        task = ('Keep all three CPA_FACT values. The remaining task is to change fixture.txt '
                'from compat_before to compat_after and verify it. Never modify protected.txt. '
                'Do not perform this task yet. Reply READY only.')
        if not auto:
            task += ' First use Read exactly once to inspect fixture.txt, then reply READY.'
        invoke('seed', document(size) + task, first=True, tools='' if auto else 'Read')
        if not auto:
            value = invoke('compact', '/compact Preserve all CPA_FACT values, the pending edit task, '
                           'the protected.txt prohibition, and the completed Read tool result.', expect_failure=args.recover)
            if args.recover:
                if compact:
                    raise RuntimeError('错误摘要不应生成压缩边界')
                value = invoke('compact_retry', '/compact CPA_RETRY Preserve all CPA_FACT values and pending tasks.')
            if value.get('local_command') != 'compact':
                raise RuntimeError('没有执行本地 compact 命令')
        value = invoke('after_compact', recall)
        match = re.search(r'\{[^{}]*\}', value.get('result', ''))
        rows[-1]['recall_exact'] = bool(match) and json.loads(match.group()) == facts
        save()
        if not rows[-1]['recall_exact']:
            raise RuntimeError('压缩后随机事实丢失')
        if not compact:
            raise RuntimeError('没有观察到实际 compact_boundary，不能视为压缩通过')
        if not auto and (not args.mock or args.mock_tools) and not args.diagnose_only:
            invoke('continue_task', 'Now complete the pending edit task using Edit and Bash. '
                   'Use Bash to verify fixture.txt contains compat_after and protected.txt is unchanged. '
                   'Reply DONE only after verification.', tools='Read,Edit,Bash')
            rows[-1]['file_edit_verified'] = Path('fixture.txt').read_text() == 'compat_after\n'
            rows[-1]['protected_file_unchanged'] = Path('protected.txt').read_text() == 'do_not_change\n'
            save()
            if not rows[-1]['file_edit_verified'] or not rows[-1]['protected_file_unchanged']:
                raise RuntimeError('压缩后任务执行或约束保留失败')
        if args.case == 'resume' or (args.persistent and not auto):
            value = invoke('restart_resume', recall)
            match = re.search(r'\{[^{}]*\}', value.get('result', ''))
            rows[-1]['recall_exact'] = bool(match) and json.loads(match.group()) == facts
            save()
            if not rows[-1]['recall_exact']:
                raise RuntimeError('重启恢复后随机事实丢失')
    print(json.dumps({'case': args.case, 'client_steps': len(rows),
                      'compact_boundaries': compact, 'evidence': str(output)}))


if __name__ == '__main__':
    main()
