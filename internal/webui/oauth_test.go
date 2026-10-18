package webui

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shichao-wang/cpa/internal/config"
	"github.com/shichao-wang/cpa/internal/connection"
	"github.com/shichao-wang/cpa/internal/store"
)

type testControl struct {
	mu         sync.Mutex
	st         *store.Store
	lock       *store.RunLock
	mode       string
	resumeFail bool
	pauses     int
	resumes    int
}

func (c *testControl) Mode() string { c.mu.Lock(); defer c.mu.Unlock(); return c.mode }
func (c *testControl) Pause(context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pauses++
	if c.lock != nil {
		c.lock.Close()
		c.lock = nil
	}
	c.mode = "paused"
	return nil
}
func (c *testControl) Resume(context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.resumes++
	if c.resumeFail {
		c.mode = "failed"
		return fmt.Errorf("mock resume failure")
	}
	if c.lock == nil {
		var err error
		c.lock, err = c.st.AcquireRunLock()
		if err != nil {
			return err
		}
	}
	c.mode = "running"
	return nil
}
func setupOAuth(t *testing.T) (*store.Store, *Handler, string, *testControl) {
	t.Helper()
	st, err := store.New(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	control := &testControl{st: st, mode: "paused"}
	if err = control.Resume(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	h, err := New(st, "[::1]:18317", func() (uint64, bool) {
		s, e := st.Read()
		if e != nil {
			return 0, false
		}
		return s.Revision, control.Mode() == "running"
	}, control, ctx)
	if err != nil {
		t.Fatal(err)
	}
	h.oauth.loginDraft = func(context.Context, func(string)) (config.OAuthCredential, error) { return testCredential(), nil }
	h.oauth.login = func(_ context.Context, name string, _ func(string)) error {
		session, err := connection.BeginOAuth(st, name)
		if err != nil {
			return err
		}
		defer session.Close()
		return session.Commit(testCredential())
	}
	h.oauth.discover = func(context.Context, config.OAuthCredential) ([]config.Model, error) {
		return []config.Model{{ID: "mock-coding", Name: "账号编程模型"}, {ID: "mock-fast"}}, nil
	}
	token, err := Token(st)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); h.Close(); control.Pause(context.Background()) })
	return st, h, token, control
}
func testCredential() config.OAuthCredential {
	return config.OAuthCredential{AccessToken: "mock-access-token", RefreshToken: "mock-refresh-token", IDToken: "mock-id-token", AccountID: "mock-account", PlanType: "plus", ExpiresAt: time.Now().Add(time.Hour), LastRefresh: time.Now()}
}
func waitOAuth(t *testing.T, h *Handler, phase string) *OAuthStatus {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s := h.oauth.snapshot()
		if s != nil && s.Phase == phase {
			return s
		}
		time.Sleep(10 * time.Millisecond)
	}
	s := h.oauth.snapshot()
	t.Fatalf("授权未达到 %s：%+v", phase, s)
	return nil
}

