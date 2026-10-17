package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/shichao-wang/cpa/internal/agent/claudecode"
	"github.com/shichao-wang/cpa/internal/config"
	"github.com/shichao-wang/cpa/internal/store"
	"github.com/sirupsen/logrus"
)

// 真实请求必须显式启用；观测只保留参数、状态及 usage，绝不保存授权 header。
type compatRealTransport struct {
	base         http.RoundTripper
	count        atomic.Int32
	failed       atomic.Bool
	mu           sync.Mutex
	observations []map[string]any
	inbound      []map[string]any
	currentCase  string
	ledger       string
}

func (r *compatRealTransport) RoundTripperFor(*coreauth.Auth) http.RoundTripper { return r }
func (r *compatRealTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if r.failed.Load() {
		return nil, errors.New("真实测试已熔断")
	}
	if req.URL.Scheme != "https" || req.URL.Host != "chatgpt.com" || req.URL.Path != "/backend-api/codex/responses" || req.Method != "POST" {
		r.failed.Store(true)
		return nil, errors.New("真实测试拒绝非官方推理入口")
	}
	index, err := r.reserveBudget()
	if err != nil {
		r.failed.Store(true)
		return nil, err
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, errors.New("读取真实测试请求失败")
	}
	req.Body.Close()
	var payload map[string]any
	if json.Unmarshal(body, &payload) != nil {
		return nil, errors.New("真实测试请求不是JSON")
	}
	req.Body = io.NopCloser(bytes.NewReader(body))
	r.mu.Lock()
	name := r.currentCase
	r.mu.Unlock()
	toolResults := 0
	if input, ok := payload["input"].([]any); ok {
		for _, item := range input {
			if object, ok := item.(map[string]any); ok && object["type"] == "function_call_output" {
				toolResults++
			}
		}
	}
	availableTools := []string{}
	if tools, ok := payload["tools"].([]any); ok {
		for _, tool := range tools {
			if object, ok := tool.(map[string]any); ok {
				if name, ok := object["name"].(string); ok {
					availableTools = append(availableTools, name)
				}
			}
		}
	}
	entry := map[string]any{"available_tools": availableTools, "tool_results": toolResults, "case": name, "index": index, "model": payload["model"], "reasoning": payload["reasoning"], "stream": payload["stream"]}
	started := time.Now()
	ctx, cancel := context.WithTimeout(req.Context(), 45*time.Second)
	req = req.Clone(ctx)
	response, err := r.base.RoundTrip(req)
	entry["headers_ms"] = time.Since(started).Milliseconds()
	if err != nil {
		cancel()
		r.failed.Store(true)
		entry["transport_error"] = true
		r.add(entry)
		return nil, errors.New("真实上游请求失败，测试已熔断")
	}
	entry["status"] = response.StatusCode
	if response.StatusCode != http.StatusOK {
		// 非200返回不交给SDK重试/刷新。实际状态仅作为本次短请求证据。
		response.Body.Close()
		cancel()
		r.failed.Store(true)
		r.add(entry)
		return nil, errors.New("真实上游返回非200，测试已熔断且禁止刷新")
	}
	response.Body = &compatRealBody{ReadCloser: response.Body, cancel: cancel, transport: r, entry: entry, started: started}
	return response, nil
}
func (r *compatRealTransport) add(entry map[string]any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.observations = append(r.observations, entry)
}

type compatRealBody struct {
	io.ReadCloser
	cancel    context.CancelFunc
	transport *compatRealTransport
	entry     map[string]any
	started   time.Time
	pending   []byte
	once      sync.Once
}

