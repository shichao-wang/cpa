#!/usr/bin/env python3
"""真实 Claude Code 工具闭环，所有模型响应来自逐字节 loopback mock。"""
import importlib.util, json, sys
from pathlib import Path
spec = importlib.util.spec_from_file_location('compat', Path(__file__).with_name('claude_mock.py'))
c = importlib.util.module_from_spec(spec)
spec.loader.exec_module(c)
original_run = c.run
fixture = {'work': None, 'resume_verified': False}

def run(args, env, cwd, stdin=None, timeout=45):
    if '--bare' in args:
        fixture['work'] = Path(cwd)
        (Path(cwd) / 'demo.txt').write_text('compat_before\n')
        args = list(args)
        args[args.index('--tools') + 1] = 'Read,Grep,Edit,Bash'
        args.remove('--no-session-persistence')
        session_id = str(c.uuid.uuid4())
        args += ['--session-id', session_id]
        args += ['--allowedTools', 'Read,Grep,Edit,Bash', '--permission-mode', 'acceptEdits', '--max-turns', '8']
        args[args.index('-p') + 1] = 'Use Read and Grep to inspect demo.txt. Edit compat_before to compat_after. Use Bash to verify with cat demo.txt. Then reply exactly CPA_COMPAT_OK.'
        result = original_run(args, env, cwd, stdin, timeout)
        assert (Path(cwd) / 'demo.txt').read_text() == 'compat_after\n', 'Edit工具未修改文件'
        resumed = list(args)
        pos = resumed.index('--session-id')
        del resumed[pos:pos + 2]
        resumed += ['--resume', session_id]
        resumed[resumed.index('-p') + 1] = 'Resume the prior tool test and reply exactly CPA_COMPAT_OK.'
        resumed_result = original_run(resumed, env, cwd, stdin, timeout)
        assert json.loads(resumed_result).get('result', '').strip() == 'CPA_COMPAT_OK'
        output = Path(sys.argv[sys.argv.index('--output') + 1])
        (output / 'claude-resume-result.json').write_text(resumed_result)
        fixture['resume_verified'] = True
        return result
    return original_run(args, env, cwd, stdin, timeout)
c.run = run
original_handler = c.handler

