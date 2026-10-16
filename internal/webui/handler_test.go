package webui

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/shichao-wang/cpa/internal/agent/claudecode"
	"github.com/shichao-wang/cpa/internal/config"
	"github.com/shichao-wang/cpa/internal/connection"
	"github.com/shichao-wang/cpa/internal/store"
)

func setup(t *testing.T) (*store.Store, *Handler, string) {
	t.Helper()
	st, err := store.New(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	h, err := New(st, func() (uint64, bool) {
		s, err := st.Read()
		if err != nil {
			return 0, false
		}
		return s.Revision, true
	})
	if err != nil {
		t.Fatal(err)
	}
	token, err := Token(st)
	if err != nil {
		t.Fatal(err)
	}
	return st, h, token
}
func request(t *testing.T, h http.Handler, method, path, token, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, "http://localhost:18317"+path, strings.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func TestManagementBoundary(t *testing.T) {
	st, h, token := setup(t)
	cases := []struct {
		name, method, path, key, body string
		headers                       map[string]string
		want                          int
	}{
		{name: "公开页面", method: "GET", path: "/ui/", want: 200},
		{name: "脚本", method: "GET", path: "/ui/app.js", want: 200},
		{name: "样式", method: "GET", path: "/ui/style.css", want: 200},
		{name: "页面不能写入", method: "POST", path: "/ui/", want: 405},
		{name: "缺少管理令牌", method: "GET", path: "/admin/api/state", want: 401},
		{name: "profile key 不能管理", method: "GET", path: "/admin/api/state", key: "cpa_profile_test", want: 401},
		{name: "正确管理令牌", method: "GET", path: "/admin/api/state", key: token, want: 200},
		{name: "不同隧道端口", method: "GET", path: "/admin/api/state", key: token, headers: map[string]string{"Origin": "http://localhost:18317"}, want: 200},
		{name: "跨端口来源", method: "GET", path: "/admin/api/state", key: token, headers: map[string]string{"Origin": "http://localhost:8317"}, want: 403},
		{name: "跨站写入", method: "POST", path: "/admin/api/connections", key: token, body: `{}`, headers: map[string]string{"Origin": "https://evil.example"}, want: 403},
		{name: "null 来源", method: "GET", path: "/admin/api/state", key: token, headers: map[string]string{"Origin": "null"}, want: 403},
		{name: "跨站浏览器", method: "GET", path: "/admin/api/state", key: token, headers: map[string]string{"Sec-Fetch-Site": "cross-site"}, want: 403},
		{name: "查询令牌禁用", method: "GET", path: "/admin/api/state?token=" + token, want: 404},
		{name: "未知资源", method: "GET", path: "/ui/missing", want: 404},
		{name: "未知接口", method: "GET", path: "/admin/api/missing", key: token, want: 404},
		{name: "错误方法", method: "POST", path: "/admin/api/state", key: token, want: 405},
		{name: "未知字段", method: "POST", path: "/admin/api/connections", key: token, body: `{"credentialRef":"secret"}`, want: 400},
		{name: "多份 JSON", method: "POST", path: "/admin/api/connections", key: token, body: `{} {}`, want: 400},
		{name: "表单写入", method: "POST", path: "/admin/api/connections", key: token, body: `{}`, headers: map[string]string{"Content-Type": "text/plain"}, want: 415},
		{name: "限制请求大小", method: "POST", path: "/admin/api/connections", key: token, body: `{"apiKey":"` + strings.Repeat("x", 1<<20) + `"}`, want: 400},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := request(t, h, tc.method, tc.path, tc.key, tc.body, tc.headers)
			if w.Code != tc.want {
				t.Fatalf("状态 %d，预期 %d", w.Code, tc.want)
			}
			if w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("管理响应不得缓存")
			}
			if strings.Contains(w.Body.String(), token) {
				t.Fatal("不得返回管理令牌")
			}
		})
	}
	r := httptest.NewRequest("GET", "http://evil.example/admin/api/state", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("必须拒绝 DNS 重绑定 Host")
	}
	s, _ := st.Read()
	if s.Revision != 0 {
		t.Fatal("被拒绝的请求不能修改状态")
	}
}
func TestConfigurationFlow(t *testing.T) {
	st, h, token := setup(t)
	call := func(method, path, body string, want int) string {
		t.Helper()
		w := request(t, h, method, "/admin/api/"+path, token, body, nil)
		if w.Code != want {
			t.Fatalf("%s %s 状态 %d，预期 %d", method, path, w.Code, want)
		}
		return w.Body.String()
	}
	call("POST", "connections", `{"name":"local","protocol":"anthropic-messages","baseURL":"https://example.com","models":[{"id":"actual"}],"apiKey":"upstream-test-secret"}`, 200)
	body := call("GET", "state", "", 200)
	if strings.Contains(body, "upstream-test-secret") || strings.Contains(body, "credentialRef") {
		t.Fatal("不得导出上游凭证或引用")
	}
	call("PATCH", "connections/local", `{"baseURL":"https://example.org","apiKey":"rotated-test-secret"}`, 200)
	s, _ := st.Read()
	key, _ := s.Key(s.Connections["local"])
	if key != "rotated-test-secret" || s.Connections["local"].BaseURL != "https://example.org" {
		t.Fatal("轮换和地址修改未保存")
	}
	models := map[string]config.Binding{}
	for _, slot := range config.Slots {
		models[slot] = config.Binding{PublicModel: "claude-" + slot, Connection: "local", TargetModel: "actual"}
	}
	data, _ := json.Marshal(map[string]any{"name": "daily", "models": models})
	created := call("POST", "profiles", string(data), 200)
	var result struct {
		ProfileKey string `json:"profileKey"`
	}
	if err := json.Unmarshal([]byte(created), &result); err != nil || config.ValidateKey(result.ProfileKey) != nil {
		t.Fatal("创建必须返回一次有效的下游 key")
	}
	s, _ = st.Read()
	profileKey := s.Secrets[s.Profiles["daily"].KeyRef]
	if result.ProfileKey != profileKey {
		t.Fatal("返回的 key 必须能认证新建 profile")
	}
	duplicate := call("POST", "profiles", string(data), 409)
	if strings.Contains(duplicate, profileKey) {
		t.Fatal("重复创建不能泄漏现有 key")
	}
	body = call("GET", "state", "", 200)
	if strings.Contains(body, profileKey) || strings.Contains(body, "keyRef") || strings.Contains(body, "rotated-test-secret") {
		t.Fatal("状态视图不得导出 key")
	}
	call("DELETE", "connections/local", "", 409)
	call("PATCH", "connections/local", `{"models":[{"id":"other"}]}`, 409)
	call("PATCH", "connections/local", `{"authType":"codex-oauth"}`, 400)
	updated := call("PATCH", "profiles/daily", `{"models":{"sonnet":{"public_model":"claude-sonnet","connection":"local","target_model":"actual","label":"更新展示名"}}}`, 200)
	if strings.Contains(updated, profileKey) || strings.Contains(updated, "profileKey") {
		t.Fatal("更新响应不得再次返回 key")
	}
	s, _ = st.Read()
	if s.Profiles["daily"].Models["sonnet"].Label != "更新展示名" || s.Secrets[s.Profiles["daily"].KeyRef] != profileKey {
		t.Fatal("profile 更新应保留 key 与其他档位")
	}
	settings := filepath.Join(t.TempDir(), "settings.json")
	if err := claudecode.Apply(st, s.Profiles["daily"], settings); err != nil {
		t.Fatal(err)
	}
	call("DELETE", "profiles/daily", "", 409)
	if err := claudecode.Restore(st, settings); err != nil {
		t.Fatal(err)
	}
	call("DELETE", "profiles/daily", "", 200)
	call("DELETE", "connections/local", "", 200)
	s, _ = st.Read()
	if len(s.Connections) != 0 || len(s.Profiles) != 0 || len(s.Secrets) != 0 {
		t.Fatal("删除必须回收自己的凭证")
	}
}
func TestOAuthOperationsRemainStoppedOnly(t *testing.T) {
	st, h, token := setup(t)
	c := config.Connection{Name: "oauth", AuthType: config.AuthCodexOAuth, Protocol: config.Responses, BaseURL: config.CodexBaseURL, Models: []config.Model{{ID: "actual"}}}
	if err := connection.Create(context.Background(), st, c, ""); err != nil {
		t.Fatal(err)
	}
	lock, err := st.AcquireRunLock()
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	for _, tc := range []struct {
		method, path, body string
		want               int
	}{
		{"POST", "connections", `{"name":"other","authType":"codex-oauth"}`, 409},
		{"DELETE", "connections/oauth", "", 409},
		{"PATCH", "connections/oauth", `{"apiKey":"test-key"}`, 409},
		{"POST", "connections/oauth/login", `{}`, 404},
		{"POST", "connections/oauth/logout", `{}`, 404},
	} {
		w := request(t, h, tc.method, "/admin/api/"+tc.path, token, tc.body, nil)
		if w.Code != tc.want {
			t.Fatalf("%s %s = %d", tc.method, tc.path, w.Code)
		}
	}
	s, _ := st.Read()
	if s.Revision != 1 || len(s.Connections) != 1 {
		t.Fatal("OAuth 拒绝操作必须保持原状态")
	}
}
func TestTokenPersistenceAndPermissions(t *testing.T) {
	st, _, first := setup(t)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			token, err := Token(st)
			if err != nil || token != first {
				t.Error("管理令牌必须稳定且并发安全")
			}
		})
	}
	wg.Wait()
	info, err := os.Stat(st.Path("admin-token"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("管理令牌必须以 0600 保存")
	}
	if err := os.Chmod(st.Path("admin-token"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Token(st); err == nil {
		t.Fatal("拒绝不安全权限")
	}
}
func TestNoActiveRevisionAcknowledgement(t *testing.T) {
	st, _, token := setup(t)
	h, err := New(st, func() (uint64, bool) { return 0, false })
	if err != nil {
		t.Fatal(err)
	}
	w := request(t, h, "POST", "/admin/api/connections", token, `{"name":"pending","protocol":"responses","baseURL":"https://example.com","models":[{"id":"model"}],"apiKey":"test-key"}`, nil)
	if w.Code != 202 || !strings.Contains(w.Body.String(), `"active":false`) {
		t.Fatal("未生效时必须明确返回已保存但尚未生效")
	}
	s, _ := st.Read()
	if _, ok := s.Connections["pending"]; !ok {
		t.Fatal("必须验证实际保存结果")
	}
	models := map[string]config.Binding{}
	for _, slot := range config.Slots {
		models[slot] = config.Binding{PublicModel: "claude-" + slot, Connection: "pending", TargetModel: "model"}
	}
	body, err := json.Marshal(map[string]any{"name": "pending-profile", "models": models})
	if err != nil {
		t.Fatal(err)
	}
	w = request(t, h, "POST", "/admin/api/profiles", token, string(body), nil)
	var result saveResult
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != 202 || !result.Saved || result.Active || config.ValidateKey(result.ProfileKey) != nil {
		t.Fatal("已保存但未生效的创建也必须返回一次 key")
	}
	s, err = st.Read()
	if err != nil || result.ProfileKey != s.Secrets[s.Profiles["pending-profile"].KeyRef] {
		t.Fatal("未生效时返回的 key 必须与实际保存值一致")
	}
}

// SDK 在明文连接上也会填入非 nil 的空 TLS 状态。
func TestOriginWithSDKConnectionState(t *testing.T) {
	_, h, token := setup(t)
	for _, tc := range []struct {
		scheme   string
		tlsState *tls.ConnectionState
	}{
		{"http", &tls.ConnectionState{}},
		{"https", &tls.ConnectionState{HandshakeComplete: true}},
	} {
		r := httptest.NewRequest("GET", tc.scheme+"://localhost:18317/admin/api/state", nil)
		r.TLS = tc.tlsState
		r.Header.Set("Origin", tc.scheme+"://localhost:18317")
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("%s 同源请求被拒绝", tc.scheme)
		}
	}
}
