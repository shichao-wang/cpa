package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shichao-wang/cpa/internal/config"
	"github.com/shichao-wang/cpa/internal/store"
)

// TestWebUIThroughTunnel 使用真实 binary 和 TCP 转发验证 SSH 隧道的不同端口语义。
// 不执行真实 SSH、不连接账户、不发送真实模型请求。
func TestWebUIThroughTunnel(t *testing.T) {
	if testing.Short() {
		t.Skip("短测试模式跳过子进程验收")
	}
	tmp := t.TempDir()
	binary := filepath.Join(tmp, "cpa")
	dir := filepath.Join(tmp, "state")
	build := exec.Command("go", "build", "-o", binary, "./cmd/cpa")
	build.Dir = filepath.Join("..", "..")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("构建失败：%v %s", err, out)
	}
	call := func(args ...string) string {
		t.Helper()
		out, err := exec.Command(binary, append([]string{"--state-dir", dir}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("命令 %v 失败：%v", args, err)
		}
		return strings.TrimSpace(string(out))
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	call("server", "start", "--listen", address)
	stopped := false
	t.Cleanup(func() {
		if !stopped {
			_ = exec.Command(binary, "--state-dir", dir, "server", "stop").Run()
		}
	})
	token := call("server", "admin-token")
	// 隧道只转发 TCP 字节，不改写 Host、Origin 或认证头。
	tunnel, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tunnel.Close()
	var forwards sync.WaitGroup
	acceptDone := make(chan struct{})
	go func() {
		defer close(acceptDone)
		for {
			client, err := tunnel.Accept()
			if err != nil {
				return
			}
			forwards.Go(func() {
				defer client.Close()
				server, err := net.DialTimeout("tcp", address, time.Second)
				if err != nil {
					return
				}
				defer server.Close()
				done := make(chan struct{})
				go func() {
					_, _ = io.Copy(server, client)
					if c, ok := server.(*net.TCPConn); ok {
						_ = c.CloseWrite()
					}
					close(done)
				}()
				_, _ = io.Copy(client, server)
				_ = client.Close()
				<-done
			})
		}
	}()
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}}
	defer client.CloseIdleConnections()
	base := "http://" + tunnel.Addr().String()
	request := func(method, path, key string, body any, origin string) (int, []byte) {
		t.Helper()
		var data []byte
		if body != nil {
			data, _ = json.Marshal(body)
		}
		req, _ := http.NewRequest(method, base+path, bytes.NewReader(data))
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, err = io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, data
	}
	code, page := request("GET", "/ui/", "", nil, "")
	if code != 200 || !bytes.Contains(page, []byte("网关管理")) {
		t.Fatal("同端口页面不可用")
	}
	for _, asset := range []string{"app.js", "style.css"} {
		code, _ = request("GET", "/ui/"+asset, "", nil, "")
		if code != 200 {
			t.Fatal("静态资源不可用")
		}
	}
	code, _ = request("GET", "/admin/api/state", "", nil, "")
	if code != 200 {
		t.Fatal("监听 127.0.0.1 时无需管理令牌")
	}
	code, _ = request("GET", "/v1/models", token, nil, "")
	if code != 401 {
		t.Fatal("管理令牌不能用于模型请求")
	}
	var inferenceCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/models" && r.Header.Get("X-Api-Key") == "upstream-mock-key" {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"data":[{"id":"actual","display_name":"测试模型"},{"id":"unselected","display_name":"未选模型"}],"has_more":false}`)
			return
		}
		inferenceCalls.Add(1)
		if r.URL.Path != "/v1/messages" || (r.Header.Get("X-Api-Key") != "upstream-mock-key" && r.Header.Get("Authorization") != "Bearer upstream-mock-key") {
			t.Error("上游连接路由或认证错误")
			w.WriteHeader(401)
			return
		}
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"msg_mock","type":"message","role":"assistant","model":"actual","content":[{"type":"text","text":"mock"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer upstream.Close()
	connectionBody := map[string]any{"name": "remote", "protocol": "anthropic-messages", "baseURL": upstream.URL, "models": []config.Model{{ID: "actual"}}, "apiKey": "upstream-mock-key"}
	code, data := request("GET", "/admin/api/state", token, nil, base)
	var beforeDiscovery struct {
		Revision uint64 `json:"revision"`
	}
	if code != 200 || json.Unmarshal(data, &beforeDiscovery) != nil {
		t.Fatal("无法读取测试前的修订")
	}
	code, data = request("POST", "/admin/api/connections/test", token, map[string]any{"protocol": "anthropic-messages", "baseURL": upstream.URL, "apiKey": "upstream-mock-key"}, base)
	if code != 200 || !bytes.Contains(data, []byte(`"id":"actual"`)) || !bytes.Contains(data, []byte(`"id":"unselected"`)) || bytes.Contains(data, []byte("upstream-mock-key")) {
		t.Fatalf("通过隧道获取模型目录失败：%d", code)
	}
	code, data = request("GET", "/admin/api/state", token, nil, base)
	var afterDiscovery struct {
		Revision uint64 `json:"revision"`
	}
	if code != 200 || json.Unmarshal(data, &afterDiscovery) != nil || afterDiscovery.Revision != beforeDiscovery.Revision || !bytes.Contains(data, []byte(`"connections":{}`)) {
		t.Fatal("测试连接不得提前保存配置")
	}
	code, data = request("POST", "/admin/api/connections", token, connectionBody, "http://evil.example")
	if code != 403 {
		t.Fatal("跨来源写入必须拒绝")
	}
	code, data = request("POST", "/admin/api/connections", token, connectionBody, base)
	if code != 200 || !bytes.Contains(data, []byte(`"active":true`)) {
		t.Fatalf("连接未实际加载：%d", code)
	}
	models := map[string]config.Binding{}
	for _, slot := range config.Slots {
		models[slot] = config.Binding{PublicModel: "claude-" + slot, Connection: "remote", TargetModel: "actual"}
	}
	invalidModels := map[string]config.Binding{}
	for slot, binding := range models {
		invalidModels[slot] = binding
	}
	invalidModels["sonnet"] = config.Binding{PublicModel: "claude-sonnet", Connection: "remote", TargetModel: "unselected"}
	code, _ = request("POST", "/admin/api/profiles", token, map[string]any{"name": "forbidden", "models": invalidModels}, base)
	if code != 409 {
		t.Fatal("未选中的模型不得通过管理接口绑定到 profile")
	}
	code, data = request("POST", "/admin/api/profiles", token, map[string]any{"name": "daily", "models": models}, base)
	if code != 200 || !bytes.Contains(data, []byte(`"active":true`)) {
		t.Fatalf("profile 未实际加载：%d", code)
	}
	var created struct {
		ProfileKey string `json:"profileKey"`
	}
	if err := json.Unmarshal(data, &created); err != nil || config.ValidateKey(created.ProfileKey) != nil {
		t.Fatal("创建响应必须提供一次下游 key")
	}
	st, err := store.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	s, err := st.Read()
	if err != nil {
		t.Fatal(err)
	}
	key := created.ProfileKey
	if key != s.Secrets[s.Profiles["daily"].KeyRef] {
		t.Fatal("返回 key 与持久化 key 不符")
	}
	code, data = request("GET", "/admin/api/state", token, nil, base)
	if code != 200 || bytes.Contains(data, []byte(key)) || bytes.Contains(data, []byte("upstream-mock-key")) {
		t.Fatal("管理状态不得泄漏凭证")
	}
	if bytes.Contains(data, []byte("unselected")) {
		t.Fatal("未选模型不得被保存到连接或 profile")
	}
	code, _ = request("GET", "/admin/api/state", key, nil, base)
	if code != 401 {
		t.Fatal("profile key 不能用于管理")
	}
	code, data = request("GET", "/v1/models", key, nil, base)
	if code != 200 || bytes.Contains(data, []byte("unselected")) {
		t.Fatal("下游模型目录不得暴露未选模型")
	}
	beforeCalls := inferenceCalls.Load()
	for _, path := range []string{"/v1/messages", "/v1/messages/count_tokens"} {
		code, _ = request("POST", path, key, map[string]any{"model": "unselected", "max_tokens": 8, "messages": []map[string]string{{"role": "user", "content": "mock"}}}, base)
		if code != 400 || inferenceCalls.Load() != beforeCalls {
			t.Fatal("未选模型必须在网关边界被拒绝，不能转发到上游")
		}
	}
	code, data = request("POST", "/v1/messages", key, map[string]any{"model": "claude-sonnet", "max_tokens": 8, "messages": []map[string]string{{"role": "user", "content": "mock"}}}, base)
	if code != 200 || !bytes.Contains(data, []byte(`"model":"claude-sonnet"`)) {
		t.Fatalf("WebUI 保存后模型路由不可用：%d", code)
	}
	models["sonnet"] = config.Binding{PublicModel: "claude-sonnet", Connection: "remote", TargetModel: "actual", Label: "远端修改"}
	code, data = request("PATCH", "/admin/api/profiles/daily", token, map[string]any{"models": models}, base)
	if code != 200 {
		t.Fatalf("更新失败：%d %s", code, data)
	}
	code, data = request("GET", "/v1/models", key, nil, base)
	if code != 200 || !bytes.Contains(data, []byte("远端修改")) {
		t.Fatalf("修改未对模型服务生效：HTTP %d，响应 %s",code,data)
	}
	code, _ = request("DELETE", "/admin/api/connections/remote", token, nil, base)
	if code != 409 {
		t.Fatal("不能删除被引用连接")
	}
	code, _ = request("DELETE", "/admin/api/profiles/daily", token, nil, base)
	if code != 200 {
		t.Fatal("profile 删除失败")
	}
	code, _ = request("DELETE", "/admin/api/connections/remote", token, nil, base)
	if code != 200 {
		t.Fatal("连接删除失败")
	}
	logData, err := os.ReadFile(st.Path("server.log"))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{token, key, "upstream-mock-key"} {
		if bytes.Contains(logData, []byte(secret)) {
			t.Fatal("服务日志不得记录凭证")
		}
	}
	call("server", "stop")
	stopped = true
	conn, err := net.DialTimeout("tcp", address, 300*time.Millisecond)
	if err == nil {
		conn.Close()
		t.Fatal("停止后 WebUI 和模型 API 必须同时关闭")
	}
	_ = tunnel.Close()
	<-acceptDone
	forwards.Wait()
}
