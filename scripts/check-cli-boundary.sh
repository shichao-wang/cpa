#!/bin/sh
# 在临时 HOME 中检查已移除的客户端接入入口，不读取真实配置。
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT HUP INT TERM
cd "$root"
go build -o "$scratch/cpa" ./cmd/cpa
export HOME="$scratch/home"
export XDG_CONFIG_HOME="$scratch/config"
mkdir -p "$HOME/.claude"
printf '%s\n' '{"unrelated":true}' > "$scratch/original.json"
cp "$scratch/original.json" "$HOME/.claude/settings.json"
"$scratch/cpa" profile --help > "$scratch/help"
for command in apply restore; do
    if "$scratch/cpa" profile "$command" > "$scratch/output" 2>&1; then
        echo "已移除的命令仍可执行：profile $command" >&2
        exit 1
    fi
    case "$(cat "$scratch/output")" in
        *'unknown command'*) ;;
        *) echo "命令未在解析阶段被拒绝：$command" >&2; exit 1 ;;
    esac
done
if "$scratch/cpa" profile create demo --agent claude-code > "$scratch/output" 2>&1; then
    echo '已移除的 --agent 仍可使用' >&2
    exit 1
fi
case "$(cat "$scratch/output")" in
    *'unknown flag: --agent'*) ;;
    *) echo '--agent 未在解析阶段被拒绝' >&2; exit 1 ;;
esac
test ! -e "$XDG_CONFIG_HOME/cpa"
cmp "$scratch/original.json" "$HOME/.claude/settings.json"
echo '客户端接入命令已移除，拒绝命令未创建状态或改写客户端配置。'
