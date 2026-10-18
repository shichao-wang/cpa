package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	cliproxy "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy"
	"github.com/shichao-wang/cpa/internal/config"
)

func TestServerToolFiltering(t *testing.T) {
	for _, tc := range []struct {
		name, capability, choice, wantChoice string
		keepClient, wantSearch               bool
	}{
		{"unknown", `{}`, `{"type":"any"}`, `{"type":"none"}`, false, false},
		{"unsupported", `{"web_search":false}`, `{"type":"tool","name":"search"}`, `{"type":"none"}`, false, false},
		{"supported", `{"web_search":true}`, `{"type":"tool","name":"search"}`, `{"type":"tool","name":"search"}`, false, true},
		{"removed-choice", `{}`, `{"type":"tool","name":"fetch","disable_parallel_tool_use":true}`, `{"type":"auto","disable_parallel_tool_use":true}`, true, false},
		{"client-choice", `{}`, `{"type":"tool","name":"Bash","disable_parallel_tool_use":true}`, `{"type":"tool","name":"Bash","disable_parallel_tool_use":true}`, true, false},
		{"any-with-client", `{}`, `{"type":"any"}`, `{"type":"any"}`, true, false},
		{"none", `{}`, `{"type":"none"}`, `{"type":"none"}`, true, false},
		{"no-choice", `{}`, `null`, `{"type":"none"}`, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			route := modelRoute{protocol: config.Responses, authID: "server-tools-" + tc.name, sdkModel: "isolated/model"}
			var info cliproxy.ModelInfo
			if err := json.Unmarshal([]byte(`{"id":"isolated/model","native_capabilities":`+tc.capability+`}`), &info); err != nil {
				t.Fatal(err)
			}
			registry := cliproxy.GlobalModelRegistry()
			registry.RegisterClient(route.authID, "codex", []*cliproxy.ModelInfo{&info})
			t.Cleanup(func() { registry.UnregisterClient(route.authID) })
			// 另一连接的同名模型明确支持搜索，也不能改变当前路由的判定。
			var other cliproxy.ModelInfo
			_ = json.Unmarshal([]byte(`{"id":"isolated/model","native_capabilities":{"web_search":true}}`), &other)
			registry.RegisterClient(route.authID+"-other", "codex", []*cliproxy.ModelInfo{&other})
			t.Cleanup(func() { registry.UnregisterClient(route.authID + "-other") })
			tools := []any{
				map[string]any{"type": "web_search_20250305", "name": "search"},
				map[string]any{"type": "web_fetch_20250910", "name": "fetch"},
				map[string]any{"type": "code_execution_20250825", "name": "secret\r\nname"},
			}
			if tc.keepClient {
				for _, name := range []string{"Bash", "WebFetch", "web_fetch", "code_execution", "mcp__local__search"} {
					tools = append(tools, map[string]any{"name": name, "input_schema": map[string]any{"type": "object"}})
				}
			}
			root := map[string]any{"model": "public", "tools": tools, "messages": []any{map[string]any{"role": "user", "content": "web_fetch code_execution"}}}
			if tc.choice != "null" {
				root["tool_choice"] = json.RawMessage(tc.choice)
			}
			data, _ := json.Marshal(root)
			headers := http.Header{"X-Cpa-Compatibility-Warnings": []string{"existing-warning"}}
			filtered, err := filterUnsupportedServerTools(data, route, headers)
			if err != nil {
				t.Fatal(err)
			}
			var actual map[string]json.RawMessage
			_ = json.Unmarshal(filtered, &actual)
			var gotTools []map[string]any
			_ = json.Unmarshal(actual["tools"], &gotTools)
			wantCount := 0
			if tc.keepClient {
				wantCount += 5
			}
			if tc.wantSearch {
				wantCount++
			}
			if len(gotTools) != wantCount {
				t.Fatalf("工具清单错误：%s", filtered)
			}
			var choice, wantChoice any
			_ = json.Unmarshal(actual["tool_choice"], &choice)
			_ = json.Unmarshal([]byte(tc.wantChoice), &wantChoice)
			if !reflect.DeepEqual(choice, wantChoice) {
				t.Fatalf("tool_choice=%s", actual["tool_choice"])
			}
			original, _ := parameterObject(data)
			if !bytes.Equal(original["messages"], actual["messages"]) {
				t.Fatal("历史消息被修改")
			}
			if !strings.Contains(headers.Get("X-CPA-Compatibility-Warnings"), "existing-warning,unsupported-server-tools-disabled") || strings.Contains(headers.Get("X-CPA-Disabled-Server-Tools"), "secret") {
				t.Fatalf("响应提示错误：%v", headers)
			}
		})
	}
}

