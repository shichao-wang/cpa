package gateway

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	cliproxy "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/shichao-wang/cpa/internal/config"
	"github.com/shichao-wang/cpa/internal/connection"
)

type hotStreamTransport struct {
	mock    oauthTransport
	release chan struct{}
	once    sync.Once
}

func (m *hotStreamTransport) RoundTripperFor(*coreauth.Auth) http.RoundTripper { return m }
func (m *hotStreamTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Hostname() == "127.0.0.1" {
		return http.DefaultTransport.RoundTrip(r)
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	r.Body = io.NopCloser(strings.NewReader(string(body)))
	if !strings.Contains(string(body), `"stream":true`) {
		return m.mock.RoundTrip(r)
	}
	// Codex 也用流传递非流请求，因此只有首个请求保持打开。
	first := false
	m.once.Do(func() { first = true })
	if !first {
		return m.mock.RoundTrip(r)
	}
	reader, writer := io.Pipe()
	go func() {
		defer writer.Close()
		events := []string{
			`{"type":"response.created","response":{"id":"resp_stream","model":"gpt-5.5","output":[]}}`,
			`{"type":"response.output_item.added","output_index":0,"item":{"id":"msg_stream","type":"message","role":"assistant","content":[]}}`,
			`{"type":"response.content_part.added","output_index":0,"content_index":0,"part":{"type":"output_text","text":""}}`,
			`{"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"before-update"}`,
		}
		for _, event := range events {
			if _, err := fmt.Fprintf(writer, "data: %s\n\n", event); err != nil {
				return
			}
		}
		select {
		case <-m.release:
		case <-r.Context().Done():
			return
		}
		fmt.Fprint(writer, "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":0,\"delta\":\"after-update\"}\n\n")
		fmt.Fprint(writer, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_stream\",\"status\":\"completed\",\"model\":\"gpt-5.5\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":2}}}\n\n")
	}()
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: reader, Request: r}, nil
}

