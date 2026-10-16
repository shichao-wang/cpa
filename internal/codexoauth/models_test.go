package codexoauth

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/shichao-wang/cpa/internal/config"
)

type modelTransport func(*http.Request) (*http.Response, error)

func (f modelTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func modelCredential() config.OAuthCredential {
	return config.OAuthCredential{AccessToken: "mock-access", RefreshToken: "mock-refresh", AccountID: "mock-account", Generation: 1, ExpiresAt: time.Now().Add(time.Hour)}
}

func TestOAuthModelsUseOfficialAccountCatalog(t *testing.T) {
	calls := 0
	transport := modelTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != "GET" || r.URL.Scheme != "https" || r.URL.Host != "chatgpt.com" || r.URL.Path != "/backend-api/codex/models" || r.URL.Query().Get("client_version") == "" || r.Header.Get("Authorization") != "Bearer mock-access" || r.Header.Get("Chatgpt-Account-Id") != "mock-account" {
			t.Fatal("必须仅向官方账号目录发送 OAuth 凭证")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"models":[{"slug":"gpt-6.1-sol","display_name":"Z","visibility":"list","supported_in_api":false},{"slug":"hidden","visibility":"hide"},{"slug":"future-model-not-in-sdk","visibility":"list"},{"slug":"gpt-6-luna","display_name":"A","visibility":"list"},{"slug":"gpt-6.1-sol","visibility":"list"}]}`)), Header: make(http.Header)}, nil
	})
	models, err := discoverModels(context.Background(), modelCredential(), transport)
	if err != nil {
		t.Fatal(err)
	}
	want := []config.Model{{ID: "gpt-6-luna", Name: "A"}, {ID: "gpt-6.1-sol", Name: "Z"}}
	if calls != 1 || !reflect.DeepEqual(models, want) {
		t.Fatalf("账号模型目录不正确：%+v", models)
	}
}
func TestOAuthModelFailuresDoNotLeakOrFollowRedirects(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"unauthorized", 401, "mock-access private upstream error"},
		{"redirect", 302, "mock-access"},
		{"invalid", 200, `{"models":[{"slug":"","visibility":"list"}]}`},
		{"no catalog", 200, `{"data":[{"id":"wrong-api"}]}`},
		{"empty", 200, `{"models":[]}`},
		{"unsupported", 200, `{"models":[{"slug":"future-model-not-in-sdk","visibility":"list"}]}`},
		{"oversized", 200, strings.Repeat("x", (4<<20)+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			transport := modelTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: tc.status, Header: http.Header{"Location": []string{"https://foreign.example/models"}}, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})
			_, err := discoverModels(context.Background(), modelCredential(), transport)
			if err == nil || strings.Contains(err.Error(), "mock-access") || strings.Contains(err.Error(), "private upstream") || calls != 1 {
				t.Fatal("失败不得泄漏响应正文或跟随重定向")
			}
		})
	}
	expired := modelCredential()
	expired.ExpiresAt = time.Now().Add(-time.Second)
	_, err := discoverModels(context.Background(), expired, modelTransport(func(*http.Request) (*http.Response, error) {
		t.Fatal("过期凭证不得请求上游")
		return nil, fmt.Errorf("unexpected")
	}))
	if err == nil {
		t.Fatal("过期凭证应要求重新登录")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = discoverModels(ctx, modelCredential(), modelTransport(func(r *http.Request) (*http.Response, error) { return nil, r.Context().Err() }))
	if err == nil {
		t.Fatal("取消时应结束目录请求")
	}
}
