package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/shichao-wang/cpa/internal/config"
	"github.com/shichao-wang/cpa/internal/connection"
	"github.com/shichao-wang/cpa/internal/store"
)

func TestDesktopCLIApplySwitchRestoreAlongsideCode(t *testing.T) {
	st, home := desktopCLIStore(t)
	models := desktopCLIYAML(t, "agent: claude-desktop\n")
	for _, args := range [][]string{
		{"profile", "create", "desktop", "--file", models},
		{"profile", "create", "second", "--file", models},
		{"profile", "create", "code", "--file", models, "--agent", "claude-code"},
	} {
		if text, err := desktopCLIRun(args...); err != nil {
			t.Fatalf("创建失败：%v %s", err, text)
		}
	}
	state, err := st.Read()
	if err != nil {
		t.Fatal(err)
	}
	if state.Profiles["desktop"].Agent != "claude-desktop" || state.Profiles["code"].Agent != "claude-code" {
		t.Fatal("YAML Agent 或显式 --agent 未生效")
	}
	settings := filepath.Join(home, "code-settings.json")
	codeOriginal := map[string]any{"model": "original", "permissions": map[string]any{"allow": []any{"Read"}}}
	writeDesktopCLIJSON(t, settings, codeOriginal)
	root := filepath.Join(home, "Desktop Support")
	primary := filepath.Join(root, "Claude", "claude_desktop_config.json")
	writeDesktopCLIJSON(t, primary, map[string]any{"deploymentMode": "1p", "mcpServers": map[string]any{"kept": map[string]any{}}})
	for _, args := range [][]string{
		{"profile", "apply", "code", "--settings", settings},
		{"profile", "apply", "desktop", "--config-dir", root},
		{"profile", "apply", "desktop", "--config-dir", root},
	} {
		if text, err := desktopCLIRun(args...); err != nil {
			t.Fatalf("应用失败：%v %s", err, text)
		}
	}
	metaPath := filepath.Join(root, "Claude-3p", "configLibrary", "_meta.json")
	meta := readDesktopCLIJSON(t, metaPath)
	entry := meta["appliedId"].(string)
	activePath := filepath.Join(filepath.Dir(metaPath), entry+".json")
	active := readDesktopCLIJSON(t, activePath)
	if active["inferenceGatewayApiKey"] != state.Secrets[state.Profiles["desktop"].KeyRef] {
		t.Fatal("Desktop 配置未使用专属下游 key")
	}
	if text, err := desktopCLIRun("profile", "apply", "second", "--config-dir", root); err != nil {
		t.Fatalf("切换失败：%v %s", err, text)
	}
	if readDesktopCLIJSON(t, metaPath)["appliedId"] != entry || readDesktopCLIJSON(t, activePath)["inferenceGatewayApiKey"] != state.Secrets[state.Profiles["second"].KeyRef] {
		t.Fatal("切换应复用受管配置项并更新专属 key")
	}
	if text, err := desktopCLIRun("profile", "delete", "desktop", "--yes"); err != nil {
		t.Fatalf("完成切换后旧 profile 应可删除：%v %s", err, text)
	}
	update := filepath.Join(home, "switch-agent.yaml")
	if err := os.WriteFile(update, []byte("agent: claude-code\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := desktopCLIRun("profile", "update", "second", "--file", update); err == nil {
		t.Fatal("CLI 不应切换受管 profile 的 Agent")
	}
	if _, err := desktopCLIRun("profile", "delete", "second", "--yes"); err == nil {
		t.Fatal("CLI 不应删除受管 Desktop profile")
	}
	if _, err := desktopCLIRun("profile", "delete", "code", "--yes"); err == nil {
		t.Fatal("Desktop 接管期间仍应保护 Code profile")
	}
	current := readDesktopCLIJSON(t, primary)
	current["unrelated"] = "keep after restore"
	writeDesktopCLIJSON(t, primary, current)
	if text, err := desktopCLIRun("profile", "restore", "--agent", "claude-desktop", "--config-dir", root); err != nil {
		t.Fatalf("Desktop 恢复失败：%v %s", err, text)
	}
	wantPrimary := map[string]any{"deploymentMode": "1p", "mcpServers": map[string]any{"kept": map[string]any{}}, "unrelated": "keep after restore"}
	if !reflect.DeepEqual(readDesktopCLIJSON(t, primary), wantPrimary) {
		t.Fatal("Desktop 恢复未保留基线或无关修改")
	}
	if _, err := os.Stat(activePath); !os.IsNotExist(err) {
		t.Fatal("恢复后未删除 cpa 新建配置项")
	}
	if _, err := desktopCLIRun("profile", "delete", "code", "--yes"); err == nil {
		t.Fatal("Desktop 恢复不应解除 Code 接管")
	}
	if text, err := desktopCLIRun("profile", "restore", "--settings", settings); err != nil {
		t.Fatalf("默认 restore 应恢复 Code：%v %s", err, text)
	}
	if !reflect.DeepEqual(readDesktopCLIJSON(t, settings), codeOriginal) {
		t.Fatal("Code 基线未恢复")
	}
	if text, err := desktopCLIRun("profile", "update", "second", "--file", update); err != nil {
		t.Fatalf("恢复后应可切换 Agent：%v %s", err, text)
	}
}

func TestDesktopCLIRejectsWrongAgentFlagsWithoutWriting(t *testing.T) {
	st, home := desktopCLIStore(t)
	models := desktopCLIYAML(t, "")
	for _, name := range []string{"claude-code", "claude-desktop"} {
		if text, err := desktopCLIRun("profile", "create", name, "--agent", name, "--file", models); err != nil {
			t.Fatalf("创建失败：%v %s", err, text)
		}
	}
	settings := filepath.Join(home, ".claude", "settings.json")
	writeDesktopCLIJSON(t, settings, map[string]any{"sentinel": "keep"})
	root := filepath.Join(home, "Desktop Support")
	for _, value := range []string{"", filepath.Join(home, "wrong-path")} {
		for _, args := range [][]string{
			{"profile", "apply", "claude-code", "--config-dir", value},
			{"profile", "apply", "claude-desktop", "--settings", value},
			{"profile", "restore", "--config-dir", value},
			{"profile", "restore", "--agent", "claude-desktop", "--settings", value},
		} {
			if _, err := desktopCLIRun(args...); err == nil {
				t.Fatalf("错误 Agent 参数应拒绝：%v", args)
			}
		}
	}
	if _, err := desktopCLIRun("profile", "restore", "--agent", "unknown", "--config-dir", root); err == nil {
		t.Fatal("未知 Agent 应拒绝")
	}
	if !reflect.DeepEqual(readDesktopCLIJSON(t, settings), map[string]any{"sentinel": "keep"}) {
		t.Fatal("错误参数修改了 Code 配置")
	}
	for _, path := range []string{root, filepath.Join(home, "Library"), st.Path("claudecode"), st.Path("claudedesktop"), filepath.Join(home, "wrong-path")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("错误参数产生了配置：%s", path)
		}
	}
}