def handler(recorder, proxy_port=None):
    if proxy_port is not None:
        return original_handler(recorder, proxy_port)

    class H(original_handler(recorder)):
        rounds = 0

        def do_POST(self):
            body = self.rfile.read(int(self.headers.get('content-length', '0')))
            payload = json.loads(body)
            recorder.write('cpa-to-responses', self, body)
            H.rounds += 1
            n = H.rounds
            tools = [('Read', {'file_path': str(fixture['work'] / 'demo.txt')}), ('Grep', {'pattern': 'compat_before', 'path': str(fixture['work'] / 'demo.txt'), 'output_mode': 'content'}), ('Edit', {'file_path': str(fixture['work'] / 'demo.txt'), 'old_string': 'compat_before', 'new_string': 'compat_after'}), ('Bash', {'command': 'cat demo.txt', 'description': '验证临时测试文件'})]
            if n <= 4:
                (name, args) = tools[n - 1]
                item = {'id': f'fc_{n}', 'type': 'function_call', 'call_id': f'call_{n}', 'name': name, 'arguments': json.dumps(args)}
                added = dict(item, arguments='')
                events = [('response.output_item.added', {'type': 'response.output_item.added', 'output_index': 0, 'item': added}), ('response.function_call_arguments.delta', {'type': 'response.function_call_arguments.delta', 'output_index': 0, 'item_id': item['id'], 'delta': item['arguments']}), ('response.function_call_arguments.done', {'type': 'response.function_call_arguments.done', 'output_index': 0, 'item_id': item['id'], 'arguments': item['arguments']}), ('response.output_item.done', {'type': 'response.output_item.done', 'output_index': 0, 'item': item})]
            else:
                item = {'id': 'msg_done', 'type': 'message', 'role': 'assistant', 'status': 'completed', 'content': [{'type': 'output_text', 'text': 'CPA_COMPAT_OK', 'annotations': []}]}
                events = [('response.output_item.added', {'type': 'response.output_item.added', 'output_index': 0, 'item': dict(item, content=[])}), ('response.content_part.added', {'type': 'response.content_part.added', 'output_index': 0, 'content_index': 0, 'part': {'type': 'output_text', 'text': ''}}), ('response.output_text.delta', {'type': 'response.output_text.delta', 'output_index': 0, 'content_index': 0, 'delta': 'CPA_COMPAT_OK'})]
            output_items = [item]
            if n == 1:
                parallel = {'id': 'fc_parallel', 'type': 'function_call', 'call_id': 'call_parallel',
                            'name': 'Bash', 'arguments': json.dumps({'command': 'printf PARALLEL_OK', 'description': '独立并行工具配对测试'})}
                output_items.append(parallel)
                events += [
                    ('response.output_item.added', {'type': 'response.output_item.added', 'output_index': 1, 'item': dict(parallel, arguments='')}),
                    ('response.function_call_arguments.delta', {'type': 'response.function_call_arguments.delta', 'output_index': 1, 'item_id': parallel['id'], 'delta': parallel['arguments']}),
                    ('response.function_call_arguments.done', {'type': 'response.function_call_arguments.done', 'output_index': 1, 'item_id': parallel['id'], 'arguments': parallel['arguments']}),
                    ('response.output_item.done', {'type': 'response.output_item.done', 'output_index': 1, 'item': parallel}),
                ]
            response = {'id': f'resp_{n}', 'object': 'response', 'status': 'completed', 'model': payload['model'], 'output': output_items, 'usage': {'input_tokens': 20, 'output_tokens': 5, 'total_tokens': 25}}
            events.insert(0, ('response.created', {'type': 'response.created', 'response': dict(response, status='in_progress', output=[])}))
            events.append(('response.completed', {'type': 'response.completed', 'response': response}))
            self.send_response(200)
            self.send_header('content-type', 'text/event-stream')
            self.send_header('connection', 'close')
            self.end_headers()
            for (event, data) in events:
                frame = f'event: {event}\ndata: {json.dumps(data)}\n\n'.encode()
                for byte in frame:
                    self.wfile.write(bytes([byte]))
                    self.wfile.flush()
            self.close_connection = True
    return H
c.handler = handler
c.main()
output = Path(sys.argv[sys.argv.index('--output') + 1])
entries = [json.loads(line) for line in (output / 'requests.jsonl').read_text().splitlines()]
inbound = next((x['body'] for x in entries if x['stage'] == 'claude-to-cpa'))
rounds = [x for x in entries if x['stage'] == 'cpa-to-responses']
results = {}
for record in rounds:
    for item in record['body'].get('input', []):
        if item.get('type') == 'function_call_output':
            results[item['call_id']] = item.get('output', '')
assert 'compat_before' in results['call_1']
assert results['call_parallel'].strip() == 'PARALLEL_OK'
assert any({'call_1', 'call_parallel'} <= {x.get('call_id') for x in record['body'].get('input', []) if x.get('type') == 'function_call_output'} for record in rounds)
assert 'updated successfully' in results['call_3']
assert results['call_4'].strip() == 'compat_after'
summary = {'available_tools': [t['name'] for t in inbound.get('tools', [])], 'rounds': len(rounds), 'parallel_result_pairs_verified': True, 'read_verified': True, 'edit_file_verified': True, 'bash_verified': True, 'grep_result': results.get('call_2'), 'stream_fragment_bytes': 1, 'resume_verified': fixture['resume_verified'], 'real_credentials': False, 'real_paid_calls': False}
(output / 'tools-summary.json').write_text(json.dumps(summary, ensure_ascii=False, indent=2) + '\n')
print(json.dumps(summary, ensure_ascii=False))
