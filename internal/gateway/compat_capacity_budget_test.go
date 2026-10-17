package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shichao-wang/cpa/internal/store"
)

func TestCapacityBudgetAccounting(t *testing.T) {
	for _, code := range []string{"context_length_exceeded", "other_error"} {
		t.Run(code, func(t *testing.T) {
			ledger := filepath.Join(t.TempDir(), "budget.json")
			if err := store.WriteJSON(ledger, longBudget{}); err != nil {
				t.Fatal(err)
			}
			calls := 0
			base := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: 400, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"code":"` + code + `","type":"invalid_request_error"}}`))}, nil
			})
			run := func() (*longTransport, error) {
				tr := &longTransport{ledger: ledger, capacityBudget: true, inner: &compatRealTransport{base: base, capacityProbe: true}}
				req, _ := http.NewRequest("POST", "https://chatgpt.com/backend-api/codex/responses", strings.NewReader(`{"model":"gpt-6.1-sol","input":[]}`))
				resp, err := tr.RoundTrip(req)
				if err == nil {
					io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
				}
				return tr, err
			}
			tr, err := run()
			b, readErr := tr.load()
			if readErr != nil {
				t.Fatal(readErr)
			}
			if code == "other_error" {
				if err == nil || !b.Stopped || b.Reserved == 0 {
					t.Fatal("非容量异常未保留预留并熔断")
				}
				_, _ = run()
				if calls != 1 {
					t.Fatal("熔断后仍触达上游")
				}
				return
			}
			if err != nil || b.Used != 1 || b.Stopped || b.Reserved != 0 || b.UnknownInput == 0 || b.UnknownOutput != 8192 || b.ContextRejections != 1 || b.Input != 0 || b.Output != 0 {
				t.Fatalf("容量拒绝未保守结算：%+v %v", b, err)
			}
			// 同一客户端用例的自动重试不能额外触达上游。
			req, _ := http.NewRequest("POST", "https://chatgpt.com/backend-api/codex/responses", strings.NewReader(`{"input":[]}`))
			if _, err = tr.RoundTrip(req); err == nil || calls != 1 {
				t.Fatal("单用例多次请求未拒绝")
			}
			_, err = run()
			if err != nil || calls != 2 {
				t.Fatal("计划内邻近复测被错误拒绝")
			}
			_, err = run()
			if err == nil || calls != 2 {
				t.Fatal("未结算输出预留没有计入预算")
			}
		})
	}
}

// 只校验用户要求发送的文档与召回指令；客户端附加的system-reminder另计，不冒充截断。
func capacityInputText(payload map[string]any) (string, string) {
	var texts []string
	var collect func(any)
	collect = func(v any) {
		switch v := v.(type) {
		case []any:
			for _, x := range v {
				collect(x)
			}
		case map[string]any:
			if text, ok := v["text"].(string); ok {
				texts = append(texts, text)
			}
			for k, x := range v {
				if k != "text" {
					collect(x)
				}
			}
		}
	}
	collect(payload["input"])
	all := strings.Join(texts, "\n")
	start := strings.Index(all, "<document>\nCPA_FACT_FIRST=")
	const end = "No tools, no explanation."
	last := strings.LastIndex(all, end)
	if start < 0 || last < start || strings.Count(all, "<document>\nCPA_FACT_FIRST=") != 1 {
		return all, ""
	}
	return all, all[start : last+len(end)]
}

func TestCapacityInputObservation(t *testing.T) {
	prompt := "<document>\nCPA_FACT_FIRST=1\n a a\nCPA_FACT_MIDDLE=2\n a a\nCPA_FACT_LAST=3\n</document>\nReturn only JSON. No tools, no explanation."
	raw, _ := json.Marshal(map[string]any{"input": []any{map[string]any{"content": []any{map[string]any{"type": "input_text", "text": "<system-reminder>fixture</system-reminder>\n" + prompt + "\n<system-reminder>fixture</system-reminder>"}}}}})
	var payload map[string]any
	json.Unmarshal(raw, &payload)
	all, got := capacityInputText(payload)
	if got != prompt || len(all) <= len(got) {
		t.Fatal("附加提示导致正文校验错误")
	}
}

func TestCapacityBudgetAdmission(t *testing.T) {
	ledger := filepath.Join(t.TempDir(), "budget.json")
	calls := 0
	base := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		t.Fatal("拒绝后不应触达上游")
		return nil, nil
	})
	for _, b := range []longBudget{{Used: 10}, {Input: 7999999}, {UnknownInput: 7999999}, {Output: 11809}, {UnknownOutput: 11809}, {Reserved: 1}, {Stopped: true}, {Limits: longLimits{12, 300000, 12000}}} {
		if err := store.WriteJSON(ledger, b); err != nil {
			t.Fatal(err)
		}
		tr := &longTransport{ledger: ledger, capacityBudget: true, inner: &compatRealTransport{base: base, capacityProbe: true}}
		req, _ := http.NewRequest("POST", "https://chatgpt.com/backend-api/codex/responses", strings.NewReader(`{"input":[]}`))
		if _, err := tr.RoundTrip(req); err == nil {
			t.Fatal("容量预算准入未拒绝")
		}
	}
	if calls != 0 {
		t.Fatal("拒绝后仍触达上游")
	}
}
