package webui

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/shichao-wang/cpa/internal/config"
	"github.com/shichao-wang/cpa/internal/connection"
)

func TestConnectionDiscoveryIsAuthenticatedAndDoesNotPersist(t *testing.T) {
	st, h, token := setup(t)
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "GET" || r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer mock-directory-key" {
			t.Error("只能使用独立上游凭证请求模型目录")
		}
		io.WriteString(w, `{"data":[{"id":"available-model"}]}`)
	}))
	defer upstream.Close()
	before, _ := st.Read()
	input, _ := json.Marshal(map[string]any{"protocol": config.Chat, "baseURL": upstream.URL + "/v1", "apiKey": "mock-directory-key"})
	for _, headers := range []map[string]string{nil, {"Origin": "https://foreign.example"}} {
		key := token
		want := 403
		if headers == nil {
			key = ""
			want = 401
		}
		w := request(t, h, "POST", "/admin/api/connections/test", key, string(input), headers)
		if w.Code != want || calls != 0 {
			t.Fatal("未认证或跨来源请求不得访问上游")
		}
	}
	w := request(t, h, "POST", "/admin/api/connections/test", token, string(input), nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"available-model"`) || strings.Contains(w.Body.String(), "mock-directory-key") {
		t.Fatal("应返回模型清单且不返回凭证")
	}
	after, _ := st.Read()
	if !reflect.DeepEqual(before, after) || calls != 1 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("测试连接不得保存结构或凭证，响应不得缓存")
	}
}

func TestConnectionDiscoveryStoredCredentialBoundary(t *testing.T) {
	st, h, token := setup(t)
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer stored-mock-key" {
			t.Error("应使用此连接自己的凭证")
		}
		io.WriteString(w, `{"data":[{"id":"model-a"}]}`)
	}))
	defer upstream.Close()
	c := config.Connection{Name: "saved", AuthType: config.AuthAPIKey, Protocol: config.Chat, BaseURL: upstream.URL, Models: []config.Model{{ID: "model-a"}}}
	if err := connection.Create(context.Background(), st, c, "stored-mock-key"); err != nil {
		t.Fatal(err)
	}
	before, _ := st.Read()
	w := request(t, h, "POST", "/admin/api/connections/test", token, `{"connection":"saved"}`, nil)
	if w.Code != 200 || calls != 1 {
		t.Fatal("同地址应允许使用已保存凭证")
	}
	for _, body := range []string{
		`{"connection":"saved","baseURL":"https://another.example"}`,
		`{"connection":"saved","protocol":"responses"}`,
		`{"connection":"missing"}`,
	} {
		w = request(t, h, "POST", "/admin/api/connections/test", token, body, nil)
		if w.Code == 200 || calls != 1 || strings.Contains(w.Body.String(), "stored-mock-key") {
			t.Fatal("不得把保存的凭证发送到新地址、切换协议或返回给浏览器")
		}
	}
	after, _ := st.Read()
	if !reflect.DeepEqual(before, after) {
		t.Fatal("发现模型不得更新状态或 revision")
	}
}

func TestConnectionDiscoveryRejectsOAuthAPIKeyFallback(t *testing.T) {
	st, h, token := setup(t)
	c := config.Connection{Name: "oauth", AuthType: config.AuthCodexOAuth, Protocol: config.Responses, BaseURL: config.CodexBaseURL, Models: []config.Model{{ID: "coding"}}}
	if err := connection.Create(context.Background(), st, c, ""); err != nil {
		t.Fatal(err)
	}
	w := request(t, h, "POST", "/admin/api/connections/test", token, `{"connection":"oauth","apiKey":"mock-directory-key"}`, nil)
	if w.Code != 400 {
		t.Fatal("OAuth 连接不得回退到 API key 请求目录")
	}
}

func TestConnectionDiscoveryDoesNotEchoUpstreamErrors(t *testing.T) {
	_, h, token := setup(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(401)
		io.WriteString(w, "echoed-private-response mock-directory-key")
	}))
	defer upstream.Close()
	input, _ := json.Marshal(map[string]any{"protocol": config.Chat, "baseURL": upstream.URL, "apiKey": "mock-directory-key"})
	w := request(t, h, "POST", "/admin/api/connections/test", token, string(input), nil)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "HTTP 401") || strings.Contains(w.Body.String(), "mock-directory-key") || strings.Contains(w.Body.String(), "echoed-private-response") {
		t.Fatal("上游拒绝认证应保留管理会话且不泄漏 key 或响应正文")
	}
}
