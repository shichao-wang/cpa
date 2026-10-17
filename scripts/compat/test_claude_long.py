"""不启动客户端或模型的会话观测回归。"""
import json
from pathlib import Path
import tempfile
import threading
import unittest

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


if __name__ == '__main__':
    unittest.main()
