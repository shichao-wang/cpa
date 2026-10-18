package gateway

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/shichao-wang/cpa/internal/agent/claudecode"
	"github.com/shichao-wang/cpa/internal/config"
	"github.com/shichao-wang/cpa/internal/store"
	"github.com/sirupsen/logrus"
	"github.com/tiktoken-go/tokenizer"
	"golang.org/x/sys/unix"
)

// 与上一轮30次短请求账本隔离；每次准入先落盘，崩溃后未结算的调用禁止继续。
type longLimits struct {
	Calls  int `json:"calls"`
	Input  int `json:"input"`
	Output int `json:"output"`
}

func (r *longTransport) limits() longLimits {
	if r.capacityBudget {
		return longLimits{10, 8000000, 20000}
	}
	if r.interactiveBudget {
		return longLimits{12, 300000, 12000}
	}
	if r.supplement {
		return longLimits{30, 800000, 20000}
	}
	return longLimits{20, 2000000, 50000}
}

type longBudget struct {
	Limits            longLimits `json:"limits,omitempty"`
	Used              int        `json:"used"`
	Input             int        `json:"input_tokens"`
	Output            int        `json:"output_tokens"`
	Reserved          int        `json:"reserved_input"`
	UnknownInput      int        `json:"unknown_input_reserve,omitempty"`
	UnknownOutput     int        `json:"unknown_output_reserve,omitempty"`
	ContextRejections int        `json:"context_rejections,omitempty"`
	Stopped           bool       `json:"stopped"`
}
type longTransport struct {
	inner             *compatRealTransport
	ledger            string
	supplement        bool
	interactiveBudget bool
	capacityBudget    bool
	mu                sync.Mutex
}

func (r *longTransport) RoundTripperFor(*coreauth.Auth) http.RoundTripper { return r }
func (r *longTransport) load() (longBudget, error) {
	var b longBudget
	data, err := os.ReadFile(r.ledger)
	if err != nil {
		return b, err
	}
	err = json.Unmarshal(data, &b)
	if err == nil && (b.Used < 0 || b.Input < 0 || b.Output < 0 || b.Reserved < 0 || b.UnknownInput < 0 || b.UnknownOutput < 0 || b.ContextRejections < 0) {
		err = errors.New("无效预算账本")
	}
	return b, err
}
func (r *longTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r.mu.Lock()
	// 此锁直到流关闭才释放，避免多个在途请求绕过累计预算检查。
	b, err := r.load()
	limits := r.limits()
	if err != nil {
		r.mu.Unlock()
		return nil, errors.New("无法读取长上下文预算")
	}
	data, err := io.ReadAll(req.Body)
	if err != nil {
		r.mu.Unlock()
		return nil, errors.New("无法读取长上下文请求")
	}
	req.Body.Close()
	req.Body = io.NopCloser(bytes.NewReader(data))
	codec, _ := tokenizer.Get(tokenizer.O200kBase)
	tokens, err := codec.Count(string(data))
	estimate := tokens*12/10 + 2048
	maxTokens := 220000
	if r.capacityBudget {
		maxTokens = 900000
	}
	if err != nil || b.Stopped || b.Reserved != 0 || b.Used >= limits.Calls || b.Output+b.UnknownOutput+8192 > limits.Output || tokens > maxTokens || b.Input+b.UnknownInput+estimate > limits.Input || (r.capacityBudget && r.inner.count.Load() > 0) {
		r.mu.Unlock()
		return nil, errors.New("长上下文预算或单次容量限制阻止请求")
	}
	if b.Limits != (longLimits{}) && b.Limits != limits {
		r.mu.Unlock()
		return nil, errors.New("预算授权与账本不一致")
	}
	b.Limits = limits
	b.Used++
	b.Reserved = estimate
	if store.WriteJSON(r.ledger, b) != nil {
		r.mu.Unlock()
		return nil, errors.New("无法预留长上下文预算")
	}
	response, err := r.inner.RoundTrip(req)
	if err != nil {
		b.Stopped = true
		_ = store.WriteJSON(r.ledger, b)
		r.mu.Unlock()
		return nil, err
	}
	response.Body = &longBody{ReadCloser: response.Body, finish: func() {
		defer r.mu.Unlock()
		r.inner.mu.Lock()
		rows := r.inner.observations
		var usage map[string]any
		contextRejected := false
		if len(rows) > 0 {
			usage, _ = rows[len(rows)-1]["usage"].(map[string]any)
			contextRejected, _ = rows[len(rows)-1]["context_rejection"].(bool)
		}
		input, inOK := usage["input_tokens"].(float64)
		output, outOK := usage["output_tokens"].(float64)
		r.inner.mu.Unlock()
		if r.capacityBudget && contextRejected {
			b.UnknownInput += b.Reserved
			b.UnknownOutput += 8192
			b.ContextRejections++
			b.Reserved = 0
		} else if !inOK || !outOK {
			b.Stopped = true
		} else {
			b.Input += int(input)
			b.Output += int(output)
			b.Reserved = 0
		}
		if b.Input+b.UnknownInput >= limits.Input || b.Output+b.UnknownOutput >= limits.Output {
			b.Stopped = true
		}
		if store.WriteJSON(r.ledger, b) != nil {
			r.inner.failed.Store(true)
		}
	}}
	return response, nil
}