func TestDesktopCLICorruptJournalBlocksUnmanagedProfileChanges(t *testing.T) {
	st, home := desktopCLIStore(t)
	models := desktopCLIYAML(t, "")
	if text, err := desktopCLIRun("profile", "create", "free", "--file", models); err != nil {
		t.Fatalf("创建失败：%v %s", err, text)
	}
	if err := os.MkdirAll(st.Path("claudedesktop"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(st.Path("claudedesktop", "settings.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	update := filepath.Join(home, "switch.yaml")
	if err := os.WriteFile(update, []byte("agent: claude-desktop\n"), 0600); err != nil {
		t.Fatal(err)
	}
	before, err := st.Read()
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"profile", "delete", "free", "--yes"}, {"profile", "update", "free", "--file", update}} {
		if _, err := desktopCLIRun(args...); err == nil {
			t.Fatalf("损坏管理记录时必须拒绝：%v", args)
		}
	}
	after, err := st.Read()
	if err != nil {
		t.Fatal(err)
	}
	if after.Revision != before.Revision || !reflect.DeepEqual(after.Profiles, before.Profiles) {
		t.Fatal("拒绝操作后状态发生变化")
	}
}

func desktopCLIStore(t *testing.T) (*store.Store, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := testStateDir(t)
	st, err := store.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := connection.Create(context.Background(), st, config.Connection{Name: "mock", AuthType: config.AuthAPIKey, Protocol: config.Anthropic, BaseURL: "https://example.test", Models: []config.Model{{ID: "model"}}}, "mock-upstream-key"); err != nil {
		t.Fatal(err)
	}
	return st, home
}
func desktopCLIYAML(t *testing.T, prefix string) string {
	t.Helper()
	var data strings.Builder
	data.WriteString(prefix + "models:\n")
	for _, slot := range config.Slots {
		data.WriteString("  " + slot + ":\n    public_model: claude-test-" + slot + "\n    connection: mock\n    target_model: model\n")
	}
	path := filepath.Join(t.TempDir(), "profile.yaml")
	if err := os.WriteFile(path, []byte(data.String()), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
func desktopCLIRun(args ...string) (string, error) {
	cmd := NewAppCommand()
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetIn(strings.NewReader(""))
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(context.Background())
	return output.String(), err
}
func writeDesktopCLIJSON(t *testing.T, path string, value any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}
func readDesktopCLIJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	return value
}