func (b *compatRealBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.pending = append(b.pending, p[:n]...)
	for {
		i := bytes.IndexByte(b.pending, '\n')
		if i < 0 {
			break
		}
		line := b.pending[:i]
		if bytes.HasPrefix(line, []byte("data: ")) {
			var e struct {
				Type string `json:"type"`
				Item struct {
					Type string `json:"type"`
					Name string `json:"name"`
				} `json:"item"`
				Response struct {
					Usage  json.RawMessage `json:"usage"`
					Status string          `json:"status"`
					Output []struct {
						Type string `json:"type"`
						Name string `json:"name"`
					} `json:"output"`
				} `json:"response"`
			}
			if json.Unmarshal(line[6:], &e) == nil && e.Type == "response.output_item.done" && e.Item.Type == "function_call" {
				tools, _ := b.entry["tool_calls"].([]string)
				b.entry["tool_calls"] = append(tools, e.Item.Name)
			}
			if json.Unmarshal(line[6:], &e) == nil && e.Type == "response.completed" {
				b.entry["usage"] = compatRealUsage(e.Response.Usage)
				b.entry["response_status"] = e.Response.Status
				tools, _ := b.entry["tool_calls"].([]string)
				for _, item := range e.Response.Output {
					if item.Type == "function_call" {
						tools = append(tools, item.Name)
					}
				}
				b.entry["tool_calls"] = tools
			}
		}
		b.pending = b.pending[i+1:]
	}
	if len(b.pending) > 65536 {
		b.pending = nil
	}
	return n, err
}
func (b *compatRealBody) Close() error {
	err := b.ReadCloser.Close()
	b.once.Do(func() {
		b.cancel()
		b.entry["duration_ms"] = time.Since(b.started).Milliseconds()
		b.transport.add(b.entry)
	})
	return err
}