type longBody struct {
	io.ReadCloser
	finish func()
	once   sync.Once
}

func (b *longBody) Close() error { err := b.ReadCloser.Close(); b.once.Do(b.finish); return err }

func TestLongBudgetAdmission(t *testing.T) {
	ledger := filepath.Join(t.TempDir(), "budget.json")
	calls := 0
	base := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":100,\"output_tokens\":5}}}\n\n"))}, nil
	})
	for _, b := range []longBudget{{Used: 20}, {Reserved: 100}, {Input: 1999999}, {Output: 50000}, {Stopped: true}} {
		store.WriteJSON(ledger, b)
		tr := &longTransport{ledger: ledger, inner: &compatRealTransport{base: base}}
		req, _ := http.NewRequest("POST", "https://chatgpt.com/backend-api/codex/responses", strings.NewReader(`{"input":[]}`))
		if _, err := tr.RoundTrip(req); err == nil {
			t.Fatal("预算限制未拒绝")
		}
	}
	if calls != 0 {
		t.Fatal("拒绝后仍触达上游")
	}
	store.WriteJSON(ledger, longBudget{})
	tr := &longTransport{ledger: ledger, inner: &compatRealTransport{base: base}}
	req, _ := http.NewRequest("POST", "https://chatgpt.com/backend-api/codex/responses", strings.NewReader(`{"input":[]}`))
	response, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	b, err := tr.load()
	if err != nil || b.Used != 1 || b.Input != 100 || b.Output != 5 || b.Reserved != 0 || calls != 1 {
		t.Fatalf("请求和usage结算错误：%+v %v", b, err)
	}
}

func TestSupplementBudgetAdmission(t *testing.T) {
	ledger := filepath.Join(t.TempDir(), "budget.json")
	calls := 0
	base := roundTripFunc(func(r *http.Request) (*http.Response, error) { calls++; return nil, errors.New("不应触达上游") })
	for _, b := range []longBudget{{Used: 30}, {Input: 799999}, {Output: 11809}, {Reserved: 1}, {Stopped: true}, {Limits: longLimits{20, 2000000, 50000}}} {
		if err := store.WriteJSON(ledger, b); err != nil {
			t.Fatal(err)
		}
		tr := &longTransport{ledger: ledger, inner: &compatRealTransport{base: base}, supplement: true}
		req, _ := http.NewRequest("POST", "https://chatgpt.com/backend-api/codex/responses", strings.NewReader(`{"input":[]}`))
		if _, err := tr.RoundTrip(req); err == nil {
			t.Fatal("补验预算限制未拒绝")
		}
	}
	if calls != 0 {
		t.Fatal("拒绝后仍触达上游")
	}
}

