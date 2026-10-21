package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/shichao-wang/cpa/internal/agent/claudecode"
	"github.com/shichao-wang/cpa/internal/agent/claudedesktop"
	"github.com/shichao-wang/cpa/internal/config"
	"github.com/shichao-wang/cpa/internal/connection"
	"github.com/shichao-wang/cpa/internal/store"
)

func TestDesktopAPIProtectsBothManagedAgentsAndAllowsModelUpdates(t *testing.T) {
	st, h, token := desktopAPISetup(t)
	for _, name := range []string{"claude-code", "claude-desktop"} {
		desktopAPICreateProfile(t, h, token, name, name)
	}
	state, err := st.Read()
	if err != nil {
		t.Fatal(err)
	}
	codePath := filepath.Join(t.TempDir(), "settings.json")
	desktopRoot := t.TempDir()
	if err := claudecode.Apply(st, state.Profiles["claude-code"], codePath); err != nil {
		t.Fatal(err)
	}
	if err := claudedesktop.Apply(st, state.Profiles["claude-desktop"], desktopRoot); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"claude-code", "claude-desktop"} {
		target := "claude-code"
		if name == target {
			target = "claude-desktop"
		}
		before, err := st.Read()
		if err != nil {
			t.Fatal(err)
		}
		for _, call := range []struct{ method, body string }{
			{"DELETE", ""}, {"PATCH", `{"agent":"` + target + `"}`},
		} {
			w := request(t, h, call.method, "/admin/api/profiles/"+name, token, call.body, nil)
			if w.Code != http.StatusConflict {
				t.Fatalf("受管 %s %s 应拒绝，实际 %d", name, call.method, w.Code)
			}
		}
		after, err := st.Read()
		if err != nil {
			t.Fatal(err)
		}
		if after.Revision != before.Revision || !reflect.DeepEqual(after.Profiles, before.Profiles) {
			t.Fatal("API 拒绝操作后状态发生变化")
		}
		old := before.Profiles[name]
		body := `{"agent":"` + name + `","models":{"sonnet":{"public_model":"claude-updated-` + name + `","connection":"mock","target_model":"model"}}}`
		w := request(t, h, "PATCH", "/admin/api/profiles/"+name, token, body, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("相同 Agent 的模型更新应允许：%d %s", w.Code, w.Body.String())
		}
		after, err = st.Read()
		if err != nil {
			t.Fatal(err)
		}
		p := after.Profiles[name]
		if p.Models["sonnet"].PublicModel != "claude-updated-"+name || p.ID != old.ID || p.KeyRef != old.KeyRef || after.Secrets[p.KeyRef] != before.Secrets[old.KeyRef] {
			t.Fatal("API 模型更新未保持身份、专属 key 或更新模型")
		}
	}
	w := request(t, h, "GET", "/admin/api/state", token, "", nil)
	var view struct {
		Profiles map[string]profileView `json:"profiles"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil || w.Code != http.StatusOK {
		t.Fatal("无法读取受管视图")
	}
	for name, p := range state.Profiles {
		if !view.Profiles[name].Managed || view.Profiles[name].Agent != p.Agent {
			t.Fatal("API 状态未显示正确 Agent 和受管状态")
		}
		if strings.Contains(w.Body.String(), state.Secrets[p.KeyRef]) {
			t.Fatal("API 状态泄漏下游 key")
		}
	}
	if err := claudedesktop.Restore(st, desktopRoot); err != nil {
		t.Fatal(err)
	}
	if w := request(t, h, "DELETE", "/admin/api/profiles/claude-code", token, "", nil); w.Code != http.StatusConflict {
		t.Fatal("恢复 Desktop 不应解除 Code 保护")
	}
	if err := claudecode.Restore(st, codePath); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"claude-code", "claude-desktop"} {
		if w := request(t, h, "DELETE", "/admin/api/profiles/"+name, token, "", nil); w.Code != http.StatusOK {
			t.Fatalf("解除接管后删除失败：%d", w.Code)
		}
	}
}

func TestDesktopAPICorruptAdapterJournalFailsClosed(t *testing.T) {
	for _, adapter := range []string{"claudecode", "claudedesktop"} {
		t.Run(adapter, func(t *testing.T) {
			st, h, token := desktopAPISetup(t)
			desktopAPICreateProfile(t, h, token, "free", "claude-code")
			if err := os.MkdirAll(st.Path(adapter), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(st.Path(adapter, "settings.json"), []byte("{"), 0600); err != nil {
				t.Fatal(err)
			}
			before, err := st.Read()
			if err != nil {
				t.Fatal(err)
			}
			for _, call := range []struct{ method, body string }{
				{"DELETE", ""}, {"PATCH", `{"agent":"claude-desktop"}`},
			} {
				w := request(t, h, call.method, "/admin/api/profiles/free", token, call.body, nil)
				if w.Code != http.StatusConflict {
					t.Fatalf("任一管理记录损坏时应拒绝 %s，实际 %d", call.method, w.Code)
				}
			}
			after, err := st.Read()
			if err != nil {
				t.Fatal(err)
			}
			if after.Revision != before.Revision || !reflect.DeepEqual(after.Profiles, before.Profiles) {
				t.Fatal("拒绝操作后状态变化")
			}
			w := request(t, h, "GET", "/admin/api/state", token, "", nil)
			var view struct {
				Profiles map[string]profileView `json:"profiles"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil || !view.Profiles["free"].Managed {
				t.Fatal("无法确认引用时视图应显示受保护")
			}
			w = request(t, h, "PATCH", "/admin/api/profiles/free", token, `{"agent":"claude-code","models":{"sonnet":{"public_model":"claude-updated","connection":"mock","target_model":"model"}}}`, nil)
			if w.Code != http.StatusOK {
				t.Fatal("相同 Agent 的模型更新不应依赖管理记录读取")
			}
			after, err = st.Read()
			if err != nil || after.Profiles["free"].Models["sonnet"].PublicModel != "claude-updated" {
				t.Fatal("相同 Agent 的模型更新未保存")
			}
		})
	}
}

func desktopAPISetup(t *testing.T) (*store.Store, *Handler, string) {
	t.Helper()
	st, h, token := setup(t)
	if err := connection.Create(context.Background(), st, config.Connection{Name: "mock", AuthType: config.AuthAPIKey, Protocol: config.Anthropic, BaseURL: "https://example.test", Models: []config.Model{{ID: "model"}}}, "mock-upstream-key"); err != nil {
		t.Fatal(err)
	}
	return st, h, token
}
func desktopAPICreateProfile(t *testing.T, h *Handler, token, name, targetAgent string) {
	t.Helper()
	models := map[string]config.Binding{}
	for _, slot := range config.Slots {
		models[slot] = config.Binding{PublicModel: "claude-test-" + slot, Connection: "mock", TargetModel: "model"}
	}
	data, err := json.Marshal(map[string]any{"name": name, "agent": targetAgent, "models": models})
	if err != nil {
		t.Fatal(err)
	}
	w := request(t, h, "POST", "/admin/api/profiles", token, string(data), nil)
	if w.Code != http.StatusOK {
		t.Fatalf("API 创建 %s 失败：%d %s", targetAgent, w.Code, w.Body.String())
	}
}
