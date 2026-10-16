#!/bin/sh
# 使用临时状态和 mock 上游复验页面资源、认证边界、配置和隧道访问。
set -eu
cd "$(dirname "$0")/.."
npm --prefix frontend ci
npm --prefix frontend run build
npm --prefix frontend test
go test ./internal/webui -count=1
go test ./internal/integration -run '^TestWebUIThroughTunnel$' -count=1