func TestCompatRealCodexClaude(t *testing.T) {
	if os.Getenv("CPA_COMPAT_REAL") != "1" {
		t.Skip("仅显式授权后设置 CPA_COMPAT_REAL=1 执行真实短请求")
	}
	previousLogLevel := logrus.GetLevel()
	logrus.SetLevel(logrus.PanicLevel)
	t.Cleanup(func() { logrus.SetLevel(previousLogLevel) })
	sourcePath := os.Getenv("CPA_COMPAT_REAL_STATE_DIR")
	if sourcePath == "" {
		t.Fatal("必须指定只读来源状态目录")
	}
	sourceFile := filepath.Join(sourcePath, "state.json")
	before, err := os.ReadFile(sourceFile)
	if err != nil {
		t.Fatal("读取来源状态失败")
	}
	beforeHash := sha256.Sum256(before)
	t.Cleanup(func() {
		after, err := os.ReadFile(sourceFile)
		if err != nil || sha256.Sum256(after) != beforeHash {
			t.Error("来源状态发生变化")
		}
	})
	source, err := store.New(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	original, err := source.Read()
	if err != nil {
		t.Fatal(err)
	}
	connection, ok := original.Connections["codex"]
	if !ok || connection.AuthType != config.AuthCodexOAuth || connection.BaseURL != config.CodexBaseURL {
		t.Fatal("缺少官方codex OAuth连接")
	}
	credential := original.OAuthCredentials[connection.CredentialRef]
	if credential.ExpiresAt.Before(time.Now().Add(26 * time.Hour)) {
		t.Fatal("凭证临近刷新窗口，拒绝执行真实测试")
	}
	modelFound := false
	for _, model := range connection.Models {
		if model.ID == "gpt-6.1-sol" {
			modelFound = true
		}
	}
	if !modelFound {
		t.Fatal("连接未选择gpt-6.1-sol")
	}
	root := t.TempDir()
	st, err := store.New(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	listen, err := reserveLoopbackAddress(t)
	if err != nil {
		t.Fatal(err)
	}
	state := config.NewState()
	state.Listen = listen
	connection.Models = []config.Model{{ID: "gpt-6.1-sol"}}
	state.Connections[connection.Name] = connection
	state.OAuthCredentials[connection.CredentialRef] = credential
	key, err := config.RandomID()
	if err != nil {
		t.Fatal(err)
	}
	profile := config.Profile{Name: "compat-real", ID: "compat-real-profile", Agent: "claude-code", KeyRef: "compat-real-key", Models: map[string]config.Binding{}}
	for slot, public := range map[string]string{"opus": "claude-opus-4-6", "sonnet": "claude-sonnet-4-6", "haiku": "claude-haiku-4-5-20251001"} {
		profile.Models[slot] = config.Binding{PublicModel: public, Connection: connection.Name, TargetModel: "gpt-6.1-sol", Label: slot}
	}
	state.Profiles[profile.Name] = profile
	state.Secrets[profile.KeyRef] = key
	if err = st.Update(func(s *config.State) error { *s = *state; return nil }); err != nil {
		t.Fatal(err)
	}
	transport := &compatRealTransport{base: &http.Transport{Proxy: nil, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 35 * time.Second}}
	usedBefore := 0
	if value := os.Getenv("CPA_COMPAT_REAL_USED"); value != "" {
		usedBefore, err = strconv.Atoi(value)
		if err != nil || usedBefore < 0 || usedBefore >= 30 {
			t.Fatal("已消耗请求数必须位于0到29")
		}
	}
	transport.count.Store(int32(usedBefore))
	transport.ledger = os.Getenv("CPA_COMPAT_REAL_LEDGER")
	if transport.ledger != "" {
		err = store.WithLock(transport.ledger+".lock", func() error {
			data, readErr := os.ReadFile(transport.ledger)
			if os.IsNotExist(readErr) {
				return store.WriteJSON(transport.ledger, map[string]int{"used": usedBefore})
			}
			if readErr != nil {
				return errors.New("读取真实预算ledger失败")
			}
			var value struct {
				Used int `json:"used"`
			}
			if json.Unmarshal(data, &value) != nil || value.Used != usedBefore || value.Used >= 30 {
				return errors.New("真实预算ledger无效或耗尽")
			}
			usedBefore = value.Used
			transport.count.Store(int32(usedBefore))
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	manager := startManagedTest(t, st, runtimeOptions{transport: transport})
	manager.StopAutoRefresh()
	manager.SetRetryConfig(0, 0, 1)
	evidence := map[string]any{"model": "gpt-6.1-sol", "request_limit": 30, "cases": []map[string]any{}, "official_only": true, "test_circuit_breaker": true}
	results := []map[string]any{}
	output := os.Getenv("CPA_COMPAT_REAL_OUTPUT")
	if output == "" {
		output = filepath.Join(os.TempDir(), "cpa-compat-real.json")
	}
	t.Cleanup(func() {
		transport.mu.Lock()
		evidence["requests"] = transport.observations
		evidence["inbound_parameters"] = transport.inbound
		transport.mu.Unlock()
		evidence["cases"] = results
		evidence["actual_requests"] = transport.count.Load() - int32(usedBefore)
		evidence["previous_requests"] = usedBefore
		evidence["global_requests"] = transport.count.Load()
		data, _ := json.MarshalIndent(evidence, "", "  ")
		if err := os.WriteFile(output, append(data, '\n'), 0600); err != nil {
			t.Error("写脱敏真实测试证据失败")
		}
		t.Logf("真实短请求数=%d，证据=%s", transport.count.Load(), output)
	})
	claude, err := exec.LookPath("claude")
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "home")
	work := filepath.Join(root, "work")
	for _, dir := range []string{home, work} {
		if err = os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	settings := filepath.Join(home, "settings.json")
	if err = os.WriteFile(settings, []byte(`{"language":"English"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err = claudecode.Apply(st, profile, settings); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := claudecode.Restore(st, settings); err != nil {
			t.Error("临时真实测试settings恢复失败")
		}
	})
	versionCommand := exec.Command(claude, "--version")
	versionCommand.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "CLAUDE_CONFIG_DIR=" + home}
	version, err := versionCommand.Output()
	if err != nil {
		t.Fatal("读取Claude版本失败")
	}
	evidence["claude_version"] = strings.TrimSpace(string(version))
	gatewayURL, _ := url.Parse("http://" + listen)
	observerProxy := httputil.NewSingleHostReverseProxy(gatewayURL)
	observerProxy.Transport = &http.Transport{Proxy: nil}
	observer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.URL.Path == "/v1/messages" {
			data, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, "测试请求读取失败", 400)
				return
			}
			r.Body.Close()
			r.Body = io.NopCloser(bytes.NewReader(data))
			var payload map[string]any
			if json.Unmarshal(data, &payload) == nil {
				transport.mu.Lock()
				transport.inbound = append(transport.inbound, map[string]any{"case": transport.currentCase, "model": payload["model"], "thinking": payload["thinking"], "output_config": payload["output_config"]})
				transport.mu.Unlock()
			}
		}
		observerProxy.ServeHTTP(w, r)
	}))
	t.Cleanup(observer.Close)
	cases := []struct{ name, effort, settings, prompt, tools, mode, schema, agents string }{
		{name: "default", prompt: "Reply exactly CPA_REAL_OK. No tools.", tools: ""},
		{name: "low", effort: "low", prompt: "Reply exactly CPA_REAL_OK. No tools.", tools: ""},
		{name: "high", effort: "high", prompt: "Reply exactly CPA_REAL_OK. No tools.", tools: ""},
		{name: "thinking-disabled", settings: `{"alwaysThinkingEnabled":false}`, prompt: "Reply exactly CPA_REAL_OK. No tools.", tools: ""},
		{name: "read-tool", prompt: "Use Read to read fixture.txt, then reply exactly its contents. Do not use any other tool.", tools: "Read"},
		{name: "plan-read", mode: "plan", prompt: "In plan mode, use Read to inspect fixture.txt. Do not edit or run commands. Then reply exactly its contents.", tools: "Read"},
		{name: "bash-tool", prompt: "Use Bash once to execute printf CPA_REAL_OK, then reply CPA_REAL_OK. Do not use any other tool.", tools: "Bash"},
		{name: "structured-json", schema: `{"type":"object","properties":{"marker":{"type":"string","const":"CPA_REAL_OK"}},"required":["marker"],"additionalProperties":false}`, prompt: "Return an object with marker CPA_REAL_OK matching the given JSON schema.", tools: ""},
		{name: "subagent", agents: `{"checker":{"description":"Reads the isolated fixture","prompt":"Use Read to inspect fixture.txt and report the exact contents. Do not use other tools.","tools":["Read"],"model":"haiku"}}`, prompt: "You must use Agent to delegate to checker once to read fixture.txt. Do not read the fixture yourself. Wait for checker to return, then reply with exactly the fixture contents.", tools: "Agent,Read"},
		{name: "image-read", prompt: "Use Read once to inspect fixture.png. If the picture is solid red, reply exactly CPA_REAL_OK; otherwise reply WRONG_COLOR. Do not inspect any other files.", tools: "Read"},
		{name: "cache-repeat-1", prompt: "Reply exactly CPA_REAL_OK. No tools.", tools: ""},
		{name: "cache-repeat-2", prompt: "Reply exactly CPA_REAL_OK. No tools.", tools: ""},
	}
	if err = os.WriteFile(filepath.Join(work, "fixture.txt"), []byte("CPA_REAL_OK\n"), 0600); err != nil {
		t.Fatal(err)
	}
	imageFixture := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for x := 0; x < 64; x++ {
		for y := 0; y < 64; y++ {
			imageFixture.Set(x, y, color.RGBA{R: 255, A: 255})
		}
	}
	var fixturePNG bytes.Buffer
	if err = png.Encode(&fixturePNG, imageFixture); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(work, "fixture.png"), fixturePNG.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	filter := os.Getenv("CPA_COMPAT_REAL_CASES")
	for _, c := range cases {
		if filter != "" && !strings.Contains(","+filter+",", ","+c.name+",") {
			continue
		}
		if transport.failed.Load() {
			break
		}
		t.Run(c.name, func(t *testing.T) {
			transport.mu.Lock()
			transport.currentCase = c.name
			transport.mu.Unlock()
			startCount := transport.count.Load()
			runSettings := filepath.Join(root, "case-settings.json")
			settingsData := c.settings
			if settingsData == "" {
				settingsData = "{}"
			}
			if err = os.WriteFile(runSettings, []byte(settingsData), 0600); err != nil {
				t.Fatal(err)
			}
			args := []string{"--bare", "--setting-sources", "", "--settings", runSettings, "--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`, "--tools", c.tools, "--no-session-persistence", "--model", "sonnet", "--output-format", "json", "--max-turns", "3", "--permission-mode", "dontAsk", "-p", c.prompt}
			if c.name == "subagent" {
				if index := slices.Index(args, "--bare"); index >= 0 {
					args = slices.Delete(args, index, index+1)
				}
			}
			if c.schema != "" {
				args = append(args, "--json-schema", c.schema)
			}
			if c.agents != "" {
				args = append(args, "--agents", c.agents)
			}
			if c.mode != "" {
				args = append(args, "--permission-mode", c.mode)
			}
			if c.tools != "" {
				args = append(args, "--allowedTools", c.tools)
			}
			if c.effort != "" {
				args = append(args, "--effort", c.effort)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, claude, args...)
			command.Dir = work
			command.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "TMPDIR=" + root, "CLAUDE_CONFIG_DIR=" + home, "DISABLE_TELEMETRY=1", "DISABLE_ERROR_REPORTING=1", "DISABLE_AUTOUPDATER=1", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1", "CLAUDE_CODE_MAX_OUTPUT_TOKENS=1024", "ANTHROPIC_BASE_URL=" + observer.URL, "ANTHROPIC_API_KEY=" + key, "ANTHROPIC_DEFAULT_SONNET_MODEL=claude-sonnet-4-6", "ANTHROPIC_DEFAULT_HAIKU_MODEL=claude-haiku-4-5-20251001", "NO_PROXY=127.0.0.1,localhost"}
			if c.name == "subagent" {
				for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"} {
					command.Env = append(command.Env, name+"=http://127.0.0.1:1")
				}
				command.Env = append(command.Env, "no_proxy=127.0.0.1,localhost,::1")
			}
			started := time.Now()
			data, runErr := command.Output()
			var result struct {
				Result     string          `json:"result"`
				IsError    bool            `json:"is_error"`
				Usage      json.RawMessage `json:"usage"`
				Subtype    string          `json:"subtype"`
				Structured map[string]any  `json:"structured_output"`
			}
			decodeErr := json.Unmarshal(data, &result)
			marker := strings.Contains(result.Result, "CPA_REAL_OK")
			if c.schema != "" {
				marker = result.Structured["marker"] == "CPA_REAL_OK"
			}
			passed := runErr == nil && decodeErr == nil && !result.IsError && marker
			toolCalls := []string{}
			toolResults := 0
			childRouted := false
			transport.mu.Lock()
			for _, observed := range transport.observations {
				if observed["case"] == c.name {
					if calls, ok := observed["tool_calls"].([]string); ok {
						toolCalls = append(toolCalls, calls...)
					}
					if count, ok := observed["tool_results"].(int); ok {
						toolResults += count
					}
				}
			}
			for _, incoming := range transport.inbound {
				if incoming["case"] == c.name && incoming["model"] == "claude-haiku-4-5-20251001" {
					childRouted = true
				}
			}
			transport.mu.Unlock()
			if c.tools != "" && (len(toolCalls) == 0 || toolResults == 0) {
				passed = false
			}
			if c.name == "subagent" && (!slices.Contains(toolCalls, "Agent") || !childRouted || transport.count.Load()-startCount < 4) {
				passed = false
			}
			entry := map[string]any{"child_route_verified": childRouted, "tool_calls": toolCalls, "tool_results": toolResults, "name": c.name, "passed": passed, "marker_received": marker, "usage": compatRealUsage(result.Usage), "subtype": result.Subtype, "duration_ms": time.Since(started).Milliseconds(), "actual_requests": transport.count.Load() - startCount}
			results = append(results, entry)
			t.Logf("case=%s，已累计真实请求=%d/30，本case=%d", c.name, transport.count.Load(), transport.count.Load()-startCount)
			if !passed {
				t.Errorf("真实Claude短请求失败，case=%s；仅保存脱敏结果", c.name)
			}
		})
	}
	if !transport.failed.Load() && strings.Contains(","+filter+",", ",direct-image,") {
		t.Run("direct-image", func(t *testing.T) {
			transport.mu.Lock()
			transport.currentCase = "direct-image"
			transport.mu.Unlock()
			startCount := transport.count.Load()
			picture := image.NewRGBA(image.Rect(0, 0, 64, 64))
			for x := 0; x < 64; x++ {
				for y := 0; y < 64; y++ {
					picture.Set(x, y, color.RGBA{R: 255, A: 255})
				}
			}
			var encoded bytes.Buffer
			if err := png.Encode(&encoded, picture); err != nil {
				t.Fatal(err)
			}
			payload := map[string]any{"model": "claude-sonnet-4-6", "max_tokens": 128, "stream": false, "messages": []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": base64.StdEncoding.EncodeToString(encoded.Bytes())}}, map[string]any{"type": "text", "text": "If the image is solid red, reply exactly CPA_REAL_OK. Otherwise reply WRONG_COLOR."}}}}}
			data, _ := json.Marshal(payload)
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			request, _ := http.NewRequestWithContext(ctx, "POST", "http://"+listen+"/v1/messages", bytes.NewReader(data))
			request.Header.Set("X-Api-Key", key)
			request.Header.Set("Content-Type", "application/json")
			client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 60 * time.Second}
			defer client.CloseIdleConnections()
			response, err := client.Do(request)
			entry := map[string]any{"name": "direct-image", "source": "direct-api", "passed": false}
			if err == nil {
				defer response.Body.Close()
				var answer struct {
					Content []struct {
						Text string `json:"text"`
					} `json:"content"`
					Usage json.RawMessage `json:"usage"`
				}
				if json.NewDecoder(response.Body).Decode(&answer) == nil {
					for _, part := range answer.Content {
						if strings.Contains(part.Text, "CPA_REAL_OK") {
							entry["passed"] = response.StatusCode == 200
						}
					}
					entry["usage"] = compatRealUsage(answer.Usage)
				}
				entry["status"] = response.StatusCode
			}
			entry["actual_requests"] = transport.count.Load() - startCount
			results = append(results, entry)
			t.Logf("direct-image累计请求=%d/30", transport.count.Load())
			if entry["passed"] != true {
				t.Error("真实合成图片API测试失败，见脱敏证据")
			}
		})
	}

	if transport.count.Load() > 30 {
		t.Error("请求预算超限")
	}
	if transport.failed.Load() {
		t.Error("真实测试触发熔断，后续case未执行")
	}
}

func TestCompatRealTransportGuard(t *testing.T) {
	guard := &compatRealTransport{}
	req, _ := http.NewRequest("POST", "https://example.com/responses", strings.NewReader("{}"))
	if _, err := guard.RoundTrip(req); err == nil {
		t.Fatal("未知地址未拒绝")
	}
	if guard.count.Load() != 0 {
		t.Fatal("未知地址不得消耗网络请求")
	}
	guard = &compatRealTransport{}
	guard.count.Store(30)
	req, _ = http.NewRequest("POST", "https://chatgpt.com/backend-api/codex/responses", strings.NewReader("{}"))
	if _, err := guard.RoundTrip(req); err == nil || !guard.failed.Load() {
		t.Fatal("预算耗尽未熔断")
	}
}

// 仅保留数值usage，不保存服务端的逐消息归因ID或未知字段。
func compatRealUsage(raw json.RawMessage) map[string]any {
	var value map[string]any
	if json.Unmarshal(raw, &value) != nil {
		return nil
	}
	selected := map[string]any{}
	for _, key := range []string{"input_tokens", "output_tokens", "total_tokens", "cache_read_input_tokens", "cache_creation_input_tokens"} {
		if number, ok := value[key].(float64); ok {
			selected[key] = number
		}
	}
	for _, key := range []string{"input_tokens_details", "output_tokens_details"} {
		if detail, ok := value[key].(map[string]any); ok {
			numbers := map[string]any{}
			for name, number := range detail {
				if numeric, ok := number.(float64); ok {
					numbers[name] = numeric
				}
			}
			selected[key] = numbers
		}
	}
	return selected
}

func TestCompatRealToolObservation(t *testing.T) {
	transport := &compatRealTransport{}
	event := `data: {"type":"response.output_item.done","item":{"type":"function_call","name":"Read"}}` + "\n\n" + `data: {"type":"response.completed","response":{"status":"completed","output":[],"usage":{"input_tokens":5,"output_tokens":2,"attribution":{"account":"must-not-save"}}}}` + "\n\n"
	body := &compatRealBody{ReadCloser: io.NopCloser(strings.NewReader(event)), cancel: func() {}, transport: transport, entry: map[string]any{}, started: time.Now()}
	if _, err := io.Copy(io.Discard, body); err != nil {
		t.Fatal(err)
	}
	body.Close()
	calls, _ := transport.observations[0]["tool_calls"].([]string)
	if len(calls) != 1 || calls[0] != "Read" {
		t.Fatal("未记录真实格式output_item.done工具调用")
	}
	encoded, _ := json.Marshal(transport.observations)
	if bytes.Contains(encoded, []byte("must-not-save")) {
		t.Fatal("usage记录包含非白名单字段")
	}
}

func (r *compatRealTransport) reserveBudget() (int32, error) {
	if r.ledger != "" {
		var next int32
		err := store.WithLock(r.ledger+".lock", func() error {
			data, err := os.ReadFile(r.ledger)
			if err != nil {
				return errors.New("读取真实预算失败")
			}
			var value struct {
				Used int32 `json:"used"`
			}
			if json.Unmarshal(data, &value) != nil || value.Used < 0 || value.Used >= 30 {
				return errors.New("真实测试请求预算已耗尽")
			}
			next = value.Used + 1
			if err := store.WriteJSON(r.ledger, map[string]int32{"used": next}); err != nil {
				return errors.New("写入真实预算失败")
			}
			r.count.Store(next)
			return nil
		})
		return next, err
	}
	for {
		previous := r.count.Load()
		if previous >= 30 {
			return 0, errors.New("真实测试请求预算已耗尽")
		}
		if r.count.CompareAndSwap(previous, previous+1) {
			return previous + 1, nil
		}
	}
}

func TestCompatRealBudgetLedger(t *testing.T) {
	path := filepath.Join(t.TempDir(), "budget.json")
	if err := store.WriteJSON(path, map[string]int{"used": 29}); err != nil {
		t.Fatal(err)
	}
	guard := &compatRealTransport{ledger: path}
	if next, err := guard.reserveBudget(); err != nil || next != 30 {
		t.Fatal("跨运行最后一个预算未正确计数")
	}
	if _, err := guard.reserveBudget(); err == nil {
		t.Fatal("跨运行预算突破30")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var value struct {
		Used int `json:"used"`
	}
	json.Unmarshal(data, &value)
	if value.Used != 30 {
		t.Fatal("预算耗尽后ledger被错误递增")
	}
}
