package gateway

import (
	"context"
	"encoding/json"
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

func TestOutputLimitCompatibility(t *testing.T) {
	var calls atomic.Int32
	endpoint, client := compatGateway(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		io.Copy(io.Discard, r.Body)
		compatCompleted(w, compatText())
	})
	response := compatRequest(t, client, endpoint, compatPayload(false), context.Background())
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.StatusCode != 200 || response.Header.Get("X-CPA-Output-Limit") != "not-enforced" || !strings.Contains(response.Header.Get("X-CPA-Compatibility-Warnings"), "output-limit-not-enforced") {
		t.Fatalf("未明确告知预算不执行：%d %v", response.StatusCode, response.Header)
	}
	before := calls.Load()
	for _, value := range []string{"true", "invalid"} {
		req, _ := http.NewRequest("POST", endpoint, strings.NewReader(`{"model":"claude-haiku-test","max_tokens":1,"messages":[]}`))
		req.Header.Set("X-Api-Key", "alpha-client-secret")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-CPA-Require-Output-Limit", value)
		resp := mustDo(t, client, req)
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 400 {
			t.Fatalf("严格预算或非法选项未拒绝：%d", resp.StatusCode)
		}
	}
	if calls.Load() != before {
		t.Fatal("严格预算失败仍触发了上游")
	}
}

func TestResponsesParameterValidationAndWarnings(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		io.Copy(io.Discard, r.Body)
		compatCompleted(w, compatText())
	}))
	t.Cleanup(upstream.Close)
	listen, err := reserveLoopbackAddress(t)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.New(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	state := testState(listen, upstream.URL, upstream.URL, upstream.URL, upstream.URL)
	connection := state.Connections["responses"]
	connection.Models = []config.Model{{ID: "gpt-6.1-sol"}}
	state.Connections["responses"] = connection
	profile := state.Profiles["alpha"]
	binding := profile.Models["haiku"]
	binding.TargetModel = "gpt-6.1-sol"
	profile.Models["haiku"] = binding
	state.Profiles["alpha"] = profile
	delete(state.Profiles, "beta")
	if err := st.Update(func(s *config.State) error { *s = *state; return nil }); err != nil {
		t.Fatal(err)
	}
	startManagedTest(t, st, runtimeOptions{})
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil}}
	t.Cleanup(client.CloseIdleConnections)
	postRaw := func(body string) *http.Response {
		t.Helper()
		req, _ := http.NewRequest("POST", "http://"+listen+"/v1/messages", strings.NewReader(body))
		req.Header.Set("X-Api-Key", "alpha-client-secret")
		req.Header.Set("Content-Type", "application/json")
		return mustDo(t, client, req)
	}
	post := func(extra string) *http.Response {
		return postRaw(`{"model":"claude-haiku-test","max_tokens":64000,"messages":[{"role":"user","content":"mock"}]` + extra + `}`)
	}
	for _, invalid := range []string{`"1"`, `null`, `true`, `0`, `-1`, `1.5`, `2147483648`, `9223372036854775808`} {
		before := calls.Load()
		resp := postRaw(`{"model":"claude-haiku-test","max_tokens":` + invalid + `,"messages":[]}`)
		data, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 400 || calls.Load() != before || !strings.Contains(string(data), "max_tokens 必须") {
			t.Fatalf("非法输出预算未在入口拒绝：%d %s", resp.StatusCode, data)
		}
	}
	for _, extra := range []string{
		`,"thinking":null`, `,"thinking":[]`, `,"thinking":{"budget_tokens":1024}`,
		`,"thinking":{"type":"other"}`, `,"thinking":{"type":"enabled","budget_tokens":"1024"}`,
		`,"thinking":{"type":"enabled","budget_tokens":null}`, `,"thinking":{"type":"enabled","budget_tokens":true}`,
		`,"thinking":{"type":"enabled","budget_tokens":1024.5}`, `,"thinking":{"type":"enabled","budget_tokens":1e3}`,
		`,"thinking":{"type":"enabled","budget_tokens":0}`, `,"thinking":{"type":"enabled","budget_tokens":-1}`,
		`,"thinking":{"type":"enabled","budget_tokens":1023}`, `,"thinking":{"type":"enabled","budget_tokens":2147483648}`,
		`,"thinking":{"type":"enabled","budget_tokens":9223372036854775808}`,
		`,"thinking":{"type":"enabled","budget_tokens":64000}`, `,"thinking":{"type":"enabled","budget_tokens":64001}`,
		`,"thinking":{"type":"disabled","budget_tokens":1024}`, `,"thinking":{"type":"adaptive","budget_tokens":1024}`,
		`,"thinking":{"type":"enabled","budget_tokens":1024,"budget_tokens":2048}`,
		`,"thinking":{"type":"enabled"},"thinking":{"type":"disabled"}`,
		`,"max_tokens":100`, `,"output_config":null`, `,"output_config":{"effort":null}`,
		`,"output_config":{"effort":3}`, `,"output_config":{"effort":""}`,
		`,"output_config":{"effort":"unknown-effort"}`,
		`,"output_config":{"effort":"low","effort":"high"}`,
	} {
		t.Run(extra, func(t *testing.T) {
			before := calls.Load()
			resp := post(extra)
			data, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != 400 || calls.Load() != before || !json.Valid(data) {
				t.Fatalf("非法参数未在上游前拒绝：%d %s", resp.StatusCode, data)
			}
		})
	}
	for _, tc := range []struct{ extra, warning string }{
		{`,"thinking":{"type":"disabled"}`, "thinking-disabled-mapped"},
		{`,"thinking":{"type":"adaptive"},"output_config":{"effort":"high"}`, "adaptive-effort-mapping"},
		{`,"thinking":{"type":"enabled","budget_tokens":1024},"output_config":{"effort":"high"}`, "thinking-budget-overrides-effort"},
		{`,"output_config":{"effort":"high"}`, "effort-without-thinking-ignored"},
	} {
		resp := post(tc.extra)
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("X-CPA-Compatibility-Warnings"), tc.warning) {
			t.Fatalf("合法兼容请求未保留或缺少映射说明：%d %v", resp.StatusCode, resp.Header)
		}
	}
	req, _ := http.NewRequest("GET", "http://"+listen+"/v1/models", nil)
	req.Header.Set("X-Api-Key", "alpha-client-secret")
	resp := mustDo(t, client, req)
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(data), `"cpa_capabilities"`) || !strings.Contains(string(data), `"contextWindow":"unverified"`) || !strings.Contains(string(data), `"reasoning":"effort-mapping"`) {
		t.Fatalf("模型目录未声明能力限制：%s", data)
	}
}

func TestNativeAnthropicKeepsInterleavedBudgetPolicy(t *testing.T) {
	endpoint, client := compatGateway(t, func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"msg_native","type":"message","role":"assistant","model":"anthropic-target","content":[{"type":"text","text":"native"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	})
	req, _ := http.NewRequest("POST", endpoint, strings.NewReader(`{"model":"claude-sonnet-test","max_tokens":1000,"thinking":{"type":"enabled","budget_tokens":2048},"messages":[{"role":"user","content":"mock"}]}`))
	req.Header.Set("X-Api-Key", "alpha-client-secret")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Anthropic-Beta", "interleaved-thinking-2025-05-14")
	resp := mustDo(t, client, req)
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("X-CPA-Output-Limit") != "" || !strings.Contains(string(data), "native") {
		t.Fatalf("将Responses规则误用于原生Anthropic：%d %s", resp.StatusCode, data)
	}
}
