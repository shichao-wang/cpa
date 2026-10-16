package connection

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/shichao-wang/cpa/internal/config"
)

func TestDiscoverModelsUsesExplicitProtocolAndOnlyDirectoryGET(t *testing.T) {
	for _, protocol := range []string{config.Chat, config.Responses, config.Anthropic} {
		t.Run(protocol, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != "GET" || r.URL.Path != "/v1/models" {
					t.Error("只能请求模型目录")
				}
				if protocol == config.Anthropic {
					if r.Header.Get("x-api-key") != "mock-directory-key" || r.Header.Get("anthropic-version") != "2023-06-01" || r.Header.Get("Authorization") != "" {
						t.Error("Anthropic 认证头不正确")
					}
				} else if r.Header.Get("Authorization") != "Bearer mock-directory-key" || r.Header.Get("x-api-key") != "" {
					t.Error("Bearer 认证头不正确")
				}
				io.WriteString(w, `{"data":[{"id":"z-model","name":"Z"},{"id":"a-model"},{"id":"z-model"}]}`)
			}))
			defer server.Close()
			// 无名称与模型清单也可以发现，不根据名称选择协议。
			c := config.Connection{Protocol: protocol, AuthType: config.AuthAPIKey, BaseURL: server.URL + "/v1/"}
			models, err := DiscoverModels(context.Background(), c, "mock-directory-key")
			want := []config.Model{{ID: "a-model"}, {ID: "z-model", Name: "Z"}}
			if err != nil || !reflect.DeepEqual(models, want) || calls != 1 {
				t.Fatalf("目录解析错误：%v，次数 %d", err, calls)
			}
		})
	}
}

func TestDiscoverAnthropicPagination(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/v1/models" {
			t.Error("根地址应补齐 /v1")
		}
		if calls == 1 {
			io.WriteString(w, `{"data":[{"id":"alpha","display_name":"Alpha"}],"has_more":true,"last_id":"cursor+&?"}`)
		} else {
			if r.URL.Query().Get("after_id") != "cursor+&?" {
				t.Error("分页游标未正确编码")
			}
			io.WriteString(w, `{"data":[{"id":"beta","display_name":"Beta"},{"id":"retired","lifecycle":"retired"}],"has_more":false}`)
		}
	}))
	defer server.Close()
	models, err := DiscoverModels(context.Background(), config.Connection{Protocol: config.Anthropic, AuthType: config.AuthAPIKey, BaseURL: server.URL}, "mock-directory-key")
	if err != nil || !reflect.DeepEqual(models, []config.Model{{ID: "alpha", Name: "Alpha"}, {ID: "beta", Name: "Beta"}}) || calls != 2 {
		t.Fatalf("分页未完整获取：%v，次数 %d", err, calls)
	}
}

func TestDiscoverRejectsInvalidOrPartialDirectory(t *testing.T) {
	for _, body := range []string{
		`{"data":[]}`, `{"data":null}`, `{"error":"mock-directory-key"}`, `<html>mock-directory-key</html>`,
		`{"data":[{"id":""}]}`, `{"data":[{"id":"x\n"}]}`, `{"data":[{"id":"x","display_name":"bad\n"}]}`,
		`{"data":[{"id":"x"}],"has_more":true}`, `{"data":[{"id":"x"}],"has_more":true,"last_id":"repeated"}`,
		`{"data":[{"id":"x"}]} {}`, strings.Repeat(" ", (4<<20)+1),
	} {
		t.Run("拒绝不完整目录", func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, body) }))
			defer server.Close()
			models, err := DiscoverModels(context.Background(), config.Connection{Protocol: config.Anthropic, AuthType: config.AuthAPIKey, BaseURL: server.URL}, "mock-directory-key")
			if err == nil || models != nil || strings.Contains(err.Error(), "mock-directory-key") {
				t.Fatal("无效目录应拒绝且不泄漏响应正文或返回部分结果")
			}
		})
	}
}

func TestDiscoverRejectsOAuthAndRedirects(t *testing.T) {
	calls := 0
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]string{"id": "unexpected"}}})
	}))
	defer destination.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", destination.URL)
		w.WriteHeader(302)
		io.WriteString(w, "mock-directory-key")
	}))
	defer source.Close()
	c := config.Connection{Protocol: config.Responses, AuthType: config.AuthAPIKey, BaseURL: source.URL}
	_, err := DiscoverModels(context.Background(), c, "mock-directory-key")
	if err == nil || strings.Contains(err.Error(), "mock-directory-key") || calls != 0 {
		t.Fatal("不能跟随重定向或暴露上游正文")
	}
	c.AuthType = config.AuthCodexOAuth
	c.BaseURL = destination.URL
	if _, err = DiscoverModels(context.Background(), c, "mock-directory-key"); err == nil || calls != 0 {
		t.Fatal("OAuth 不得回退到 API key 发现")
	}
}

func TestDiscoverHonorsCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	models, err := DiscoverModels(ctx, config.Connection{Protocol: config.Chat, AuthType: config.AuthAPIKey, BaseURL: server.URL}, "mock-directory-key")
	if err == nil || models != nil {
		t.Fatal("取消请求后不得返回成功目录")
	}
}
