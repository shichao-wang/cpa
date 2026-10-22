package gateway

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shichao-wang/cpa/internal/config"
	"github.com/shichao-wang/cpa/internal/store"
)

// 只使用临时状态、公开 Messages 入口和 HTTP mock，不读取运行中的配置或凭证。
func compatGateway(t *testing.T, handler http.HandlerFunc) (string, *http.Client) {
	t.Helper()
	upstream := httptest.NewServer(handler)
	t.Cleanup(upstream.Close)
	address, err := reserveLoopbackAddress(t)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.New(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	state := testState(address, upstream.URL, upstream.URL, upstream.URL, upstream.URL)
	if err = st.Update(func(s *config.State) error { *s = *state; return nil }); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, st, "compat-tools-stream") }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("兼容性测试网关未退出")
		}
	})
	waitForReady(t, st)
	client := &http.Client{Timeout: 8 * time.Second, Transport: &http.Transport{Proxy: nil}}
	t.Cleanup(client.CloseIdleConnections)
	return "http://" + address + "/v1/messages", client
}
func compatRequest(t *testing.T, client *http.Client, endpoint string, payload any, ctx context.Context) *http.Response {
	t.Helper()
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Api-Key", "alpha-client-secret")
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}
func compatPayload(stream bool) map[string]any {
	return map[string]any{"model": "claude-haiku-test", "max_tokens": 1024, "stream": stream, "messages": []any{map[string]any{"role": "user", "content": "兼容性测试"}}}
}
func compatCompleted(w http.ResponseWriter, output []any) {
	w.Header().Set("Content-Type", "text/event-stream")
	event, _ := json.Marshal(map[string]any{"type": "response.completed", "response": map[string]any{"id": "resp_compat", "object": "response", "status": "completed", "model": "responses-target", "output": output, "usage": map[string]any{"input_tokens": 7, "output_tokens": 9, "total_tokens": 16}}})
	fmt.Fprintf(w, "data: %s\n\n", event)
}
func compatText() []any {
	return []any{map[string]any{"id": "msg_done", "type": "message", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "完成", "annotations": []any{}}}}}
}

func TestCompatParallelToolsAndResults(t *testing.T) {
	observed := make(chan map[string]any, 4)
	var calls atomic.Int32
	names := []string{"Read", "Grep", "Edit", "Bash"}
	args := []string{`{"file_path":"demo.txt","offset":1}`, `{"pattern":"中文","path":"."}`, `{"file_path":"demo.txt","old_string":"旧","new_string":"新"}`, `{"command":"printf 测试","timeout":1000}`}
	endpoint, client := compatGateway(t, func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		observed <- b
		if calls.Add(1) > 1 {
			compatCompleted(w, compatText())
			return
		}
		output := []any{}
		for i, name := range names {
			output = append(output, map[string]any{"type": "function_call", "id": fmt.Sprint("fc_", i), "call_id": fmt.Sprint("call_", i), "name": name, "arguments": args[i]})
		}
		compatCompleted(w, output)
	})
	payload := compatPayload(false)
	tools := []any{}
	for _, name := range names {
		tools = append(tools, map[string]any{"name": name, "description": "测试工具", "input_schema": map[string]any{"type": "object", "additionalProperties": true}})
	}
	payload["tools"] = tools
	resp := compatRequest(t, client, endpoint, payload, context.Background())
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var result struct {
		Content    []map[string]any `json:"content"`
		StopReason string           `json:"stop_reason"`
	}
	if resp.StatusCode != 200 || json.Unmarshal(body, &result) != nil {
		t.Fatalf("工具响应失败：%d %s", resp.StatusCode, body)
	}
	if len(result.Content) != 4 || result.StopReason != "tool_use" {
		t.Fatalf("并行工具未完整输出：%s", body)
	}
	results := []any{}
	for i, block := range result.Content {
		var expected any
		_ = json.Unmarshal([]byte(args[i]), &expected)
		got, _ := json.Marshal(block["input"])
		want, _ := json.Marshal(expected)
		if block["name"] != names[i] || block["id"] != fmt.Sprint("call_", i) || !bytes.Equal(got, want) {
			t.Fatalf("工具ID/名称/参数改变：%s", body)
		}
		results = append(results, map[string]any{"type": "tool_result", "tool_use_id": block["id"], "content": "结果中文 " + names[i]})
	}
	first := <-observed
	defined := map[string]bool{}
	for _, tool := range first["tools"].([]any) {
		m := tool.(map[string]any)
		if m["type"] == "function" {
			defined[m["name"].(string)] = true
		}
	}
	for _, name := range names {
		if !defined[name] {
			t.Fatalf("出站工具定义丢失：%s", name)
		}
	}
	payload["messages"] = []any{map[string]any{"role": "user", "content": "兼容性测试"}, map[string]any{"role": "assistant", "content": result.Content}, map[string]any{"role": "user", "content": results}}
	resp = compatRequest(t, client, endpoint, payload, context.Background())
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !bytes.Contains(body, []byte("完成")) {
		t.Fatalf("工具结果续轮失败：%d %s", resp.StatusCode, body)
	}
	second := <-observed
	input := second["input"].([]any)
	found := map[string]string{}
	for _, item := range input {
		m := item.(map[string]any)
		if m["type"] == "function_call_output" {
			found[m["call_id"].(string)] = m["output"].(string)
		}
	}
	for i, name := range names {
		if found[fmt.Sprint("call_", i)] != "结果中文 "+name {
			t.Fatalf("出站结果配对失败：%v", found)
		}
	}
	t.Log("四个并行工具的参数/调用ID和第二轮结果正确映射；SDK未执行本机工具")
}

