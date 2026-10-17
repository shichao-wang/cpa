#!/usr/bin/env python3
"""真实 Claude Code 的 Plan→resume 实施闭环；模型响应全部来自本地 mock。"""
import importlib.util
import json
from pathlib import Path
import sys
import uuid

spec = importlib.util.spec_from_file_location('compat', Path(__file__).with_name('claude_mock.py'))
c = importlib.util.module_from_spec(spec)
spec.loader.exec_module(c)
original_run = c.run
fixture = {'work': None, 'phase': 'plan'}
plan_text = 'CPA_COMPAT_PLAN: Change compat_before to compat_after in demo.txt, then verify it with cat demo.txt.'


def run(args, env, cwd, stdin=None, timeout=45):
    if '-p' not in args:
        return original_run(args, env, cwd, stdin, timeout)
    work = Path(cwd)
    fixture['work'] = work
    demo = work / 'demo.txt'
    demo.write_text('compat_before\n')
    output = Path(sys.argv[sys.argv.index('--output') + 1])
    base = list(args)
    base.remove('--no-session-persistence')
    session_id = str(uuid.uuid4())
    plan = list(base)
    plan[plan.index('--tools') + 1] = 'Read'
    plan[plan.index('-p') + 1] = 'Read demo.txt and propose a short plan to change compat_before to compat_after, then verify it. Do not modify any file. Return the plan only.'
    plan += ['--session-id', session_id, '--permission-mode', 'plan', '--allowedTools', 'Read', '--max-turns', '3']
    before = {str(path.relative_to(work)): path.read_bytes() for path in work.rglob('*') if path.is_file()}
    plan_result = original_run(plan, env, cwd, stdin, timeout)
    (output / 'claude-plan-result.json').write_text(plan_result)
    parsed_plan = json.loads(plan_result)
    assert parsed_plan.get('result', '').strip() == plan_text, parsed_plan
    after = {str(path.relative_to(work)): path.read_bytes() for path in work.rglob('*') if path.is_file()}
    assert after == before, 'Plan 阶段修改了合成工作目录'
    fixture['plan_no_write'] = True
    fixture['phase'] = 'implement'
    implement = list(base)
    implement[implement.index('--tools') + 1] = 'Read,Edit,Bash'
    implement[implement.index('-p') + 1] = 'I approve the previous plan. Implement it now using Edit and verify with Bash. Then reply exactly CPA_COMPAT_OK.'
    implement += ['--resume', session_id, '--permission-mode', 'acceptEdits', '--allowedTools', 'Read,Edit,Bash', '--max-turns', '4']
    result = original_run(implement, env, cwd, stdin, timeout)
    (output / 'claude-plan-implementation-result.json').write_text(result)
    assert demo.read_text() == 'compat_after\n', '实施阶段未正确修改文件'
    fixture['file_after'] = demo.read_text()
    return result


c.run = run
original_handler = c.handler


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
                name, arguments = 'Read', {'file_path': str(fixture['work'] / 'demo.txt')}
            elif n == 3:
                name, arguments = 'Edit', {'file_path': str(fixture['work'] / 'demo.txt'), 'old_string': 'compat_before', 'new_string': 'compat_after'}
            elif n == 4:
                name, arguments = 'Bash', {'command': 'cat demo.txt', 'description': '验证合成文件的修改结果'}
            else:
                name, arguments = None, None
            if name:
                item = {'id': f'fc_plan_{n}', 'type': 'function_call', 'call_id': f'call_plan_{n}', 'name': name, 'arguments': json.dumps(arguments)}
                events = [('response.output_item.added', {'type': 'response.output_item.added', 'output_index': 0, 'item': dict(item, arguments='')}),
                          ('response.function_call_arguments.delta', {'type': 'response.function_call_arguments.delta', 'output_index': 0, 'item_id': item['id'], 'delta': item['arguments']}),
                          ('response.function_call_arguments.done', {'type': 'response.function_call_arguments.done', 'output_index': 0, 'item_id': item['id'], 'arguments': item['arguments']}),
                          ('response.output_item.done', {'type': 'response.output_item.done', 'output_index': 0, 'item': item})]
            else:
                text = plan_text if n == 2 else 'CPA_COMPAT_OK'
                item = {'id': f'msg_plan_{n}', 'type': 'message', 'role': 'assistant', 'status': 'completed', 'content': [{'type': 'output_text', 'text': text, 'annotations': []}]}
                events = [('response.output_item.added', {'type': 'response.output_item.added', 'output_index': 0, 'item': dict(item, content=[])}),
                          ('response.content_part.added', {'type': 'response.content_part.added', 'output_index': 0, 'content_index': 0, 'part': {'type': 'output_text', 'text': ''}}),
                          ('response.output_text.delta', {'type': 'response.output_text.delta', 'output_index': 0, 'content_index': 0, 'delta': text})]
            response = {'id': f'resp_plan_{n}', 'object': 'response', 'status': 'completed', 'model': payload['model'], 'output': [item], 'usage': {'input_tokens': 20, 'output_tokens': 5, 'total_tokens': 25}}
            events.insert(0, ('response.created', {'type': 'response.created', 'response': dict(response, status='in_progress', output=[])}))
            events.append(('response.completed', {'type': 'response.completed', 'response': response}))
            self.send_response(200)
            self.send_header('content-type', 'text/event-stream')
            self.send_header('connection', 'close')
            self.end_headers()
            for event, data in events:
                self.wfile.write(f'event: {event}\ndata: {json.dumps(data)}\n\n'.encode())
                self.wfile.flush()
            self.close_connection = True
    return Handler


c.handler = handler
c.main()
output = Path(sys.argv[sys.argv.index('--output') + 1])
entries = [json.loads(line) for line in (output / 'requests.jsonl').read_text().splitlines()]
rounds = [entry['body'] for entry in entries if entry['stage'] == 'cpa-to-responses']
assert len(rounds) == 5, f'期望5轮，实际{len(rounds)}'
assert plan_text in json.dumps(rounds[2], ensure_ascii=False), 'resume 请求丢失计划历史'
results = {}
for payload in rounds:
    for item in payload.get('input', []):
        if item.get('type') == 'function_call_output':
            results[item['call_id']] = item.get('output', '')
assert 'compat_before' in results['call_plan_1'], 'Read 结果未返回'
assert 'updated successfully' in results['call_plan_3'], 'Edit 结果未返回'
assert results['call_plan_4'].strip() == 'compat_after', 'Bash 验证失败'
summary = {'rounds': len(rounds), 'plan_permission_mode': 'plan', 'plan_allowed_tools': ['Read'],
           'plan_no_worktree_write': fixture['plan_no_write'], 'resume_plan_history_verified': True,
           'implementation_permission_mode': 'acceptEdits', 'implementation_allowed_tools': ['Read', 'Edit', 'Bash'],
           'edit_result_verified': True, 'bash_result_verified': True, 'final_file': fixture['file_after'],
           'real_claude_processes': 2, 'model_responses': 'loopback-mock', 'real_paid_calls': False}
(output / 'plan-summary.json').write_text(json.dumps(summary, ensure_ascii=False, indent=2) + '\n')
print(json.dumps(summary, ensure_ascii=False))
