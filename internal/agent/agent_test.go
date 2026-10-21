package agent_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/shichao-wang/cpa/internal/agent"
	"github.com/shichao-wang/cpa/internal/agent/claudecode"
	"github.com/shichao-wang/cpa/internal/agent/claudedesktop"
	"github.com/shichao-wang/cpa/internal/config"
	"github.com/shichao-wang/cpa/internal/profile"
	"github.com/shichao-wang/cpa/internal/store"
)

func TestBothAdaptersProtectStableProfileIDs(t *testing.T) {
	st, profiles := managedProfiles(t)
	references := func(id string) (bool, error) { return agent.ReferencesProfileID(st, id) }
	for _, p := range profiles {
		if found, err := references(p.ID); err != nil || !found {
			t.Fatalf("%s 管理引用未被汇总：%v %v", p.Agent, found, err)
		}
		if err := profile.Remove(st, p.Name, references); err == nil {
			t.Fatalf("不应删除仍被 %s 引用的 profile", p.Agent)
		}
	}
	if found, err := references("unmanaged-id"); err != nil || found {
		t.Fatalf("未受管理的稳定 ID 被保护：%v %v", found, err)
	}
	// 即使旧状态绕过领域入口更改了 agent，两个适配器的记录仍须保护相同稳定 ID。
	if err := st.Update(func(state *config.State) error {
		for name, p := range state.Profiles {
			if p.Agent == "claude-code" {
				p.Agent = "claude-desktop"
			} else {
				p.Agent = "claude-code"
			}
			state.Profiles[name] = p
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, p := range profiles {
		if !agent.IsManagedProfile(st, p.Name) {
			t.Fatalf("%s 的引用被当前 agent 隐藏", p.Name)
		}
		originalAgent := p.Agent
		if err := profile.Update(st, p.Name, profile.File{Agent: &originalAgent}, references); err == nil {
			t.Fatal("仍有跨适配器引用时不应允许切换 agent")
		}
	}
}

func TestAnyUnreadableAdapterBlocksProfileChanges(t *testing.T) {
	for _, dir := range []string{"claudecode", "claudedesktop"} {
		t.Run(dir, func(t *testing.T) {
			st, profiles := managedProfiles(t)
			if err := os.WriteFile(st.Path(dir, "settings.json"), []byte("{"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := agent.ReferencesProfileID(st, "unmanaged-id"); err == nil {
				t.Fatal("任一管理记录损坏时必须返回错误")
			}
			for _, p := range profiles {
				if !agent.IsManagedProfile(st, p.Name) {
					t.Fatal("读取错误时受管状态必须保守处理")
				}
			}
		})
	}
}

func managedProfiles(t *testing.T) (*store.Store, []config.Profile) {
	t.Helper()
	st, err := store.New(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	profiles := []config.Profile{}
	if err := st.Update(func(state *config.State) error {
		state.Secrets["upstream"] = "mock-upstream-key"
		state.Connections["mock"] = config.Connection{ID: "connection", Name: "mock", AuthType: config.AuthAPIKey, Protocol: config.Anthropic, BaseURL: "https://example.test", CredentialRef: "upstream", Models: []config.Model{{ID: "model"}}}
		for _, name := range []string{"claude-code", "claude-desktop"} {
			p := config.Profile{ID: name + "-id", Name: name, Agent: name, KeyRef: name + "-key", Models: map[string]config.Binding{}}
			for _, slot := range config.Slots {
				p.Models[slot] = config.Binding{PublicModel: "claude-" + name + "-" + slot, Connection: "mock", TargetModel: "model"}
			}
			state.Profiles[name] = p
			state.Secrets[p.KeyRef] = "mock-" + name + "-key"
			profiles = append(profiles, p)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := claudecode.Apply(st, profiles[0], filepath.Join(t.TempDir(), "settings.json")); err != nil {
		t.Fatal(err)
	}
	if err := claudedesktop.Apply(st, profiles[1], t.TempDir()); err != nil {
		t.Fatal(err)
	}
	return st, profiles
}