func TestCompatFragmentedStreamCancellation(t *testing.T) {
	canceled := make(chan struct{}, 1)
	var calls atomic.Int32
	endpoint, client := compatGateway(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) > 1 {
			compatCompleted(w, compatText())
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		events := []string{
			`{"type":"response.created","response":{"id":"resp_stream","model":"responses-target","output":[]}}`,
			`{"type":"response.reasoning_summary_part.added","output_index":0,"summary_index":0,"part":{"type":"summary_text","text":""}}`,
			`{"type":"response.reasoning_summary_text.delta","output_index":0,"summary_index":0,"delta":"推理摘要中文"}`,
			`{"type":"response.reasoning_summary_part.done","output_index":0,"summary_index":0,"part":{"type":"summary_text","text":"推理摘要中文"}}`,
			`{"type":"response.output_item.added","output_index":1,"item":{"id":"fc_stream","type":"function_call","call_id":"call_stream","name":"Read","arguments":""}}`,
			`{"type":"response.function_call_arguments.delta","output_index":1,"item_id":"fc_stream","delta":"{\"file_path\":\"中文.txt\"}"}`,
			`{"type":"response.function_call_arguments.done","output_index":1,"item_id":"fc_stream","arguments":"{\"file_path\":\"中文.txt\"}"}`,
			`{"type":"response.output_item.done","output_index":1,"item":{"id":"fc_stream","type":"function_call","call_id":"call_stream","name":"Read","arguments":"{\"file_path\":\"中文.txt\"}"}}`,
			`{"type":"response.output_item.added","output_index":2,"item":{"id":"msg_stream","type":"message","role":"assistant","content":[]}}`,
			`{"type":"response.content_part.added","output_index":2,"content_index":0,"part":{"type":"output_text","text":""}}`,
			`{"type":"response.output_text.delta","output_index":2,"content_index":0,"delta":"你好🌍"}`,
		}
		for _, event := range events {
			for _, b := range []byte("data: " + event + "\n\n") {
				if _, err := w.Write([]byte{b}); err != nil {
					return
				}
				flusher.Flush()
			}
		}
		<-r.Context().Done()
		canceled <- struct{}{}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resp := compatRequest(t, client, endpoint, compatPayload(true), ctx)
	scanner := bufio.NewScanner(resp.Body)
	var text, thinking, arguments string
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var m struct {
			Type  string `json:"type"`
			Delta struct {
				Type, Text, Thinking string
				PartialJSON          string `json:"partial_json"`
			} `json:"delta"`
		}
		if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &m) != nil {
			continue
		}
		text += m.Delta.Text
		thinking += m.Delta.Thinking
		arguments += m.Delta.PartialJSON
		if strings.Contains(text, "你好🌍") {
			break
		}
	}
	if text != "你好🌍" || thinking != "推理摘要中文" || arguments != `{"file_path":"中文.txt"}` {
		t.Fatalf("分片/Unicode/摘要转换错误：text=%q thinking=%q arguments=%q scan=%v", text, thinking, arguments, scanner.Err())
	}
	cancel()
	resp.Body.Close()
	select {
	case <-canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("客户端取消未传播到上游")
	}
	next := compatRequest(t, client, endpoint, compatPayload(false), context.Background())
	body, _ := io.ReadAll(next.Body)
	next.Body.Close()
	if next.StatusCode != 200 || !bytes.Contains(body, []byte("完成")) {
		t.Fatalf("取消后新请求失败：%d %s", next.StatusCode, body)
	}
	t.Log("逐字节HTTP flush跨UTF-8分片正确；未等上游结束即收到文本；取消传播且下一请求成功")
}