func TestInteractiveBudgetAdmission(t *testing.T) {
	ledger := filepath.Join(t.TempDir(), "budget.json")
	calls := 0
	base := roundTripFunc(func(r *http.Request) (*http.Response, error) { calls++; return nil, errors.New("不应触达上游") })
	for _, b := range []longBudget{{Used: 12}, {Input: 299999}, {Output: 3809}, {Reserved: 1}, {Stopped: true}, {Limits: longLimits{30, 800000, 20000}}} {
		if err := store.WriteJSON(ledger, b); err != nil {
			t.Fatal(err)
		}
		tr := &longTransport{ledger: ledger, inner: &compatRealTransport{base: base}, interactiveBudget: true}
		req, _ := http.NewRequest("POST", "https://chatgpt.com/backend-api/codex/responses", strings.NewReader(`{"input":[]}`))
		if _, err := tr.RoundTrip(req); err == nil {
			t.Fatal("交互补验预算限制未拒绝")
		}
	}
	if calls != 0 {
		t.Fatal("拒绝后仍触达上游")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// 所有真实执行均需显式开启，并指定独立账本、证据文件及白名单用例。
func TestCompatLongContext(t *testing.T) {
	mode := os.Getenv("CPA_LONG_MODE")
	if mode != "real" && mode != "mock" && mode != "mock-recovery" {
		t.Skip("长上下文测试须显式选择 real 或 mock")
	}
	which := os.Getenv("CPA_LONG_CASE")
	capacityCase := os.Getenv("CPA_LONG_BUDGET") == "capacity-boundary" && regexp.MustCompile(`^capacity-(25[6-9]|2[6-9][0-9]|[3-8][0-9][0-9])-(a|b)$`).MatchString(which)
	if capacityCase {
		var size int
		fmt.Sscanf(which, "capacity-%d-", &size)
		if size > 896 {
			t.Fatal("容量档位超过896K授权")
		}
	}
	if !capacityCase && !regexp.MustCompile(`^(long-(16|32|64|128|192)-(a|b)|manual|auto|resume)$`).MatchString(which) {
		t.Fatal("必须选择已声明的长上下文用例")
	}
	switch os.Getenv("CPA_LONG_BUDGET") {
	case "", "manual-supplement", "interactive-final":
	case "capacity-boundary":
		if !capacityCase {
			t.Fatal("容量预算只能用于已声明的容量档位")
		}
	default:
		t.Fatal("未知预算授权")
	}
	ledger, output := os.Getenv("CPA_LONG_LEDGER"), os.Getenv("CPA_LONG_OUTPUT")
	if ledger == "" || output == "" {
		t.Fatal("必须指定独立预算和证据路径")
	}
	fd, lockErr := unix.Open(ledger+".lock", unix.O_CREAT|unix.O_RDWR|unix.O_NOFOLLOW, 0600)
	if lockErr != nil {
		t.Fatal("无法创建预算执行锁")
	}
	defer unix.Close(fd)
	if unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB) != nil {
		t.Fatal("同一预算已有测试运行，拒绝并发消费")
	}
	defer unix.Flock(fd, unix.LOCK_UN)
	if _, err := os.Stat(ledger); os.IsNotExist(err) {
		if err = store.WriteJSON(ledger, longBudget{}); err != nil {
			t.Fatal(err)
		}
	}
	oldLevel := logrus.GetLevel()
	logrus.SetLevel(logrus.PanicLevel)
	t.Cleanup(func() { logrus.SetLevel(oldLevel) })
	root := t.TempDir()
	st, err := store.New(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	address, err := reserveLoopbackAddress(t)
	if err != nil {
		t.Fatal(err)
	}
	state := config.NewState()
	state.Listen = address
	connection := config.Connection{ID: "long-context-id", Name: "long-context", Protocol: config.Responses, AuthType: config.AuthAPIKey, BaseURL: "", CredentialRef: "upstream", Models: []config.Model{{ID: "gpt-6.1-sol"}}}
	var wireMu sync.Mutex
	var wire []map[string]any
	observeMock := func(protocol string, data []byte) {
		wireMu.Lock()
		defer wireMu.Unlock()
		entry := map[string]any{"protocol": protocol, "bytes": len(data)}
		if capacityCase {
			var payload map[string]any
			json.Unmarshal(data, &payload)
			all, prompt := capacityInputText(payload)
			hash := sha256.Sum256([]byte(prompt))
			entry["input_sha256"] = fmt.Sprintf("%x", hash)
			if prompt != "" {
				parts := strings.SplitN(all, prompt, 2)
				if len(parts[0]) < 1024 && len(parts[1]) < 1024 {
					entry["mock_prefix"] = parts[0]
					entry["mock_suffix"] = parts[1]
				}
			}
		}

		wire = append(wire, entry)
	}
	if mode == "real" {
		sourcePath := os.Getenv("CPA_LONG_SOURCE")
		if sourcePath == "" {
			t.Fatal("必须指定只读来源状态目录")
		}
		sourceFile := filepath.Join(sourcePath, "state.json")
		before, err := os.ReadFile(sourceFile)
		if err != nil {
			t.Fatal("读取来源失败")
		}
		fingerprint := sha256.Sum256(before)
		t.Cleanup(func() {
			after, err := os.ReadFile(sourceFile)
			if err != nil || sha256.Sum256(after) != fingerprint {
				t.Error("来源状态被修改")
			}
		})
		source, err := store.New(sourcePath)
		if err != nil {
			t.Fatal("来源目录不可用")
		}
		original, err := source.Read()
		if err != nil {
			t.Fatal("来源状态不可用")
		}
		found := false
		for _, c := range original.Connections {
			if c.AuthType == config.AuthCodexOAuth && c.Protocol == config.Responses && c.BaseURL == config.CodexBaseURL {
				for _, m := range c.Models {
					if m.ID == "gpt-6.1-sol" {
						connection = c
						found = true
						break
					}
				}
				if found {
					break
				}
			}
		}
		credential := original.OAuthCredentials[connection.CredentialRef]
		if !found || credential.ExpiresAt.Before(time.Now().Add(26*time.Hour)) {
			t.Fatal("没有有效且无需刷新的目标凭证")
		}
		state.OAuthCredentials[connection.CredentialRef] = credential
	} else {
		mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			data, _ := io.ReadAll(r.Body)
			observeMock("responses", data)
			r.Body = io.NopCloser(bytes.NewReader(data))
			if mode == "mock-recovery" && bytes.Contains(data, []byte("REMINDER: Do NOT call any tools")) && !bytes.Contains(data, []byte("CPA_RETRY")) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(400)
				io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"模拟压缩请求失败"}}`)
				return
			}
			longMockResponse(w, r)
		}))
		t.Cleanup(mock.Close)
		connection.BaseURL = mock.URL
		state.Secrets[connection.CredentialRef] = "mock-only"
	}
	state.Connections[connection.Name] = connection
	key, err := config.RandomID()
	if err != nil {
		t.Fatal(err)
	}
	profile := config.Profile{Name: "long-test", ID: "long-test", Agent: "claude-code", KeyRef: "downstream", Models: map[string]config.Binding{}}
	for slot, public := range map[string]string{"sonnet": "claude-sonnet-4-6", "opus": "claude-opus-4-6", "haiku": "claude-haiku-4-5-20251001", "fable": "claude-fable-5-1"} {
		profile.Models[slot] = config.Binding{PublicModel: public, Connection: connection.Name, TargetModel: "gpt-6.1-sol", Label: slot}
	}
	state.Profiles[profile.Name] = profile
	state.Secrets[profile.KeyRef] = key
	if err = st.Update(func(s *config.State) error { *s = *state; return nil }); err != nil {
		t.Fatal(err)
	}
	inner := &compatRealTransport{base: &http.Transport{Proxy: nil, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 80 * time.Second}, currentCase: which, timeout: 120 * time.Second, captureText: true, capacityProbe: capacityCase}
	transport := &longTransport{ledger: ledger, inner: inner, supplement: os.Getenv("CPA_LONG_BUDGET") == "manual-supplement", interactiveBudget: os.Getenv("CPA_LONG_BUDGET") == "interactive-final", capacityBudget: capacityCase}
	options := runtimeOptions{}
	if mode == "real" {
		options.transport = transport
	}
	directMock := os.Getenv("CPA_LONG_DIRECT_MOCK") == "1"
	if directMock && mode == "real" {
		t.Fatal("直接mock只允许mock模式")
	}
	if !directMock {
		manager := startManagedTest(t, st, options)
		manager.StopAutoRefresh()
		manager.SetRetryConfig(0, 0, 1)
	}
	home, work := filepath.Join(root, "home"), filepath.Join(root, "work")
	os.MkdirAll(home, 0700)
	os.MkdirAll(work, 0700)
	settings := filepath.Join(home, "settings.json")
	originalSettings := []byte(`{"language":"English"}`)
	os.WriteFile(settings, originalSettings, 0600)
	if err = claudecode.Apply(st, profile, settings); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if claudecode.Restore(st, settings) != nil {
			t.Error("临时settings恢复失败")
		}
		after, _ := os.ReadFile(settings)
		if os.Getenv("CPA_LONG_INTERACTIVE") == "1" {
			var restored map[string]any
			if json.Unmarshal(after, &restored) != nil || restored["language"] != "English" || bytes.Contains(after, []byte(key)) {
				t.Error("交互临时设置原字段或凭证恢复失败")
			}
		}
		if !bytes.Equal(bytes.TrimSpace(after), originalSettings) {
			var got, want any
			json.Unmarshal(after, &got)
			json.Unmarshal(originalSettings, &want)
			a, _ := json.Marshal(got)
			b, _ := json.Marshal(want)
			if !bytes.Equal(a, b) && os.Getenv("CPA_LONG_INTERACTIVE") != "1" {
				t.Error("临时设置回读不一致")
			}
		}
	})
	// Python仅继承白名单环境及临时profile key，真实OAuth凭证不进入子进程。
	runner, _ := filepath.Abs("../../scripts/compat/claude_long.py")
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	endpoint := "http://" + address
	if directMock {
		direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			data, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(data))
			observeMock("anthropic", data)
			longNativeMock(w, r)
		}))
		t.Cleanup(direct.Close)
		endpoint = direct.URL
	}
	command := exec.CommandContext(ctx, "python3", runner, "--case", which, "--output", output)
	if capacityCase {
		command.Args = append(command.Args, "--capacity")
	}
	if mode != "real" {
		command.Args = append(command.Args, "--mock")
	}
	if mode == "mock-recovery" {
		command.Args = append(command.Args, "--recover")
	}
	if os.Getenv("CPA_LONG_INTERACTIVE") == "1" {
		command.Args = append(command.Args, "--interactive")
	}
	if os.Getenv("CPA_LONG_DIAGNOSE") == "1" {
		command.Args = append(command.Args, "--diagnose-only")
	}
	if format := os.Getenv("CPA_LONG_COMPACT_FORMAT"); format != "" {
		command.Args = append(command.Args, "--compact-format", format)
	}
	if os.Getenv("CPA_LONG_PERSISTENT") == "0" {
		command.Args = append(command.Args, "--separate-processes")
	}
	if mode != "real" && os.Getenv("CPA_LONG_MOCK_TASK") == "1" {
		command.Args = append(command.Args, "--mock-tools")
	}
	command.Dir = work
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "TMPDIR=" + root, "CLAUDE_CONFIG_DIR=" + home, "ANTHROPIC_API_KEY=" + key, "ANTHROPIC_BASE_URL=" + endpoint, "ANTHROPIC_DEFAULT_SONNET_MODEL=claude-sonnet-4-6", "ANTHROPIC_DEFAULT_HAIKU_MODEL=claude-haiku-4-5-20251001", "DISABLE_TELEMETRY=1", "DISABLE_ERROR_REPORTING=1", "DISABLE_AUTOUPDATER=1", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1", "CLAUDE_CODE_MAX_OUTPUT_TOKENS=8192", "NO_PROXY=127.0.0.1,localhost,::1", "no_proxy=127.0.0.1,localhost,::1", "HTTP_PROXY=http://127.0.0.1:1", "HTTPS_PROXY=http://127.0.0.1:1", "ALL_PROXY=http://127.0.0.1:1"}

	if capacityCase {
		command.Env = append(command.Env, "CLAUDE_CODE_MAX_RETRIES=0", "DISABLE_AUTO_COMPACT=1")
	}
	data, runErr := command.CombinedOutput()
	evidence := map[string]any{"case": which, "mode": mode, "client_exit_ok": runErr == nil, "requests": inner.observations, "limits": transport.limits(), "source_unchanged_checked": mode == "real"}
	if mode == "real" {
		b, _ := transport.load()
		evidence["ledger"] = b
	}
	if mode != "real" {
		wireMu.Lock()
		evidence["mock_requests"] = append([]map[string]any(nil), wire...)
		if runErr == nil && which == "manual" && os.Getenv("CPA_LONG_PERSISTENT") != "0" {
			if len(wire) < 4 {
				t.Error("缺少工具、压缩和独立恢复请求")
			} else {
				initial := wire[0]["bytes"].(int)
				for _, request := range wire[len(wire)-2:] {
					if request["bytes"].(int) >= initial/4 {
						t.Error("恢复后请求仍携带原始长历史")
					}
				}
			}
		}
		wireMu.Unlock()
	}
	evidenceFile := output + ".gateway.json"
	if store.WriteJSON(evidenceFile, evidence) != nil {
		t.Error("证据写入失败")
	}
	if capacityCase && mode == "real" && len(inner.observations) == 1 {
		row := inner.observations[0]
		if row["context_rejection"] == true {
			evidence["outcome"] = "upstream_context_rejection"
			store.WriteJSON(evidenceFile, evidence)
			t.Log("已记录计划内容量拒绝及保守消耗预留")
			return
		}
		if runErr == nil {
			raw, _ := os.ReadFile(output)
			var client struct {
				Steps []struct {
					InputSHA string `json:"input_sha256"`
				} `json:"steps"`
			}
			if json.Unmarshal(raw, &client) != nil || len(client.Steps) == 0 || client.Steps[0].InputSHA != row["input_sha256"] {
				t.Fatal("上游文本与客户端输入摘要不一致，不能判定容量通过")
			}
		}
	}
	if runErr != nil {
		t.Logf("客户端诊断：%s", bytes.TrimSpace(data))
		t.Fatal("长上下文客户端验证失败，已保留脱敏证据")
	}
	t.Logf("完成 %s %s，证据 %s", mode, which, evidenceFile)
}

func longMockResponse(w http.ResponseWriter, r *http.Request) {
	data, _ := io.ReadAll(r.Body)
	codec, _ := tokenizer.Get(tokenizer.O200kBase)
	tokens, _ := codec.Count(string(data))
	text := "CPA_LONG_OK"
	var payload map[string]any
	json.Unmarshal(data, &payload)
	var stringsInRequest []string
	var collect func(any)
	collect = func(v any) {
		switch v := v.(type) {
		case string:
			stringsInRequest = append(stringsInRequest, v)
		case []any:
			for _, x := range v {
				collect(x)
			}
		case map[string]any:
			for _, x := range v {
				collect(x)
			}
		}
	}
	collect(payload)
	plain := strings.Join(stringsInRequest, "\n")
	values := map[string]string{}
	for _, f := range regexp.MustCompile(`CPA_FACT_(FIRST|MIDDLE|LAST)=([a-f0-9]{16})`).FindAllStringSubmatch(plain, -1) {
		values[f[1]] = f[2]
	}
	for _, f := range regexp.MustCompile(`"(FIRST|MIDDLE|LAST)"\s*:\s*"([a-f0-9]{16})"`).FindAllStringSubmatch(plain, -1) {
		values[f[1]] = f[2]
	}
	if len(values) == 3 {
		b, _ := json.Marshal(values)
		text = string(b)
	}
	if strings.Contains(plain, "REMINDER: Do NOT call any tools") {
		text = "<analysis>Preserve the synthetic task and facts.</analysis><summary>"
		for _, key := range []string{"FIRST", "MIDDLE", "LAST"} {
			text += "\nCPA_FACT_" + key + "=" + values[key]
		}
		text += "\nRead fixture.txt was completed. Pending: change compat_before to compat_after in fixture.txt and verify with Bash. Never modify protected.txt.</summary>"
	}
	if replayPath := os.Getenv("CPA_LONG_MOCK_REPLAY"); replayPath != "" && strings.Contains(plain, "REMINDER: Do NOT call any tools") {
		replay, err := os.ReadFile(replayPath)
		var saved struct {
			Requests []struct {
				Text string `json:"text"`
			} `json:"requests"`
		}
		if err != nil || (!strings.HasSuffix(replayPath, ".txt") && json.Unmarshal(replay, &saved) != nil) {
			http.Error(w, "无法读取脱敏摘要回放", 400)
			return
		}
		if err == nil && strings.HasSuffix(replayPath, ".txt") {
			saved.Requests = append(saved.Requests, struct {
				Text string `json:"text"`
			}{Text: string(replay)})
		}
		found := false
		for _, row := range saved.Requests {
			if strings.Contains(row.Text, "<summary>") {
				text = row.Text
				for _, f := range regexp.MustCompile(`CPA_FACT_(FIRST|MIDDLE|LAST)=([a-f0-9]{16})`).FindAllStringSubmatch(row.Text, -1) {
					text = strings.ReplaceAll(text, f[2], values[f[1]])
				}
				found = true
				break
			}
		}
		if !found {
			http.Error(w, "脱敏证据没有摘要", 400)
			return
		}
	}
	lastUserText := ""
	if input, ok := payload["input"].([]any); ok {
		for _, value := range input {
			if message, ok := value.(map[string]any); ok && message["role"] == "user" {
				candidate := ""
				switch content := message["content"].(type) {
				case string:
					candidate = content
				case []any:
					for _, part := range content {
						if part, ok := part.(map[string]any); ok {
							if part["type"] == "text" || part["type"] == "input_text" {
								candidate = fmt.Sprint(part["text"])
							}
						}
					}
				}
				if candidate != "" {
					lastUserText = candidate
				}
			}
		}
	}
	if strings.Contains(lastUserText, "Now complete the pending edit task") {
		cwd := regexp.MustCompile(`CWD: ([^\n]+)`).FindStringSubmatch(plain)
		if len(cwd) != 2 {
			http.Error(w, "mock缺少工作目录", 400)
			return
		}
		name, id := "Edit", "call_mock_edit"
		arguments := map[string]any{"file_path": filepath.Join(cwd[1], "fixture.txt"), "old_string": "compat_before", "new_string": "compat_after"}
		if bytes.Contains(data, []byte("call_mock_edit")) {
			name, id = "Bash", "call_mock_bash"
			arguments = map[string]any{"command": `test "$(cat fixture.txt)" = compat_after && test "$(cat protected.txt)" = do_not_change && printf CPA_EDIT_OK`}
		}
		if !bytes.Contains(data, []byte("call_mock_bash")) {
			args, _ := json.Marshal(arguments)
			emitLongMock(w, []any{map[string]any{"id": "fc_" + id, "type": "function_call", "call_id": id, "name": name, "arguments": string(args)}}, tokens)
			return
		}
		text = "DONE"
	}
	readAvailable := false
	tools, _ := payload["tools"].([]any)
	for _, tool := range tools {
		if tool, ok := tool.(map[string]any); ok && tool["name"] == "Read" {
			readAvailable = true
		}
	}
	hasReadResult := bytes.Contains(data, []byte(`"type":"tool_result"`))
	if input, ok := payload["input"].([]any); ok {
		for _, value := range input {
			if value, ok := value.(map[string]any); ok && value["type"] == "function_call_output" {
				hasReadResult = true
			}
		}
	}
	if readAvailable && !hasReadResult && !strings.Contains(plain, "Return only a JSON object") && strings.Contains(lastUserText, "First use Read exactly once") {
		cwd := regexp.MustCompile(`CWD: ([^\n]+)`).FindStringSubmatch(plain)
		if len(cwd) != 2 {
			http.Error(w, "mock找不到临时工作目录", 400)
			return
		}
		args, _ := json.Marshal(map[string]string{"file_path": filepath.Join(cwd[1], "fixture.txt")})
		item := map[string]any{"id": "fc_mock_read", "type": "function_call", "call_id": "call_mock_read", "name": "Read", "arguments": string(args)}
		emitLongMock(w, []any{item}, tokens)
		return
	}
	if readAvailable && hasReadResult && !strings.Contains(plain, "Return only a JSON object") && !strings.Contains(plain, "REMINDER: Do NOT call any tools") {
		text = "READY"
	}
	id := fmt.Sprintf("msg_mock_%d", time.Now().UnixNano())
	emitLongMock(w, []any{map[string]any{"id": id, "type": "message", "status": "completed", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": text, "annotations": []any{}}}}}, tokens)
}

func emitLongMock(w http.ResponseWriter, output []any, tokens int) {
	id := fmt.Sprintf("resp_mock_%d", time.Now().UnixNano())
	emit := func(event map[string]any) {
		data, _ := json.Marshal(event)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event["type"], data)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}
	w.Header().Set("Content-Type", "text/event-stream")
	emit(map[string]any{"type": "response.created", "response": map[string]any{"id": id, "status": "in_progress", "output": []any{}}})
	for index, value := range output {
		item := value.(map[string]any)
		added := map[string]any{}
		for key, value := range item {
			added[key] = value
		}
		if item["type"] == "function_call" {
			added["arguments"] = ""
		} else {
			added["content"] = []any{}
		}
		emit(map[string]any{"type": "response.output_item.added", "output_index": index, "item": added})
		if item["type"] == "function_call" {
			emit(map[string]any{"type": "response.function_call_arguments.delta", "output_index": index, "item_id": item["id"], "delta": item["arguments"]})
			emit(map[string]any{"type": "response.function_call_arguments.done", "output_index": index, "item_id": item["id"], "arguments": item["arguments"]})
		} else {
			for i, part := range item["content"].([]any) {
				part := part.(map[string]any)
				emit(map[string]any{"type": "response.content_part.added", "item_id": item["id"], "output_index": index, "content_index": i, "part": map[string]any{"type": "output_text", "text": "", "annotations": []any{}}})
				emit(map[string]any{"type": "response.output_text.delta", "item_id": item["id"], "output_index": index, "content_index": i, "delta": part["text"]})
			}
		}
		emit(map[string]any{"type": "response.output_item.done", "output_index": index, "item": item})
	}
	emit(map[string]any{"type": "response.completed", "response": map[string]any{"id": id, "object": "response", "status": "completed", "model": "gpt-6.1-sol", "output": output, "usage": map[string]any{"input_tokens": tokens, "output_tokens": 20, "total_tokens": tokens + 20}}})
}

// 原生Anthropic mock完全绕过CPA与SDK，用于区分客户端持久化与协议转换问题。
func longNativeMock(w http.ResponseWriter, r *http.Request) {
	var incoming map[string]any
	if json.NewDecoder(r.Body).Decode(&incoming) != nil {
		http.Error(w, "无效mock请求", 400)
		return
	}
	payload, _ := json.Marshal(map[string]any{"input": incoming["messages"], "system": incoming["system"], "tools": incoming["tools"]})
	recorder := httptest.NewRecorder()
	longMockResponse(recorder, httptest.NewRequest("POST", "/responses", bytes.NewReader(payload)))
	if recorder.Code != 200 {
		w.WriteHeader(recorder.Code)
		w.Write(recorder.Body.Bytes())
		return
	}
	var final map[string]any
	scanner := bufio.NewScanner(bytes.NewReader(recorder.Body.Bytes()))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		line := scanner.Bytes()
		if bytes.HasPrefix(line, []byte("data: ")) {
			var event map[string]any
			json.Unmarshal(line[6:], &event)
			if event["type"] == "response.completed" {
				final, _ = event["response"].(map[string]any)
			}
		}
	}
	if final == nil {
		http.Error(w, "mock缺少最终事件", 500)
		return
	}
	usage := final["usage"].(map[string]any)
	emit := func(name string, value any) {
		data, _ := json.Marshal(value)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, data)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}
	w.Header().Set("Content-Type", "text/event-stream")
	emit("message_start", map[string]any{"type": "message_start", "message": map[string]any{"id": "msg_native_" + fmt.Sprint(final["id"]), "type": "message", "role": "assistant", "model": incoming["model"], "content": []any{}, "stop_reason": nil, "usage": map[string]any{"input_tokens": usage["input_tokens"], "output_tokens": 0}}})
	reason := "end_turn"
	for i, item := range final["output"].([]any) {
		item := item.(map[string]any)
		if item["type"] == "function_call" {
			reason = "tool_use"
			emit("content_block_start", map[string]any{"type": "content_block_start", "index": i, "content_block": map[string]any{"type": "tool_use", "id": item["call_id"], "name": item["name"], "input": map[string]any{}}})
			emit("content_block_delta", map[string]any{"type": "content_block_delta", "index": i, "delta": map[string]any{"type": "input_json_delta", "partial_json": item["arguments"]}})
		} else {
			emit("content_block_start", map[string]any{"type": "content_block_start", "index": i, "content_block": map[string]any{"type": "text", "text": ""}})
			for _, part := range item["content"].([]any) {
				part := part.(map[string]any)
				emit("content_block_delta", map[string]any{"type": "content_block_delta", "index": i, "delta": map[string]any{"type": "text_delta", "text": part["text"]}})
			}
		}
		emit("content_block_stop", map[string]any{"type": "content_block_stop", "index": i})
	}
	emit("message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": reason, "stop_sequence": nil}, "usage": map[string]any{"output_tokens": usage["output_tokens"]}})
	emit("message_stop", map[string]any{"type": "message_stop"})
}