func beginDraft(t *testing.T, h *Handler, token, name string) *OAuthStatus {
	t.Helper()
	if request(t, h, "POST", "/admin/api/oauth/authorize", token, `{"name":"`+name+`"}`, nil).Code != 202 {
		t.Fatal("无法开始 OAuth 登录")
	}
	return waitOAuth(t, h, "selecting")
}
func commitDraft(t *testing.T, h *Handler, token, id string) {
	t.Helper()
	w := request(t, h, "POST", "/admin/api/oauth/session/"+id+"/connection", token, `{"models":[{"id":"mock-fast"}]}`, nil)
	if w.Code != 202 {
		t.Fatal("确认模型后创建失败", w.Code)
	}
	waitOAuth(t, h, "succeeded")
}
func seedOAuth(t *testing.T, st *store.Store, control *testControl, name string) {
	t.Helper()
	_ = control.Pause(context.Background())
	if err := connection.CreateOAuth(context.Background(), st, name, []config.Model{{ID: "prior-model"}}, testCredential()); err != nil {
		t.Fatal(err)
	}
	if err := control.Resume(context.Background()); err != nil {
		t.Fatal(err)
	}
}
func TestWebOAuthOnlyCreatesAfterModelConfirmation(t *testing.T) {
	st, h, token, control := setupOAuth(t)
	approved := make(chan struct{})
	h.oauth.loginDraft = func(ctx context.Context, publish func(string)) (config.OAuthCredential, error) {
		publish("ABCD-1234")
		select {
		case <-approved:
			return testCredential(), nil
		case <-ctx.Done():
			return config.OAuthCredential{}, ctx.Err()
		}
	}
	before, _ := st.Read()
	for _, headers := range []map[string]string{nil, {"Origin": "https://foreign.example"}} {
		key, want := token, 403
		if headers == nil {
			key = ""
			want = 401
		}
		if request(t, h, "POST", "/admin/api/oauth/authorize", key, `{"name":"new-oauth"}`, headers).Code != want {
			t.Fatal("登录必须经过认证和同源校验")
		}
	}
	if request(t, h, "POST", "/admin/api/oauth/authorize", token, `{"name":"new-oauth"}`, nil).Code != 202 {
		t.Fatal("无法开始登录")
	}
	status := waitOAuth(t, h, "waiting")
	if control.Mode() != "running" {
		t.Fatal("登录不得暂停模型服务")
	}
	if lock, err := st.AcquireRunLock(); err == nil {
		lock.Close()
		t.Fatal("模型服务必须继续持有运行锁")
	}
	if lock, err := st.AcquireOAuthLock(); err == nil {
		lock.Close()
		t.Fatal("网络登录应持独立登录锁")
	}
	if err := store.WithLock(st.Path("state.lock"), func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if request(t, h, "GET", "/ui/", "", "", nil).Code != 200 || request(t, h, "GET", "/admin/api/oauth/session", "", "", nil).Code != 401 {
		t.Fatal("管理页面保持可用，设备码必须认证")
	}
	w := request(t, h, "GET", "/admin/api/oauth/session", token, "", nil)
	if !strings.Contains(w.Body.String(), "ABCD-1234") || strings.Contains(w.Body.String(), "mock-access-token") {
		t.Fatal("只能返回设备码，不能返回凭证")
	}
	during, _ := st.Read()
	if during.Revision != before.Revision || len(during.Connections) != 0 || len(during.OAuthCredentials) != 0 {
		t.Fatal("登录时不得创建 connection 或凭证")
	}
	close(approved)
	status = waitOAuth(t, h, "selecting")
	during, _ = st.Read()
	if len(during.Connections) != 0 || len(during.OAuthCredentials) != 0 || during.Revision != before.Revision || len(status.Models) != 2 || status.Code != "" || control.Mode() != "running" {
		t.Fatal("获取模型后只能暂存授权，模型服务应恢复")
	}
	if request(t, h, "POST", "/admin/api/oauth/authorize", token, `{"name":"another"}`, nil).Code != 409 {
		t.Fatal("未确认的授权草稿不能被覆盖")
	}
	path := "/admin/api/oauth/session/" + status.ID + "/connection"
	for _, body := range []string{`{"models":[]}`, `{"models":[{"id":"not-discovered"}]}`, `{"models":[{"id":"mock-fast"},{"id":"mock-fast"}]}`} {
		if request(t, h, "POST", path, token, body, nil).Code != 409 {
			t.Fatal("只能创建非空、不重复且来自授权目录的模型")
		}
	}
	if request(t, h, "POST", path, "", `{"models":[{"id":"mock-fast"}]}`, nil).Code != 401 {
		t.Fatal("创建必须经过管理认证")
	}
	commitDraft(t, h, token, status.ID)
	control.mu.Lock()
	pauses, resumes := control.pauses, control.resumes
	control.mu.Unlock()
	if pauses != 0 || resumes != 1 {
		t.Fatal("登录和保存均不得调用暂停或重启", pauses, resumes)
	}
	after, err := st.Read()
	if err != nil {
		t.Fatal(err)
	}
	c := after.Connections["new-oauth"]
	if len(after.Connections) != 1 || len(after.OAuthCredentials) != 1 || after.Revision != before.Revision+1 || len(c.Models) != 1 || c.Models[0].ID != "mock-fast" {
		t.Fatal("必须在一次事务中创建连接、凭证并只保存所选模型")
	}
	if c.Protocol != config.Responses || c.BaseURL != config.CodexBaseURL || c.AuthType != config.AuthCodexOAuth {
		t.Fatal("OAuth 协议和地址必须固定")
	}
	if _, err = after.Credential(c); err != nil {
		t.Fatal("OAuth 凭证未正确保存")
	}
	if request(t, h, "POST", path, token, `{"models":[{"id":"mock-fast"}]}`, nil).Code != 409 {
		t.Fatal("授权草稿只能提交一次")
	}
}
func TestWebOAuthCancelFailureAndDiscardNeverCreate(t *testing.T) {
	for _, kind := range []string{"cancel", "login-failure", "directory-failure", "discard", "expired"} {
		t.Run(kind, func(t *testing.T) {
			st, h, token, control := setupOAuth(t)
			if kind == "cancel" || kind == "login-failure" {
				h.oauth.loginDraft = func(ctx context.Context, publish func(string)) (config.OAuthCredential, error) {
					if kind == "cancel" {
						publish("CANCEL-1234")
						<-ctx.Done()
						return config.OAuthCredential{}, ctx.Err()
					}
					return config.OAuthCredential{}, fmt.Errorf("private mock-access-token")
				}
			}
			if kind == "directory-failure" {
				h.oauth.discover = func(context.Context, config.OAuthCredential) ([]config.Model, error) {
					return nil, fmt.Errorf("private mock-access-token")
				}
			}
			if request(t, h, "POST", "/admin/api/oauth/authorize", token, `{"name":"cancelled"}`, nil).Code != 202 {
				t.Fatal("无法开始登录")
			}
			if kind == "cancel" {
				s := waitOAuth(t, h, "waiting")
				request(t, h, "DELETE", "/admin/api/oauth/session/"+s.ID, token, "", nil)
				waitOAuth(t, h, "cancelled")
			} else if kind == "discard" || kind == "expired" {
				s := waitOAuth(t, h, "selecting")
				if kind == "expired" {
					h.oauth.mu.Lock()
					h.oauth.status.ExpiresAt = time.Now().Add(-time.Second)
					h.oauth.mu.Unlock()
					h.oauth.expireDraft(s.ID)
				} else {
					request(t, h, "DELETE", "/admin/api/oauth/session/"+s.ID, token, "", nil)
				}
				waitOAuth(t, h, "cancelled")
			} else {
				waitOAuth(t, h, "failed")
			}
			state, _ := st.Read()
			status := h.oauth.snapshot()
			if len(state.Connections) != 0 || len(state.OAuthCredentials) != 0 || state.Revision != 0 || control.Mode() != "running" || strings.Contains(status.Message, "mock-access-token") {
				t.Fatal("取消、失败或放弃不得创建连接或保存凭证，且必须恢复模型服务")
			}
		})
	}
}
func TestWebOAuthNameConflictDoesNotOverwriteConnection(t *testing.T) {
	st, h, token, _ := setupOAuth(t)
	status := beginDraft(t, h, token, "same-name")
	c := config.Connection{Name: "same-name", AuthType: config.AuthAPIKey, Protocol: config.Chat, BaseURL: "https://example.com/v1", Models: []config.Model{{ID: "original"}}}
	if err := connection.Create(context.Background(), st, c, "mock-key"); err != nil {
		t.Fatal(err)
	}
	w := request(t, h, "POST", "/admin/api/oauth/session/"+status.ID+"/connection", token, `{"models":[{"id":"mock-fast"}]}`, nil)
	if w.Code != 202 {
		t.Fatal("确认失败")
	}
	waitOAuth(t, h, "failed")
	state, _ := st.Read()
	if state.Connections[c.Name].AuthType != config.AuthAPIKey || len(state.OAuthCredentials) != 0 {
		t.Fatal("名称冲突不能覆盖连接或留下 OAuth 凭证")
	}
}
func TestWebOAuthExistingConnectionLoginLogoutAndDelete(t *testing.T) {
	st, h, token, control := setupOAuth(t)
	seedOAuth(t, st, control, "existing")
	if request(t, h, "POST", "/admin/api/oauth/login", token, `{"name":"existing"}`, nil).Code != 202 {
		t.Fatal("重登录失败")
	}
	waitOAuth(t, h, "succeeded")
	if request(t, h, "POST", "/admin/api/oauth/connections/existing", token, `{"action":"logout"}`, nil).Code != 202 {
		t.Fatal("退出失败")
	}
	waitOAuth(t, h, "succeeded")
	state, _ := st.Read()
	if state.Connections["existing"].CredentialRef != "" || len(state.OAuthCredentials) != 0 {
		t.Fatal("退出必须清除本地凭证")
	}
	control.mu.Lock()
	control.resumeFail = true
	control.mu.Unlock()
	request(t, h, "POST", "/admin/api/oauth/connections/existing", token, `{"action":"delete"}`, nil)
	waitOAuth(t, h, "failed")
	control.mu.Lock()
	control.resumeFail = false
	control.mu.Unlock()
	if request(t, h, "POST", "/admin/api/gateway/resume", token, "", nil).Code != http.StatusOK {
		t.Fatal("应允许手动恢复")
	}
	state, _ = st.Read()
	if len(state.Connections) != 0 {
		t.Fatal("删除未完成")
	}
}
func TestExistingOAuthDirectoryFailurePreservesCredentials(t *testing.T) {
	st, h, token, control := setupOAuth(t)
	seedOAuth(t, st, control, "existing")
	h.oauth.discover = func(context.Context, config.OAuthCredential) ([]config.Model, error) {
		return nil, fmt.Errorf("private mock-access-token")
	}
	request(t, h, "POST", "/admin/api/oauth/login", token, `{"name":"existing"}`, nil)
	status := waitOAuth(t, h, "succeeded")
	state, _ := st.Read()
	if _, err := state.Credential(state.Connections["existing"]); err != nil || status.ModelError == "" || strings.Contains(status.ModelError, "mock-access-token") {
		t.Fatal("已有连接目录获取失败应保留已完成的登录并显示安全错误")
	}
}