func TestCompatUpstreamErrorBounded(t *testing.T) {
	for _, status := range []int{400, 401, 429, 500, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls atomic.Int32
			endpoint, client := compatGateway(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				io.WriteString(w, `{"error":{"message":"mock上游拒绝","type":"compat_error","code":"compat_error"}}`)
			})
			start := time.Now()
			resp := compatRequest(t, client, endpoint, compatPayload(false), context.Background())
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode < 400 {
				t.Fatalf("上游错误变成成功：%d %s", resp.StatusCode, body)
			}
			if !json.Valid(body) {
				t.Fatalf("错误响应不是JSON：%s", body)
			}
			if calls.Load() < 1 || calls.Load() > 3 {
				t.Fatalf("重试超出本轮预算：%d", calls.Load())
			}
			t.Logf("upstream=%d downstream=%d attempts=%d elapsed=%s", status, resp.StatusCode, calls.Load(), time.Since(start).Round(time.Millisecond))
		})
	}
}

func TestCompatDeadlineCancelsUpstream(t *testing.T) {
	canceled := make(chan struct{}, 1)
	endpoint, client := compatGateway(t, func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		r.Body.Close()
		<-r.Context().Done()
		canceled <- struct{}{}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	payload, _ := json.Marshal(compatPayload(false))
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Api-Key", "alpha-client-secret")
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if resp != nil {
		resp.Body.Close()
	}
	if err == nil {
		t.Fatal("期限到达后未终止请求")
	}
	select {
	case <-canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("期限到达后上游仍未取消")
	}
	t.Log("客户端150ms期限传播到mock上游，无额外后台重试")
}

func TestCompatSmallOutputLimitObservation(t *testing.T) {
	observed := make(chan map[string]any, 1)
	endpoint, client := compatGateway(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		observed <- body
		compatCompleted(w, compatText())
	})
	payload := compatPayload(false)
	payload["max_tokens"] = 1
	resp := compatRequest(t, client, endpoint, payload, context.Background())
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !json.Valid(body) {
		t.Fatalf("小输出预算请求失败：%d %s", resp.StatusCode, body)
	}
	sent := <-observed
	t.Logf("入站max_tokens=1，出站max_output_tokens=%v max_tokens=%v；mock返回usage.output_tokens=9（只观察映射，不代表真实上游遵守预算）", sent["max_output_tokens"], sent["max_tokens"])
}

func TestCompatTruncatedStreamObservation(t *testing.T) {
	endpoint, client := compatGateway(t, func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		r.Body.Close()
		w.Header().Set("Content-Type", "text/event-stream")
		for _, event := range []string{
			`{"type":"response.created","response":{"id":"resp_broken","model":"responses-target","output":[]}}`,
			`{"type":"response.output_item.added","output_index":0,"item":{"id":"msg_broken","type":"message","role":"assistant","content":[]}}`,
			`{"type":"response.content_part.added","output_index":0,"content_index":0,"part":{"type":"output_text","text":""}}`,
			`{"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"未完成"}`,
		} {
			fmt.Fprintf(w, "data: %s\n\n", event)
			w.(http.Flusher).Flush()
		}
		// 合法SSE帧之后直接结束HTTP，刻意不发送response.completed。
	})
	resp := compatRequest(t, client, endpoint, compatPayload(true), context.Background())
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !bytes.Contains(body, []byte("未完成")) {
		t.Fatalf("未观察到断流前文本：%d %s %v", resp.StatusCode, body, err)
	}
	if !bytes.Contains(body, []byte(`"type":"error"`)) || bytes.Contains(body, []byte(`"type":"message_stop"`)) {
		t.Fatalf("不完整流必须明确报错，不能标为正常完成：%s", body)
	}
	t.Logf("无response.completed时：read_error=%v message_stop=%t error_event=%t body=%s", err, bytes.Contains(body, []byte(`"type":"message_stop"`)), bytes.Contains(body, []byte(`"type":"error"`)), body)
}