func TestConnectionAddAndOAuthLoginPreserveLiveRequests(t *testing.T) {
	for _, firstOAuth := range []bool{true, false} {
		t.Run(fmt.Sprintf("first-oauth-%t", firstOAuth), func(t *testing.T) {
			st, state := oauthTestState(t)
			streamName := "a"
			if firstOAuth {
				streamName = "api"
				if err := st.Update(func(s *config.State) error {
					for name, c := range s.Connections {
						if c.AuthType == config.AuthCodexOAuth {
							delete(s.Connections, name)
							delete(s.Profiles, name)
						}
					}
					s.OAuthCredentials = map[string]config.OAuthCredential{}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			transport := &hotStreamTransport{mock: oauthTransport{requests: make(chan observedOAuthRequest, 32)}, release: make(chan struct{})}
			defer close(transport.release)
			manager := startManagedTest(t, st, runtimeOptions{transport: transport})
			codex, _ := manager.Executor("codex")
			client := &http.Client{Timeout: 15 * time.Second}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			req, _ := http.NewRequestWithContext(ctx, "POST", "http://"+state.Listen+"/v1/messages", strings.NewReader(`{"model":"claude-opus-test","max_tokens":32,"stream":true,"messages":[{"role":"user","content":"test"}]}`))
			req.Header.Set("X-Api-Key", "client-secret-"+streamName)
			req.Header.Set("Content-Type", "application/json")
			response := mustDo(t, client, req)
			defer response.Body.Close()
			if response.StatusCode != 200 {
				t.Fatal("流请求失败", response.StatusCode)
			}
			scanner := bufio.NewScanner(response.Body)
			found := false
			for scanner.Scan() {
				if strings.Contains(scanner.Text(), "before-update") {
					found = true
					break
				}
			}
			if !found {
				t.Fatal("更新前应已输出文本", scanner.Err())
			}
			assertOldRoute := func() {
				r, _ := http.NewRequest("GET", "http://"+state.Listen+"/v1/models", nil)
				r.Header.Set("X-Api-Key", "client-secret-"+streamName)
				resp := mustDo(t, client, r)
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				if resp.StatusCode != 200 {
					t.Fatal("新增连接时原有路由不可用", resp.StatusCode)
				}
			}
			credential := config.OAuthCredential{AccessToken: "access-new", RefreshToken: "refresh-new", AccountID: "account-new", PlanType: "pro", ExpiresAt: time.Now().Add(7 * 24 * time.Hour)}
			if err := connection.Create(ctx, st, config.Connection{Name: "new", AuthType: config.AuthCodexOAuth, Protocol: config.Responses, BaseURL: config.CodexBaseURL, Models: []config.Model{{ID: "gpt-5.5"}}}, ""); err != nil {
				t.Fatal(err)
			}
			assertOldRoute()
			waitRevision(t, st)
			assertOldRoute()
			login, err := connection.BeginOAuth(st, "new")
			if err != nil {
				t.Fatal(err)
			}
			if err := login.Commit(credential); err != nil {
				login.Close()
				t.Fatal(err)
			}
			login.Close()
			assertOldRoute()
			waitRevision(t, st)
			assertOldRoute()
			if err := st.Update(func(s *config.State) error {
				p := s.Profiles[streamName]
				p.Name = "new"
				p.ID = "profile-new"
				p.KeyRef = "client-new"
				p.Models = map[string]config.Binding{}
				for _, slot := range config.Slots {
					p.Models[slot] = config.Binding{PublicModel: "claude-" + slot + "-test", Connection: "new", TargetModel: "gpt-5.5"}
				}
				s.Profiles["new"] = p
				s.Secrets[p.KeyRef] = "client-secret-new"
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			waitRevision(t, st)
			checkNew := func(key, account string) {
				resp := postMessages(t, client, state.Listen, "client-secret-new", "claude-opus-test")
				defer resp.Body.Close()
				if resp.StatusCode != 200 {
					body, _ := io.ReadAll(resp.Body)
					t.Fatalf("新认证不可用: %d %s", resp.StatusCode, body)
				}
				got := <-transport.mock.requests
				if got.key != "Bearer "+key || got.account != account {
					t.Fatal("新认证未独立生效")
				}
			}
			checkNew("access-new", "account-new")
			session, err := connection.BeginOAuth(st, "new")
			if err != nil {
				t.Fatal(err)
			}
			credential.AccessToken = "access-relogin"
			credential.AccountID = "account-relogin"
			if err := session.Commit(credential); err != nil {
				t.Fatal(err)
			}
			session.Close()
			assertOldRoute()
			waitRevision(t, st)
			checkNew("access-relogin", "account-relogin")
			if !firstOAuth {
				session, err := connection.BeginOAuth(st, streamName)
				if err != nil {
					t.Fatal(err)
				}
				credential.AccessToken = "access-stream-relogin"
				credential.AccountID = "account-stream-relogin"
				if err := session.Commit(credential); err != nil {
					t.Fatal(err)
				}
				session.Close()
				waitRevision(t, st)
				assertOldRoute()
			}
			// 三种协议的新 API_KEY 连接也必须增量注册，不能替换已有执行器。
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if strings.Contains(r.URL.Path, "chat/completions") {
					fmt.Fprint(w, `{"id":"chat_mock","object":"chat.completion","model":"gpt-5.5","choices":[{"index":0,"message":{"role":"assistant","content":"mock reply"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
				} else {
					fmt.Fprint(w, `{"id":"msg_mock","type":"message","role":"assistant","model":"gpt-5.5","content":[{"type":"text","text":"mock reply"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
				}
			}))
			defer upstream.Close()
			for _, protocol := range []string{config.Chat, config.Anthropic, config.Responses} {
				name := "added-" + protocol
				base := upstream.URL
				if protocol == config.Responses {
					base = "https://mock.example.test"
				}
				if err := connection.Create(ctx, st, config.Connection{Name: name, AuthType: config.AuthAPIKey, Protocol: protocol, BaseURL: base, Models: []config.Model{{ID: "gpt-5.5"}}}, "key-"+protocol); err != nil {
					t.Fatal(err)
				}
				assertOldRoute()
				waitRevision(t, st)
				if err := st.Update(func(s *config.State) error {
					p := s.Profiles["new"]
					for slot, b := range p.Models {
						b.Connection = name
						p.Models[slot] = b
					}
					s.Profiles["new"] = p
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				waitRevision(t, st)
				resp := postMessages(t, client, state.Listen, "client-secret-new", "claude-opus-test")
				body, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				if resp.StatusCode != 200 || !strings.Contains(string(body), "mock reply") {
					t.Fatalf("新增 %s 路由失败: %d %s", protocol, resp.StatusCode, body)
				}
			}
			if next, _ := manager.Executor("codex"); next != codex {
				t.Fatal("新增连接不得替换已有 Codex 执行器")
			}
			transport.release <- struct{}{}
			var tail strings.Builder
			for scanner.Scan() {
				tail.WriteString(scanner.Text())
				tail.WriteByte('\n')
			}
			if scanner.Err() != nil || !strings.Contains(tail.String(), "after-update") || !strings.Contains(tail.String(), "message_stop") {
				t.Fatal("热更新中断了在途输出", scanner.Err(), tail.String())
			}
		})
	}
}

// 即使切换后旧认证尚在 SDK 内，新的请求也必须只使用当前凭证。
func TestOAuthReloginNeverSelectsRetiredCredential(t *testing.T) {
	st, state := oauthTestState(t)
	transport := &oauthTransport{requests: make(chan observedOAuthRequest, 32)}
	manager := startManagedTest(t, st, runtimeOptions{transport: transport})
	var retired *coreauth.Auth
	for _, a := range manager.List() {
		if a.Metadata["account_id"] == "account-a" {
			retired = a
			break
		}
	}
	if retired == nil {
		t.Fatal("缺少原始账号")
	}
	registry := cliproxy.GlobalModelRegistry()
	retiredModels := registry.GetModelsForClient(retired.ID)
	session, err := connection.BeginOAuth(st, "a")
	if err != nil {
		t.Fatal(err)
	}
	credential := config.OAuthCredential{AccessToken: "new-access-a", RefreshToken: "new-refresh-a", AccountID: "new-account-a", PlanType: "pro", ExpiresAt: time.Now().Add(7 * 24 * time.Hour)}
	if err := session.Commit(credential); err != nil {
		session.Close()
		t.Fatal(err)
	}
	session.Close()
	waitRevision(t, st)
	// 延长旧新认证共存的窗口，使账号串线可以确定性复现。
	if _, err := manager.Register(coreauth.WithSkipPersist(t.Context()), retired); err != nil {
		t.Fatal(err)
	}
	registry.RegisterClient(retired.ID, "codex", retiredModels)
	manager.RefreshSchedulerEntry(retired.ID)
	client := &http.Client{Timeout: 5 * time.Second}
	for i := 0; i < 12; i++ {
		response := postMessages(t, client, state.Listen, "client-secret-a", "claude-opus-test")
		io.Copy(io.Discard, response.Body)
		response.Body.Close()
		if response.StatusCode != 200 {
			t.Fatal("当前账号请求不可用", response.StatusCode)
		}
		got := <-transport.requests
		if got.key != "Bearer new-access-a" || got.account != "new-account-a" {
			t.Fatal("切换后请求使用了旧账号")
		}
	}
}
