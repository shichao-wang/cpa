package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shichao-wang/cpa/internal/config"
	"github.com/shichao-wang/cpa/internal/store"
)

// TestCompatibilityParametersThroughMessages 记录固定 SDK 版本的公开协议行为；
// 这些断言不代表上游真实模型接受参数，也不代表兼容映射等价于原生能力。
func TestCompatibilityParametersThroughMessages(t *testing.T) {
	observed := make(chan map[string]any, 64)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Errorf("意外上游路径：%s", r.URL.Path)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("上游请求不是 JSON：%v", err)
		}
		observed <- payload
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "event: response.completed\ndata: "+`{"type":"response.completed","response":{"id":"resp_compat","object":"response","status":"completed","model":"gpt-6.1-sol","output":[{"id":"msg_compat","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"兼容性 mock 回复","annotations":[]}]}],"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}}`+"\n\n")
	}))
	defer upstream.Close()
	listen, err := reserveLoopbackAddress(t)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.New(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	state := testState(listen, upstream.URL, upstream.URL, upstream.URL, upstream.URL)
	conn := state.Connections["responses"]
	conn.Models = []config.Model{{ID: "gpt-6.1-sol", Name: "兼容性模型"}, {ID: "compat-unknown-model", Name: "未知模型"}}
	state.Connections["responses"] = conn
	profile := state.Profiles["alpha"]
	for slot, binding := range profile.Models {
		binding.Connection = "responses"
		binding.TargetModel = "gpt-6.1-sol"
		profile.Models[slot] = binding
	}
	haiku := profile.Models["haiku"]
	haiku.TargetModel = "compat-unknown-model"
	profile.Models["haiku"] = haiku
	state.Profiles["alpha"] = profile
	delete(state.Profiles, "beta")
	if err := st.Update(func(current *config.State) error { *current = *state; return nil }); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, st, "compat-parameters", "") }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Error("参数测试服务未停止")
		}
	})
	waitForReady(t, st)
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	request := func(method, path, body string) (int, []byte) {
		t.Helper()
		req, err := http.NewRequest(method, "http://"+listen+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-Api-Key", "alpha-client-secret")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Anthropic-Version", "2023-06-01")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, data
	}
	for _, tc := range []struct{ name, extra, effort string }{
		{"default", ``, "medium"},
		{"effort_without_thinking", `,"output_config":{"effort":"high"}`, "medium"},
		{"adaptive_low", `,"thinking":{"type":"adaptive"},"output_config":{"effort":"low"}`, "low"},
		{"adaptive_medium", `,"thinking":{"type":"adaptive"},"output_config":{"effort":"medium"}`, "medium"},
		{"adaptive_high", `,"thinking":{"type":"adaptive"},"output_config":{"effort":"high"}`, "high"},
		{"adaptive_xhigh", `,"thinking":{"type":"adaptive"},"output_config":{"effort":"xhigh"}`, "xhigh"},
		{"adaptive_minimal_clamps_low", `,"thinking":{"type":"adaptive"},"output_config":{"effort":"minimal"}`, "low"},
		{"adaptive_max", `,"thinking":{"type":"adaptive"},"output_config":{"effort":"max"}`, "max"},
		{"adaptive_display_omitted_high", `,"thinking":{"type":"adaptive","display":"omitted"},"output_config":{"effort":"high"}`, "high"},
		{"adaptive_default", `,"thinking":{"type":"adaptive"}`, "xhigh"},
		{"disabled", `,"thinking":{"type":"disabled"}`, "low"},
		{"enabled_default", `,"thinking":{"type":"enabled"}`, "medium"},
		{"enabled_effort", `,"thinking":{"type":"enabled"},"output_config":{"effort":"high"}`, "high"},
		{"budget_1024", `,"thinking":{"type":"enabled","budget_tokens":1024}`, "low"},
		{"budget_8192", `,"thinking":{"type":"enabled","budget_tokens":8192}`, "medium"},
		{"budget_24576", `,"thinking":{"type":"enabled","budget_tokens":24576}`, "high"},
		{"budget_32768", `,"thinking":{"type":"enabled","budget_tokens":32768}`, "xhigh"},
		{"budget_overrides_effort", `,"thinking":{"type":"enabled","budget_tokens":1024},"output_config":{"effort":"high"}`, "low"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"model":"claude-opus-test","max_tokens":64000,"messages":[{"role":"user","content":"仅回复 mock"}]` + tc.extra + `}`
			status, data := request("POST", "/v1/messages", body)
			if status != 200 {
				t.Fatalf("请求失败：%d %s", status, data)
			}
			var sent map[string]any
			select {
			case sent = <-observed:
			case <-time.After(time.Second):
				t.Fatal("未观测到出站请求")
			}
			reasoning, _ := sent["reasoning"].(map[string]any)
			if reasoning["effort"] != tc.effort {
				t.Errorf("实际 effort=%v，期望 %s", reasoning["effort"], tc.effort)
			}
			if sent["model"] != "gpt-6.1-sol" {
				t.Errorf("实际上游模型=%v", sent["model"])
			}
			for _, field := range []string{"thinking", "output_config"} {
				if _, exists := sent[field]; exists {
					t.Errorf("Anthropic 专有字段未转换：%s", field)
				}
			}
			if !bytes.Contains(data, []byte(`"model":"claude-opus-test"`)) {
				t.Error("响应模型未还原公开 ID")
			}
			t.Logf("effort=%v，上游路径=/responses，状态=%d", reasoning["effort"], status)
		})
	}
	for _, tc := range []struct {
		name, budget, effort string
		status               int
	}{
		{"budget_negative_invalid", "-2", "", 400},
		{"budget_invalid_string", `"invalid"`, "", 400},
		{"budget_larger_than_max_tokens", "32768", "", 400},
		{"budget_zero", "0", "", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := fmt.Sprintf(`{"model":"claude-opus-test","max_tokens":100,"messages":[{"role":"user","content":"mock"}],"thinking":{"type":"enabled","budget_tokens":%s}}`, tc.budget)
			status, data := request("POST", "/v1/messages", body)
			if status != tc.status {
				t.Fatalf("预算边界状态=%d，期望%d：%s", status, tc.status, data)
			}
			if status != 200 {
				select {
				case <-observed:
					t.Error("非法预算到达上游")
				default:
				}
				return
			}
			select {
			case sent := <-observed:
				reasoning, _ := sent["reasoning"].(map[string]any)
				if reasoning["effort"] != tc.effort {
					t.Errorf("预算 %s 实际 effort=%v，期望%s", tc.budget, reasoning["effort"], tc.effort)
				}
				t.Logf("budget=%s、max_tokens=100 转换后 effort=%v、max_output_tokens=%v", tc.budget, reasoning["effort"], sent["max_output_tokens"])
			case <-time.After(time.Second):
				t.Fatal("未观测预算边界出站")
			}
		})
	}
	t.Run("invalid_effort_rejected", func(t *testing.T) {
		status, data := request("POST", "/v1/messages", `{"model":"claude-opus-test","max_tokens":20,"messages":[{"role":"user","content":"mock"}],"thinking":{"type":"adaptive"},"output_config":{"effort":"ultra"}}`)
		if status != 400 || !bytes.Contains(data, []byte("不支持")) {
			t.Fatalf("非法 effort 未明确拒绝：%d %s", status, data)
		}
		select {
		case <-observed:
			t.Error("非法 effort 到达上游")
		default:
		}
	})
	t.Run("unknown_model_rejects_explicit_reasoning", func(t *testing.T) {
		status, data := request("POST", "/v1/messages", `{"model":"claude-haiku-test","max_tokens":20,"messages":[{"role":"user","content":"mock"}],"thinking":{"type":"adaptive"},"output_config":{"effort":"high"}}`)
		if status != 400 || !bytes.Contains(data, []byte("SDK 未识别")) {
			t.Fatalf("未知 SDK 模型未拒绝显式推理：%d %s", status, data)
		}
		select {
		case <-observed:
			t.Error("未知模型的显式推理仍到达上游")
		default:
		}
	})
	t.Run("catalog_has_no_context_limit", func(t *testing.T) {
		status, data := request("GET", "/v1/models", "")
		var result struct {
			Data []map[string]any `json:"data"`
		}
		if status != 200 || json.Unmarshal(data, &result) != nil || len(result.Data) != 3 {
			t.Fatalf("模型目录异常：%d %s", status, data)
		}
		for _, model := range result.Data {
			for _, key := range []string{"context_length", "context_window", "max_input_tokens", "max_output_tokens", "capabilities"} {
				if _, exists := model[key]; exists {
					t.Errorf("目录增加了 %s，请重新评估上下文能力：%v", key, model)
				}
			}
		}
		t.Log("目录仅含公开 ID/名称/说明/创建时间；没有上下文容量声明")
	})
	t.Run("unconfigured_1m_suffix_rejected", func(t *testing.T) {
		status, data := request("POST", "/v1/messages", `{"model":"claude-opus-test[1m]","max_tokens":20,"messages":[{"role":"user","content":"mock"}]}`)
		if status != 400 || !bytes.Contains(data, []byte("所选 profile 未开放该模型")) {
			t.Fatalf("未配置后缀未按授权边界拒绝：%d %s", status, data)
		}
		select {
		case <-observed:
			t.Error("未授权模型到达上游")
		default:
		}
	})
	t.Run("count_tokens", func(t *testing.T) {
		count := func(content string) int64 {
			status, data := request("POST", "/v1/messages/count_tokens", fmt.Sprintf(`{"model":"claude-opus-test","messages":[{"role":"user","content":%q}]}`, content))
			var result struct {
				InputTokens int64 `json:"input_tokens"`
			}
			if status != 200 || json.Unmarshal(data, &result) != nil || result.InputTokens <= 0 {
				t.Fatalf("token 计数失败：%d %s", status, data)
			}
			return result.InputTokens
		}
		small, large := count("hello"), count(strings.Repeat("hello world ", 1000))
		if large <= small {
			t.Errorf("长输入 token 未增加：%d <= %d", large, small)
		}
		select {
		case <-observed:
			t.Error("本地 token 计数意外调用推理上游")
		default:
		}
		t.Logf("短输入=%d，长输入=%d；未调用上游，此计数不证明实际上下文容量", small, large)
	})
}
