"""不启动客户端或模型的会话观测回归。"""
import json
import os
from types import SimpleNamespace
from unittest.mock import patch
from pathlib import Path
import tempfile
import threading
import unittest

import claude_long
from claude_long import read_session_records


class SessionObservationTest(unittest.TestCase):
    def test_observe_while_client_changes_lock_directories(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            project = root / 'projects' / 'test-project'
            project.mkdir(parents=True)
            session = project / 'session.jsonl'
            record = {'type': 'assistant', 'message': {'content': [{'type': 'text', 'text': 'READY'}]}}
            session.write_text(json.dumps(record) + '\n{"type":')
            # 其他会话不能作为当前轮次的通过证据。
            (project / 'other.jsonl').write_text('{"type":"wrong-session"}\n')
            def change_locks():
                for _ in range(500):
                    lock = root / 'history.jsonl.lock'
                    lock.mkdir()
                    lock.rmdir()
            worker = threading.Thread(target=change_locks)
            worker.start()
            try:
                for _ in range(100):
                    self.assertEqual(read_session_records(root, 'session'), [record])
            finally:
                worker.join()
            session.write_text(json.dumps(record) + '\n{"type":"complete"}\n')
            self.assertEqual(read_session_records(root, 'session')[-1], {'type': 'complete'})


class TerminalPrivacyTest(unittest.TestCase):
    def test_timeout_never_persists_confirmation_key_fragments(self):
        for mock in (False, True):
            with self.subTest(mock=mock), tempfile.TemporaryDirectory() as folder:
                root = Path(folder)
                output = root / 'result.json'
                fragment = 'SYNTHETIC_CREDENTIAL_FRAGMENT'
                key = 'prefix-' + fragment + '-suffix'
                descriptors = []
                real_openpty = claude_long.pty.openpty
                real_write = os.write
                def openpty():
                    pair = real_openpty()
                    descriptors.extend(pair)
                    return pair
                def start_process(command, **kwargs):
                    # 模拟真实 TUI 只显示部分 key，然后停在没有结果的会话中。
                    descriptors.append(os.dup(kwargs['stdout']))
                    real_write(kwargs['stdout'], ('Do you want to use this API key? ' + fragment + '\nfor shortcuts\n\x1b[c').encode())
                    return SimpleNamespace(poll=lambda: None)
                controls = bytearray()
                blocked = False
                def write_terminal(fd, data):
                    nonlocal blocked
                    if isinstance(data, bytearray):
                        if not blocked:
                            blocked = True
                            raise BlockingIOError()
                        written = max(1, len(data) // 2)
                        controls.extend(data[:written])
                        return written
                    return len(data)
                args = ['claude_long.py', '--case', 'manual', '--interactive', '--output', str(output)]
                if mock:
                    args.append('--mock')
                previous = Path.cwd()
                try:
                    os.chdir(root)
                    ticks = iter(range(10000))
                    with patch('sys.argv', args), patch.dict(os.environ, {'ANTHROPIC_API_KEY': key, 'CLAUDE_CONFIG_DIR': str(root)}), \
                         patch.object(claude_long.pty, 'openpty', side_effect=openpty), \
                         patch.object(claude_long.subprocess, 'Popen', side_effect=start_process), \
                         patch.object(claude_long.os, 'write', side_effect=write_terminal), \
                         patch.object(claude_long.time, 'sleep'), \
                         patch.object(claude_long.time, 'monotonic', side_effect=lambda: next(ticks)), \
                         patch.object(claude_long.atexit, 'register'):
                        with self.assertRaisesRegex(RuntimeError, '交互终端等待验收结果超时'):
                            claude_long.main()
                    self.assertIn(b'\x1b[?1;2c', controls)
                    self.assertEqual(output.stat().st_mode & 0o777, 0o600)
                    self.assertEqual(list(root.glob('*.terminal*.txt')), [])
                    for artifact in root.rglob('*'):
                        if artifact.is_file():
                            self.assertNotIn(fragment.encode(), artifact.read_bytes())
                finally:
                    os.chdir(previous)
                    for descriptor in descriptors:
                        try:
                            os.close(descriptor)
                        except OSError:
                            pass


if __name__ == '__main__':
    unittest.main()
