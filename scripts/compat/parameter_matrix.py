#!/usr/bin/env python3
"""给隔离 Claude mock harness 增加 effort 环境变量测试及出站断言。"""
import argparse
import importlib.util
import json
from pathlib import Path
import sys


def main():
    parser = argparse.ArgumentParser(description=__doc__, add_help=False)
    parser.add_argument('--env-json', default='{}')
    parser.add_argument('--expect-effort')
    own, forwarded = parser.parse_known_args()
    extra_env = json.loads(own.env_json)
    allowed = {'CLAUDE_CODE_EFFORT_LEVEL', 'MAX_THINKING_TOKENS'}
    if not isinstance(extra_env, dict) or set(extra_env) - allowed:
        raise ValueError('仅允许测试 effort 和 thinking 的环境变量')
    if any(not isinstance(value, str) for value in extra_env.values()):
        raise ValueError('环境变量值必须是字符串')
    spec = importlib.util.spec_from_file_location('claude_mock', Path(__file__).with_name('claude_mock.py'))
    harness = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(harness)
    original_run = harness.run

    def run(args, env, cwd, stdin=None, timeout=45):
        if '-p' in args:
            env = dict(env, **extra_env)
        return original_run(args, env, cwd, stdin, timeout)

    harness.run = run
    sys.argv = [str(Path(__file__).with_name('claude_mock.py')), *forwarded]
    harness.main()
    output = Path(forwarded[forwarded.index('--output') + 1])
    entries = [json.loads(line) for line in (output / 'requests.jsonl').read_text().splitlines()]
    inbound = [entry['body'] for entry in entries if entry['stage'] == 'claude-to-cpa' and entry.get('body')]
    outbound = [entry['body'] for entry in entries if entry['stage'] == 'cpa-to-responses' and entry.get('body')]
    result = {'environment_overrides': extra_env,
              'inbound_thinking': [body.get('thinking') for body in inbound],
              'inbound_effort': [(body.get('output_config') or {}).get('effort') for body in inbound],
              'outbound_effort': [(body.get('reasoning') or {}).get('effort') for body in outbound]}
    (output / 'parameter-result.json').write_text(json.dumps(result, ensure_ascii=False, indent=2) + '\n')
    if own.expect_effort:
        assert result['outbound_effort'] and all(value == own.expect_effort for value in result['outbound_effort']), result
    print(json.dumps(result, ensure_ascii=False))


if __name__ == '__main__':
    main()
