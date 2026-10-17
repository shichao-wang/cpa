#!/usr/bin/env python3
"""在临时配置和 Responses mock 上，用独立 PTY 检查 /model 与重启。"""
import fcntl
import importlib.util
import json
import os
from pathlib import Path
import pty
import re
import select
import signal
import struct
import subprocess
import sys
import termios
import time


def plain(text):
    return re.sub(r'\x1b\[[0-?]*[ -/]*[@-~]', '', text)


def probe(command, env, cwd, output):
    base = list(command)
    for flag in ['--output-format', '-p']:
        index = base.index(flag)
        del base[index:index + 2]
    base.remove('--no-session-persistence')
    results = []
    user_settings = Path(env['CLAUDE_CONFIG_DIR']) / 'settings.json'
    applied_settings = user_settings.read_bytes()
    for session in range(2):
        current = list(base)
        if session:
            index = current.index('--model')
            del current[index:index + 2]
            current[current.index('--setting-sources') + 1] = 'user'
            saved = json.loads(user_settings.read_text())
            saved.setdefault('env', {})['ANTHROPIC_BASE_URL'] = env['ANTHROPIC_BASE_URL']
            user_settings.write_text(json.dumps(saved))
        trace_start = len((output / 'requests.jsonl').read_text().splitlines())
        master, slave = pty.openpty()
        os.set_blocking(master, False)
        fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack('HHHH', 32, 120, 0, 0))
        log = output / f'pty-session-{session + 1}.txt'
        log.write_text('')
        log.chmod(0o600)
        process = subprocess.Popen(current, stdin=slave, stdout=slave, stderr=slave,
                                   env=dict(env, TERM='xterm-256color'), cwd=cwd,
                                   start_new_session=True, close_fds=True)
        os.close(slave)
        deadline = time.monotonic() + 20
        captured = []
        actions = []

        def pump(seconds):
            until = min(deadline, time.monotonic() + seconds)
            while time.monotonic() < until:
                ready, _, _ = select.select([master], [], [], min(0.15, max(0, until-time.monotonic())))
                if ready:
                    try:
                        data = os.read(master, 65536)
                    except (OSError, BlockingIOError):
                        break
                    if not data:
                        break
                    text = data.decode('utf-8', errors='replace')
                    captured.append(text)

        def send(value):
            if time.monotonic() >= deadline or process.poll() is not None:
                return
            os.write(master, value.encode())
            actions.append(value.strip())

        try:
            pump(3)
            answered = set()
            for _ in range(6):
                text = re.sub(r'\s+', '', plain(''.join(captured))[-9000:]).lower()
                choices = [('choosethetextstyle', '\r'),
                           ('doyouwanttousethisapikey', '\x1b[A\r'),
                           ('pressentertocontinue', '\r'),
                           ('doyoutrust', '\x1b[B\r'), ('yes,itrust', '\x1b[B\r')]
                matches = [(key, action) for key, action in choices if key in text and key not in answered]
                if not matches:
                    break
                key, action = matches[0]
                answered.add(key)
                send(action)
                pump(0.8)
            if session == 0:
                send('/model claude-opus-4-6\r')
                pump(2)
            send('Reply exactly CPA_COMPAT_OK. Do not use tools.\r')
            pump(7)
            send('/exit\r')
            pump(1)
        finally:
            if process.poll() is None:
                try:
                    os.killpg(process.pid, signal.SIGTERM)
                except (ProcessLookupError, PermissionError):
                    pass
                try:
                    process.wait(timeout=1)
                except subprocess.TimeoutExpired:
                    try:
                        os.killpg(process.pid, signal.SIGKILL)
                    except (ProcessLookupError, PermissionError):
                        process.kill()
                    process.wait(timeout=2)
            os.close(master)
        # Claude 首次启动可能回显临时 API key 的片段，证据只保留脱敏后的屏显。
        final_screen = plain(''.join(captured)).replace(env.get('ANTHROPIC_API_KEY', ''), '[REDACTED]')
        final_screen = re.sub(r'(ANTHROPIC_API_KEY:)[^\n]*', r'\1[REDACTED]', final_screen)
        log.write_text(final_screen)
        snapshot = {}
        config = Path(env['CLAUDE_CONFIG_DIR'])
        for file in [config / 'settings.json', config / '.claude.json', Path(env['HOME']) / '.claude.json']:
            if file.exists():
                try:
                    data = json.loads(file.read_text())
                    snapshot[file.name] = {key: value for key, value in data.items()
                                           if key.lower() in ['model', 'lastmodel', 'lastselectedmodel']}
                except (ValueError, OSError):
                    pass
        rows = [json.loads(line) for line in (output / 'requests.jsonl').read_text().splitlines()[trace_start:]]
        actual_models = [row['body'].get('model') for row in rows if row['stage'] == 'claude-to-cpa' and row.get('body')]
        statuses = [row.get('status') for row in rows if row['stage'] == 'cpa-to-claude-result']
        results.append({'session': session + 1, 'actual_models': actual_models, 'statuses': statuses, 'process_exit': process.returncode,
                        'actions': actions, 'settings_model_fields': snapshot,
                        'screen_capture': str(log), 'elapsed_limit_seconds': 20})
    user_settings.write_bytes(applied_settings)
    (output / 'pty-result.json').write_text(json.dumps(results, ensure_ascii=False, indent=2) + '\n')


def main():
    if '--output' not in sys.argv:
        raise ValueError('必须提供 --output 临时证据目录')
    output = Path(sys.argv[sys.argv.index('--output') + 1])
    spec = importlib.util.spec_from_file_location('claude_mock', Path(__file__).with_name('claude_mock.py'))
    harness = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(harness)
    original = harness.run

    def run(args, env, cwd, stdin=None, timeout=45):
        if '-p' in args:
            settings = Path(env['CLAUDE_CONFIG_DIR']) / 'settings.json'
            before = settings.read_bytes()
            try:
                probe(args, env, cwd, output)
            finally:
                settings.write_bytes(before)
        return original(args, env, cwd, stdin, timeout)

    harness.run = run
    harness.main()
    entries = [json.loads(line) for line in (output / 'requests.jsonl').read_text().splitlines()]
    requests = [{'model': entry['body'].get('model'), 'path': entry['path']}
                for entry in entries if entry['stage'] == 'claude-to-cpa' and entry.get('body')]
    (output / 'model-requests.json').write_text(json.dumps(requests, indent=2) + '\n')
    sessions = json.loads((output / 'pty-result.json').read_text())
    for session in sessions:
        assert 'claude-opus-4-6' in session['actual_models'], session
        assert session['statuses'] and all(status == 200 for status in session['statuses']), session
        assert session['settings_model_fields'].get('settings.json', {}).get('model') == 'claude-opus-4-6', session
    print(json.dumps({'recorded_models': requests, 'interactive_evidence': str(output / 'pty-result.json'),
                      'interactive_model_and_restart_verified': True}))


if __name__ == '__main__':
    main()
