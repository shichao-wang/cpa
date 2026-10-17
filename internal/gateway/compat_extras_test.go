package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
)

// 工具type与beta名称在编写前核对Anthropic公开文档，来源见docs/compat-extras.md。
func TestCompatAnthropicServerToolBoundaries(t *testing.T) {
	type observation struct {
		Tools         []map[string]any
		Beta, Version string
	}
	for _, tc := range []struct {
		name         string
		tool         map[string]any
		expectedType string
	}{
		{"web_search", map[string]any{"type": "web_search_20250305", "name": "web_search", "max_uses": 2, "allowed_domains": []string{"example.com"}}, "web_search"},
		{"web_fetch", map[string]any{"type": "web_fetch_20250910", "name": "web_fetch", "max_uses": 2, "allowed_domains": []string{"example.com"}, "citations": map[string]any{"enabled": true}}, "function"},
		{"code_execution", map[string]any{"type": "code_execution_20250825", "name": "code_execution"}, "function"},
		{"eager_input_streaming", map[string]any{"name": "make_file", "description": "兼容性探针", "input_schema": map[string]any{"type": "object", "properties": map[string]any{"content": map[string]any{"type": "string"}}}, "eager_input_streaming": true}, "function"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			observed := make(chan observation, 1)
			endpoint, client := compatGateway(t, func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Tools []map[string]any `json:"tools"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				observed <- observation{body.Tools, r.Header.Get("Anthropic-Beta"), r.Header.Get("Anthropic-Version")}
				compatCompleted(w, compatText())
			})
			payload := compatPayload(false)
			payload["tools"] = []any{tc.tool}
			data, _ := json.Marshal(payload)
			req, err := http.NewRequestWithContext(context.Background(), "POST", endpoint, bytes.NewReader(data))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("X-Api-Key", "alpha-client-secret")
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Anthropic-Version", "2023-06-01")
			req.Header.Set("Anthropic-Beta", "fine-grained-tool-streaming-2025-05-14,code-execution-2025-05-22")
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != 200 || !json.Valid(body) {
				t.Fatalf("代表性工具请求未到达mock：%d %s", resp.StatusCode, body)
			}
			actual := <-observed
			var matching map[string]any
			for _, tool := range actual.Tools {
				if tc.name == "web_search" && tool["type"] == "web_search" || tool["name"] == tc.tool["name"] {
					matching = tool
					break
				}
			}
			if matching == nil || matching["type"] != tc.expectedType {
				t.Fatalf("工具边界改变：case=%s tools=%v", tc.name, actual.Tools)
			}
			if tc.name == "web_search" {
				filters, ok := matching["filters"].(map[string]any)
				if !ok {
					t.Fatalf("搜索域名过滤未映射：%v", matching)
				}
				domains, ok := filters["allowed_domains"].([]any)
				if !ok || len(domains) != 1 || domains[0] != "example.com" {
					t.Fatalf("搜索域名过滤改变：%v", matching)
				}
			}
			if actual.Beta != "" || actual.Version != "" {
				t.Fatalf("Anthropic专用header意外传入Responses：beta=%q version=%q", actual.Beta, actual.Version)
			}
			encoded, _ := json.Marshal(actual.Tools)
			t.Logf("case=%s inbound_type=%v outbound_tools=%s anthropic_beta=%q anthropic_version=%q；HTTP200只代表mock接受，不证明原生server tool执行", tc.name, tc.tool["type"], encoded, actual.Beta, actual.Version)
		})
	}
}

func TestCompatCachedUsageMapping(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, cache := range []struct{ read, write int }{{0, 0}, {40, 0}, {40, 20}} {
			t.Run(fmt.Sprintf("stream-%t-read-%d-write-%d", stream, cache.read, cache.write), func(t *testing.T) {
				endpoint, client := compatGateway(t, func(w http.ResponseWriter, r *http.Request) {
					io.Copy(io.Discard, r.Body)
					r.Body.Close()
					w.Header().Set("Content-Type", "text/event-stream")
					response := map[string]any{"id": "resp_cached", "object": "response", "status": "completed", "model": "responses-target", "output": compatText(), "usage": map[string]any{"input_tokens": 100, "output_tokens": 7, "total_tokens": 107, "input_tokens_details": map[string]any{"cached_tokens": cache.read, "cache_write_tokens": cache.write}}}
					created, _ := json.Marshal(map[string]any{"type": "response.created", "response": map[string]any{"id": "resp_cached", "model": "responses-target", "output": []any{}}})
					completed, _ := json.Marshal(map[string]any{"type": "response.completed", "response": response})
					fmt.Fprintf(w, "data: %s\n\ndata: %s\n\n", created, completed)
				})
				resp := compatRequest(t, client, endpoint, compatPayload(stream), context.Background())
				body, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				if resp.StatusCode != 200 {
					t.Fatalf("缓存usage请求失败：%d %s", resp.StatusCode, body)
				}
				var usage map[string]any
				if !stream {
					var result map[string]any
					if err := json.Unmarshal(body, &result); err != nil {
						t.Fatal(err)
					}
					usage, _ = result["usage"].(map[string]any)
				} else {
					for _, line := range bytes.Split(body, []byte("\n")) {
						if !bytes.HasPrefix(line, []byte("data: ")) {
							continue
						}
						var event map[string]any
						if json.Unmarshal(bytes.TrimPrefix(line, []byte("data: ")), &event) == nil && event["type"] == "message_delta" {
							usage, _ = event["usage"].(map[string]any)
						}
					}
				}
				if usage == nil {
					t.Fatalf("缺少usage：%s", body)
				}
				number := func(key string) int { n, _ := usage[key].(float64); return int(n) }
				if number("input_tokens") != 100-cache.read-cache.write || number("output_tokens") != 7 || number("cache_read_input_tokens") != cache.read || number("cache_creation_input_tokens") != cache.write {
					t.Fatalf("缓存计数映射错误：%v", usage)
				}
				t.Logf("Responses input=100 cached=%d cache_write=%d → Messages usage=%v", cache.read, cache.write, usage)
			})
		}
	}
}
