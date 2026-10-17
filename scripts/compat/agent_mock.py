#!/usr/bin/env python3
"""仅通过隔离 mock 验证非 bare Claude 的 Agent 调度和子模型路由。"""
import io
import json
from pathlib import Path
import sys

namespace = {'__name__': 'compat_agent_harness'}
source = Path(__file__).with_name('claude_mock.py')
exec(compile(source.read_text(), str(source), 'exec'), namespace)
original_run = namespace['run']
original_handler = namespace['handler']
fixture = None


def run(args, env, cwd, stdin=None, timeout=45):
    global fixture
    if args[0].endswith('claude') and '--bare' in args:
        fixture = Path(cwd) / 'fixture.txt'
        fixture.write_text('CPA_COMPAT_OK\n')
        args = list(args)
        args.remove('--bare')
        args[args.index('--tools') + 1] = 'default'
        args[args.index('-p') + 1] = 'Use checker once to read fixture.txt, then reply exactly its contents.'
        args += ['--agents', '{"checker":{"description":"Reads isolated fixture","prompt":"Read fixture.txt and report its exact contents.","tools":["Read"],"model":"haiku"}}',
                 '--permission-mode', 'dontAsk', '--allowedTools', 'Agent,Read']
        env = dict(env)
        for name in ('HTTP_PROXY', 'HTTPS_PROXY', 'ALL_PROXY', 'http_proxy', 'https_proxy', 'all_proxy'):
            env[name] = 'http://127.0.0.1:1'
        env['NO_PROXY'] = env['no_proxy'] = '127.0.0.1,localhost,::1'
    return original_run(args, env, cwd, stdin, timeout)


def handler(recorder, proxy_port=None):
    base = original_handler(recorder, proxy_port)

    class Handler(base):
        def do_POST(self):
            body = self.rfile.read(int(self.headers.get('content-length', '0')))
            payload = json.loads(body)
            if proxy_port is not None:
                recorder.latest_public_model = payload.get('model')
                self.rfile = io.BytesIO(body)
                return super().do_POST()
            public_model = getattr(recorder, 'latest_public_model', '')
            is_child = 'haiku' in public_model
            has_result = any(item.get('type') == 'function_call_output' for item in payload.get('input', []) if isinstance(item, dict))
            if has_result:
                # 文本响应沿用基线 mock。
                self.rfile = io.BytesIO(body)
                return super().do_POST()
            recorder.write('cpa-to-responses', self, body)
            name = 'Read' if is_child else 'Agent'
            arguments = {'file_path': str(fixture)} if is_child else {
                'description': 'Read test fixture', 'prompt': 'Read fixture.txt and report exact contents.',
                'subagent_type': 'checker', 'run_in_background': False}
            item = {'type': 'function_call', 'id': 'fc_' + name, 'call_id': 'call_' + name,
                    'name': name, 'arguments': json.dumps(arguments), 'status': 'completed'}
            response = {'id': 'resp_' + name, 'object': 'response', 'created_at': 1,
                        'model': payload.get('model'), 'status': 'completed', 'output': [item],
                        'usage': {'input_tokens': 20, 'output_tokens': 10, 'total_tokens': 30}}
            events = [
                ('response.created', {'type': 'response.created', 'response': dict(response, output=[], status='in_progress')}),
                ('response.output_item.added', {'type': 'response.output_item.added', 'output_index': 0,
                 'item': dict(item, arguments='', status='in_progress')}),
                ('response.function_call_arguments.delta', {'type': 'response.function_call_arguments.delta',
                 'item_id': item['id'], 'output_index': 0, 'delta': item['arguments']}),
                ('response.function_call_arguments.done', {'type': 'response.function_call_arguments.done',
                 'item_id': item['id'], 'output_index': 0, 'arguments': item['arguments']}),
                ('response.output_item.done', {'type': 'response.output_item.done', 'output_index': 0, 'item': item}),
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


namespace['run'] = run
namespace['handler'] = handler
namespace['print'] = lambda *_args, **_kwargs: None
if __name__ == '__main__':
    namespace['main']()
    output = Path(sys.argv[sys.argv.index('--output') + 1])
    summary = json.loads((output / 'summary.json').read_text())
    summary['isolation']['bare'] = False
    summary['isolation']['external_http_proxy'] = 'refused-loopback-port'
    summary['isolation']['no_proxy'] = 'loopback-only'
    requests = [json.loads(line) for line in (output / 'requests.jsonl').read_text().splitlines()]
    inbound = [entry['body'] for entry in requests if entry['stage'] == 'claude-to-cpa']
    summary['agent_exposed'] = any(tool['name'] == 'Agent' for request in inbound for tool in request.get('tools', []))
    summary['grep_exposed'] = any(tool['name'] == 'Grep' for request in inbound for tool in request.get('tools', []))
    summary['child_route_observed'] = any('haiku' in request.get('model', '') for request in inbound)
    summary['inbound_models'] = [request.get('model') for request in inbound]
    (output / 'summary.json').write_text(json.dumps(summary, ensure_ascii=False, indent=2) + '\n')
    assert summary['agent_exposed'] and summary['child_route_observed'], '未形成子任务调度及子模型路由证据'
    print(json.dumps({key: summary[key] for key in ('agent_exposed', 'grep_exposed', 'child_route_observed', 'inbound_models')}))