func TestServerToolFilteringPreservesOtherProtocolsAndNoOp(t *testing.T) {
	data := []byte(`{ "model":"public", "tools":[{"type":"web_fetch_20250910","name":"fetch"}] }`)
	got, err := filterUnsupportedServerTools(data, modelRoute{protocol: config.Anthropic}, http.Header{})
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("原生协议请求被修改：%s %v", got, err)
	}
	data = []byte(`{ "model":"public", "tools":[{"name":"web_fetch","input_schema":{"type":"object"}}] }`)
	got, err = filterUnsupportedServerTools(data, modelRoute{protocol: config.Responses}, http.Header{})
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("客户端工具请求被修改：%s %v", got, err)
	}
	for _, family := range []string{"web_search", "web_fetch", "code_execution"} {
		data := []byte(`{"tools":[{"type":"` + family + `_20250305","name":"tool"}],"tool_choice":{"type":"any"}}`)
		headers := http.Header{}
		got, err := filterUnsupportedServerTools(data, modelRoute{protocol: config.Chat}, headers)
		if err != nil || headers.Get("X-CPA-Disabled-Server-Tools") != family || !bytes.Contains(got, []byte(`"tools":[]`)) {
			t.Fatalf("Chat 路径未关闭服务端工具：%s %v", got, err)
		}
	}
}

func TestServerToolFilteringRejectsAmbiguousDeclarations(t *testing.T) {
	for _, data := range []string{
		`{"tools":{}}`, `{"tools":[null]}`, `{"tools":[{"type":"web_fetch","type":"custom"}]}`,
		`{"tools":[{"type":"web_fetch"}],"tool_choice":{"type":"tool","type":"auto"}}`,
	} {
		if _, err := filterUnsupportedServerTools([]byte(data), modelRoute{protocol: config.Responses}, http.Header{}); err == nil {
			t.Fatalf("歧义声明被接受：%s", data)
		}
	}
}

func TestDisabledServerToolsReachUpstreamWithoutForcedChoice(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "json", true: "stream"}[stream], func(t *testing.T) {
			observed := make(chan map[string]any, 1)
			endpoint, client := compatGateway(t, func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				_ = json.NewDecoder(r.Body).Decode(&body)
				observed <- body
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					io.WriteString(w, "data: {\"type\":\"response.content_part.added\",\"output_index\":0,\"content_index\":0,\"part\":{\"type\":\"output_text\",\"text\":\"\"}}\n\ndata: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":0,\"delta\":\"完成\"}\n\n")
				}
				compatCompleted(w, compatText())
			})
			payload := compatPayload(stream)
			payload["tools"] = []any{map[string]any{"type": "code_execution_20250825", "name": "code_execution"}}
			payload["tool_choice"] = map[string]any{"type": "tool", "name": "code_execution"}
			response := compatRequest(t, client, endpoint, payload, context.Background())
			body, _ := io.ReadAll(response.Body)
			response.Body.Close()
			if response.StatusCode != 200 || response.Header.Get("X-CPA-Disabled-Server-Tools") != "code_execution" || !strings.Contains(string(body), "完成") {
				t.Fatalf("请求失败：%d %s %v", response.StatusCode, body, response.Header)
			}
			upstream := <-observed
			if upstream["tool_choice"] != "none" {
				t.Fatalf("上游仍强制调用工具：%v", upstream)
			}
			for _, tool := range upstream["tools"].([]any) {
				if tool.(map[string]any)["name"] == "code_execution" {
					t.Fatal("已关闭的工具到达上游")
				}
			}
		})
	}
}
