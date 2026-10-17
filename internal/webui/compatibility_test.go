package webui

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"github.com/shichao-wang/cpa/internal/config"
	"github.com/shichao-wang/cpa/internal/sdkmodels"
)

func TestConnectionCompatibilityState(t *testing.T) {
	st, handler, token := setup(t)
	if err := st.Update(func(s *config.State) error {
		s.Secrets["mock-key"] = "mock-key-not-real"
		for name, protocol := range map[string]string{"ordinary": config.Responses, "codex-in-name": config.Chat} {
			s.Connections[name] = config.Connection{ID: name, Name: name, AuthType: config.AuthAPIKey, Protocol: protocol, BaseURL: "https://example.test", CredentialRef: "mock-key", Models: []config.Model{{ID: "gpt-6.1-sol"}, {ID: "unknown-model"}}}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(st.Path("state.json"))
	if err != nil {
		t.Fatal(err)
	}
	response := request(t, handler, "GET", "/admin/api/state", token, "", nil)
	var result struct {
		Connections map[string]struct {
			Compatibility map[string]sdkmodels.ResponsesCompatibility `json:"compatibility"`
		} `json:"connections"`
	}
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &result) != nil {
		t.Fatalf("状态读取失败：%d", response.Code)
	}
	if result.Connections["ordinary"].Compatibility["gpt-6.1-sol"].Reasoning != "effort-mapping" || result.Connections["ordinary"].Compatibility["unknown-model"].Reasoning != "unavailable" {
		t.Fatal("Responses已知/未知模型的说明不准确")
	}
	if len(result.Connections["codex-in-name"].Compatibility) != 0 {
		t.Fatal("错误地根据连接名推断了协议能力")
	}
	after, err := os.ReadFile(st.Path("state.json"))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("读取能力修改了配置")
	}
}
