package claudecode

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shichao-wang/cpa/internal/config"
	"github.com/shichao-wang/cpa/internal/store"
)

func testStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.New(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Update(func(state *config.State) error {
		state.Listen = "127.0.0.1:8317"
		state.Secrets["secret-a"] = "token-a"
		state.Secrets["secret-b"] = "token-b"
		state.Secrets["upstream-secret"] = "upstream-token"
		state.Connections["mock"] = config.Connection{
			ID: "connection-mock", Name: "mock", AuthType: config.AuthAPIKey, Protocol: config.Anthropic, BaseURL: "https://example.com",
			CredentialRef: "upstream-secret",
			Models:        []config.Model{{ID: "target-opus"}, {ID: "target-sonnet"}, {ID: "target-haiku"}, {ID: "target-fable"}},
		}
		state.Profiles["alpha"] = testProfile("alpha", "profile-a", "secret-a", "alpha")
		state.Profiles["first"] = testProfile("first", "profile-a", "secret-a", "first")
		state.Profiles["second"] = testProfile("second", "profile-b", "secret-b", "second")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return s
}

func testProfile(name, id, keyRef, prefix string) config.Profile {
	models := make(map[string]config.Binding, 4)
	for _, slot := range config.Slots {
		models[slot] = config.Binding{
			PublicModel: "claude-" + prefix + "-" + slot,
			Connection:  "mock",
			TargetModel: "target-" + slot,
			Label:       strings.ToUpper(prefix) + " " + strings.ToUpper(slot),
			Description: "description for " + prefix + " " + slot,
		}
	}
	return config.Profile{Name: name, ID: id, Agent: "claude-code", KeyRef: keyRef, Models: models}
}

func writeDoc(t *testing.T, path string, value any, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, '\n'), mode); err != nil {
		t.Fatal(err)
	}
}

func readDoc(t *testing.T, path string) map[string]any {
	t.Helper()
	snap, err := readSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	return snap.Data
}

func readObject(t *testing.T, root map[string]any, key string) map[string]any {
	t.Helper()
	value, ok := root[key].(map[string]any)
	if !ok {
		t.Fatalf("%s is not an object: %#v", key, root[key])
	}
	return value
}

func assertEqualJSON(t *testing.T, got, want any) {
	t.Helper()
	if !equalJSON(got, want) {
		t.Fatalf("JSON values differ:\n got: %#v\nwant: %#v", got, want)
	}
}

func TestApplyAndRestoreNewSettings(t *testing.T) {
	s := testStore(t)
	path := filepath.Join(t.TempDir(), ".claude", "settings.json")
	profile := testProfile("alpha", "profile-a", "secret-a", "alpha")

	if err := Apply(s, profile, path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("settings permissions = %o, want 600", info.Mode().Perm())
	}
	root := readDoc(t, path)
	env := readObject(t, root, "env")
	if env["ANTHROPIC_BASE_URL"] != "http://127.0.0.1:8317" || env["ANTHROPIC_AUTH_TOKEN"] != "token-a" {
		t.Fatalf("unexpected gateway credentials: %#v", env)
	}
	if _, ok := env["ANTHROPIC_API_KEY"]; ok {
		t.Fatalf("legacy API key was not removed: %#v", env)
	}
	if _, exists := env["CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY"]; exists {
		t.Fatal("apply 不应设置模型发现开关")
	}
	if env["CLAUDE_CODE_SUBAGENT_MODEL"] != profile.Models["haiku"].PublicModel {
		t.Fatalf("subagent model does not use the public Haiku ID: %#v", env)
	}
	for _, key := range []string{"ANTHROPIC_MODEL", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY"} {
		if _, ok := env[key]; ok {
			t.Fatalf("conflicting model source was not cleared: %s=%v", key, env[key])
		}
	}
	for _, slot := range config.Slots {
		prefix := "ANTHROPIC_DEFAULT_" + strings.ToUpper(slot) + "_MODEL"
		binding := profile.Models[slot]
		if env[prefix] != binding.PublicModel {
			t.Fatalf("unexpected %s settings: %#v", slot, env)
		}
		for _, suffix := range []string{"_NAME", "_DESCRIPTION"} {
			if _, exists := env[prefix+suffix]; exists {
				t.Fatalf("apply 不应设置展示环境变量：%s", prefix+suffix)
			}
		}
	}
	picker := readObject(t, root, "modelPicker")
	if picker["replaceBuiltInOptions"] != true {
		t.Fatalf("unexpected model picker toggles: %#v", picker)
	}
	if _, exists := picker["discovery"]; exists {
		t.Fatalf("unsupported modelPicker.discovery field was written: %#v", picker)
	}
	if _, exists := root["model"]; exists {
		t.Fatalf("root model override was not cleared: %#v", root)
	}
	if _, exists := root["apiKeyHelper"]; exists {
		t.Fatalf("root API key helper was not cleared: %#v", root)
	}
	options, ok := picker["options"].([]any)
	if !ok || len(options) != 4 {
		t.Fatalf("unexpected model picker options: %#v", picker["options"])
	}
	if !IsManagedProfile(s, "alpha") || IsManagedProfile(s, "other") {
		t.Fatal("managed profile protection did not match")
	}
	if err := Restore(s, path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("settings created by Apply should be removed after restore, stat err=%v", err)
	}
	if IsManagedProfile(s, "alpha") {
		t.Fatal("profile remained protected after restore")
	}
	if err := Restore(s, path); err == nil {
		t.Fatal("repeated restore should be rejected")
	}
}

func TestApplyDerivesDisplayFromCurrentUpstreamAndSlot(t *testing.T) {
	s := testStore(t)
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := s.Update(func(state *config.State) error {
		connection := state.Connections["mock"]
		connection.Models = append(connection.Models, []config.Model{
			{ID: "gpt-6.1-sol", Name: "GPT-6.1-Sol"},
			{ID: "gpt-6-luna"},
			{ID: "internal-model", Name: "团队模型"},
			{ID: "unknown-model"},
		}...)
		state.Connections["mock"] = connection
		profile := state.Profiles["alpha"]
		for slot, target := range map[string]string{"opus": "gpt-6.1-sol", "sonnet": "internal-model", "haiku": "gpt-6-luna"} {
			binding := profile.Models[slot]
			binding.TargetModel = target
			// 已有 schema 3 状态中的自定义值也不能覆盖自动展示。
			binding.Label, binding.Description = "旧名称", "旧说明"
			profile.Models[slot] = binding
		}
		state.Profiles["alpha"] = profile
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	state, err := s.Read()
	if err != nil {
		t.Fatal(err)
	}
	profile := state.Profiles["alpha"]
	check := func(want map[string][2]string) {
		t.Helper()
		if err := Apply(s, profile, path); err != nil {
			t.Fatal(err)
		}
		root := readDoc(t, path)
		env := readObject(t, root, "env")
		picker := readObject(t, root, "modelPicker")
		options := picker["options"].([]any)
		for slot, display := range want {
			prefix := "ANTHROPIC_DEFAULT_" + strings.ToUpper(slot) + "_MODEL"
			if env[prefix] != profile.Models[slot].PublicModel {
				t.Fatalf("%s 档位映射不符", slot)
			}
			found := false
			for _, option := range options {
				item := option.(map[string]any)
				if item["model"] == profile.Models[slot].PublicModel {
					found = true
					if item["label"] != display[0] || item["description"] != display[1] {
						t.Fatalf("%s 选择器展示不符：%v", slot, item)
					}
				}
			}
			if !found {
				t.Fatalf("选择器缺少 %s 档位", slot)
			}
		}
	}
	check(map[string][2]string{"opus": {"GPT 6.1 Sol", "Opus"}, "sonnet": {"团队模型", "Sonnet"}, "haiku": {"GPT 6 Luna", "Haiku"}})
	if err := s.Update(func(state *config.State) error {
		p := state.Profiles["alpha"]
		binding := p.Models["opus"]
		binding.TargetModel = "gpt-6-luna"
		p.Models["opus"] = binding
		binding = p.Models["sonnet"]
		binding.TargetModel = "unknown-model"
		p.Models["sonnet"] = binding
		state.Profiles["alpha"] = p
		connection := state.Connections["mock"]
		for i := range connection.Models {
			if connection.Models[i].ID == "gpt-6-luna" {
				connection.Models[i].Name = "更新后的模型名称"
			}
		}
		state.Connections["mock"] = connection
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	check(map[string][2]string{"opus": {"更新后的模型名称", "Opus"}, "sonnet": {"unknown-model", "Sonnet"}, "haiku": {"更新后的模型名称", "Haiku"}})
	if err := Restore(s, path); err != nil {
		t.Fatal(err)
	}
}

func legacyDisplayEnv() map[string]any {
	return map[string]any{
		"ANTHROPIC_DEFAULT_OPUS_MODEL_NAME":          "旧 Opus 名称",
		"ANTHROPIC_DEFAULT_OPUS_MODEL_DESCRIPTION":   "旧 Opus 描述",
		"ANTHROPIC_DEFAULT_SONNET_MODEL_NAME":        "旧 Sonnet 名称",
		"ANTHROPIC_DEFAULT_SONNET_MODEL_DESCRIPTION": "旧 Sonnet 描述",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL_NAME":         "旧 Haiku 名称",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL_DESCRIPTION":  "旧 Haiku 描述",
		"ANTHROPIC_DEFAULT_FABLE_MODEL_NAME":         "旧 Fable 名称",
		"ANTHROPIC_DEFAULT_FABLE_MODEL_DESCRIPTION":  "旧 Fable 描述",
		"CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY": "1",
	}
}

func installLegacyApply(t *testing.T, s *store.Store, profile config.Profile, path string) *journal {
	t.Helper()
	if err := Apply(s, profile, path); err != nil {
		t.Fatal(err)
	}
	j := mustJournal(t, s)
	env := readObject(t, j.Applied.Data, "env")
	// 补回旧版实际写入的字段，保留原版管理记录格式和首次接管快照。
	for key, value := range legacyDisplayEnv() {
		j.Managed.Env[key] = fieldChange{
			Original: valueAt(j.Original.Data, "env", key),
			Applied:  fieldValue{Present: true, Value: value},
		}
		env[key] = value
	}
	writeDoc(t, path, j.Applied.Data, 0600)
	if err := writeJournal(s, j); err != nil {
		t.Fatal(err)
	}
	return j
}

func TestApplyPreservesUnmanagedDisplayEnv(t *testing.T) {
	s := testStore(t)
	path := filepath.Join(t.TempDir(), "settings.json")
	baseline := map[string]any{"env": legacyDisplayEnv()}
	writeDoc(t, path, baseline, 0600)
	profile := testProfile("alpha", "profile-a", "secret-a", "alpha")
	for range 2 {
		if err := Apply(s, profile, path); err != nil {
			t.Fatal(err)
		}
		liveEnv := readObject(t, readDoc(t, path), "env")
		for key, value := range legacyDisplayEnv() {
			assertEqualJSON(t, liveEnv[key], value)
		}
	}
	if err := Restore(s, path); err != nil {
		t.Fatal(err)
	}
	assertEqualJSON(t, readDoc(t, path), baseline)
}

func TestLegacyApplyCleanupAndRestore(t *testing.T) {
	for _, originalPresent := range []bool{false, true} {
		for _, operation := range []string{"reapply", "switch", "restore"} {
			t.Run(fmt.Sprintf("original=%t/%s", originalPresent, operation), func(t *testing.T) {
				s := testStore(t)
				path := filepath.Join(t.TempDir(), "settings.json")
				env := map[string]any{"KEEP": "原始值"}
				if originalPresent {
					for key := range legacyDisplayEnv() {
						env[key] = "原始 " + key
					}
				}
				baseline := map[string]any{"env": env, "other": true}
				writeDoc(t, path, baseline, 0600)
				first := testProfile("first", "profile-a", "secret-a", "first")
				installLegacyApply(t, s, first, path)
				before := readDoc(t, path)
				readObject(t, before, "env")["KEEP"] = "用户编辑"
				env["KEEP"] = "用户编辑"
				writeDoc(t, path, before, 0600)
				if operation == "restore" {
					if err := Restore(s, path); err != nil {
						t.Fatal(err)
					}
					assertEqualJSON(t, readDoc(t, path), baseline)
					return
				}
				profile := first
				if operation == "switch" {
					profile = testProfile("second", "profile-b", "secret-b", "second")
				}
				if err := Apply(s, profile, path); err != nil {
					t.Fatal(err)
				}
				live := readDoc(t, path)
				liveEnv := readObject(t, live, "env")
				for key := range legacyDisplayEnv() {
					assertEqualJSON(t, fieldValueOf(liveEnv, key), fieldValueOf(env, key))
					liveEnv[key] = "清理后用户修改 " + key
				}
				writeDoc(t, path, live, 0600)
				// 重复 apply 和切换 profile 都不能再次接管已释放的变量。
				for _, next := range []config.Profile{profile, first} {
					if err := Apply(s, next, path); err != nil {
						t.Fatal(err)
					}
				}
				if err := Restore(s, path); err != nil {
					t.Fatal(err)
				}
				for key := range legacyDisplayEnv() {
					env[key] = "清理后用户修改 " + key
				}
				assertEqualJSON(t, readDoc(t, path), baseline)
			})
		}
	}
}

func TestLegacyDisplayEnvConflictRejectsEntireOperation(t *testing.T) {
	for key := range legacyDisplayEnv() {
		for _, operation := range []string{"apply", "restore"} {
			t.Run(key+"/"+operation, func(t *testing.T) {
				s := testStore(t)
				path := filepath.Join(t.TempDir(), "settings.json")
				profile := testProfile("alpha", "profile-a", "secret-a", "alpha")
				installLegacyApply(t, s, profile, path)
				live := readDoc(t, path)
				readObject(t, live, "env")[key] = "用户修改"
				writeDoc(t, path, live, 0600)
				before := mustJournal(t, s)
				var err error
				if operation == "apply" {
					err = Apply(s, profile, path)
				} else {
					err = Restore(s, path)
				}
				if err == nil {
					t.Fatal("旧受管字段冲突时应拒绝整体操作")
				}
				assertEqualJSON(t, readDoc(t, path), live)
				assertEqualJSON(t, mustJournal(t, s), before)
			})
		}
	}
}

func TestLegacyCreatedSettingsRemovedAfterCleanupAndRestore(t *testing.T) {
	s := testStore(t)
	path := filepath.Join(t.TempDir(), "settings.json")
	profile := testProfile("alpha", "profile-a", "secret-a", "alpha")
	installLegacyApply(t, s, profile, path)
	if err := Apply(s, profile, path); err != nil {
		t.Fatal(err)
	}
	if err := Restore(s, path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("旧版创建的 settings 清理并恢复后应删除：%v", err)
	}
}

func TestInterruptedLegacyApplyCleanup(t *testing.T) {
	for _, phase := range []string{"legacy", "cleanup"} {
		for _, written := range []bool{false, true} {
			for _, operation := range []string{"apply", "restore", "conflict-apply", "conflict-restore"} {
				t.Run(fmt.Sprintf("%s/written=%t/%s", phase, written, operation), func(t *testing.T) {
					s := testStore(t)
					path := filepath.Join(t.TempDir(), "settings.json")
					baseline := map[string]any{"env": map[string]any{
						"ANTHROPIC_DEFAULT_OPUS_MODEL_NAME":          "原始名称",
						"CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY": "0",
					}}
					writeDoc(t, path, baseline, 0600)
					profile := testProfile("alpha", "profile-a", "secret-a", "alpha")
					j := installLegacyApply(t, s, profile, path)
					prior := j.Original.Data
					j.Prior = originalValues(j.Managed)
					if phase == "cleanup" {
						prior = cloneMap(j.Applied.Data)
						j.Prior = appliedValues(j.Managed)
						for key := range legacyDisplayEnv() {
							change := j.Managed.Env[key]
							change.Applied = change.Original
							j.Managed.Env[key] = change
							delete(readObject(t, j.Applied.Data, "env"), key)
						}
						for key, value := range readObject(t, baseline, "env") {
							readObject(t, j.Applied.Data, "env")[key] = value
						}
					}
					j.Status = "pending"
					j.Goal = appliedValues(j.Managed)
					if written {
						writeDoc(t, path, j.Applied.Data, 0600)
					} else {
						writeDoc(t, path, prior, 0600)
					}
					if err := writeJournal(s, j); err != nil {
						t.Fatal(err)
					}
					if strings.HasPrefix(operation, "conflict-") {
						live := readDoc(t, path)
						readObject(t, live, "env")["ANTHROPIC_DEFAULT_OPUS_MODEL_NAME"] = "中断期间用户修改"
						writeDoc(t, path, live, 0600)
						var err error
						if operation == "conflict-apply" {
							err = Apply(s, profile, path)
						} else {
							err = Restore(s, path)
						}
						if err == nil {
							t.Fatal("中断期间旧受管字段冲突时应拒绝续写")
						}
						assertEqualJSON(t, readDoc(t, path), live)
						assertEqualJSON(t, mustJournal(t, s), j)
						return
					}
					if operation == "apply" {
						if err := Apply(s, profile, path); err != nil {
							t.Fatal(err)
						}
						live := readDoc(t, path)
						liveEnv := readObject(t, live, "env")
						for key := range legacyDisplayEnv() {
							assertEqualJSON(t, fieldValueOf(liveEnv, key), valueAt(baseline, "env", key))
							liveEnv[key] = "恢复中断后用户修改 " + key
							readObject(t, baseline, "env")[key] = liveEnv[key]
						}
						writeDoc(t, path, live, 0600)
					}
					if err := Restore(s, path); err != nil {
						t.Fatal(err)
					}
					assertEqualJSON(t, readDoc(t, path), baseline)
				})
			}
		}
	}
}

func TestRestorePreservesUnrelatedEditsAndPickerItems(t *testing.T) {
	s := testStore(t)
	path := filepath.Join(t.TempDir(), "settings.json")
	profile := testProfile("alpha", "profile-a", "secret-a", "alpha")
	opus := profile.Models["opus"].PublicModel
	baseline := map[string]any{
		"env": map[string]any{"KEEP": "before", "ANTHROPIC_API_KEY": "legacy-key", "ANTHROPIC_BASE_URL": "https://old.example"},
		"modelPicker": map[string]any{
			"customToggle": true,
			"options": []any{
				map[string]any{"model": opus, "label": "original label", "description": "original description", "extra": "original extra"},
				map[string]any{"model": "custom-model", "label": "Custom", "description": "User option"},
			},
		},
		"unrelated": map[string]any{"keep": 42},
	}
	writeDoc(t, path, baseline, 0644)
	if err := Apply(s, profile, path); err != nil {
		t.Fatal(err)
	}

	live := readDoc(t, path)
	env := readObject(t, live, "env")
	env["KEEP"] = "edited after apply"
	env["NEW_ENV"] = "keep me"
	picker := readObject(t, live, "modelPicker")
	options := picker["options"].([]any)
	for _, raw := range options {
		if item, ok := raw.(map[string]any); ok && item["model"] == opus {
			item["extra"] = "user edit on same item"
		}
	}
	options = append(options, map[string]any{"model": "user-added-model", "label": "User added", "newField": true})
	picker["options"] = options
	writeDoc(t, path, live, 0600)

	if err := Restore(s, path); err != nil {
		t.Fatal(err)
	}
	restored := readDoc(t, path)
	restoredEnv := readObject(t, restored, "env")
	if restoredEnv["KEEP"] != "edited after apply" || restoredEnv["NEW_ENV"] != "keep me" {
		t.Fatalf("unrelated env edits were lost: %#v", restoredEnv)
	}
	if restoredEnv["ANTHROPIC_API_KEY"] != "legacy-key" || restoredEnv["ANTHROPIC_BASE_URL"] != "https://old.example" {
		t.Fatalf("original auth settings were not restored: %#v", restoredEnv)
	}
	restoredPicker := readObject(t, restored, "modelPicker")
	if restoredPicker["customToggle"] != true {
		t.Fatalf("unrelated model picker field was lost: %#v", restoredPicker)
	}
	restoredOptions := restoredPicker["options"].([]any)
	seenCustom, seenAdded := false, false
	for _, raw := range restoredOptions {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		switch item["model"] {
		case opus:
			if item["label"] != "original label" || item["description"] != "original description" || item["extra"] != "user edit on same item" {
				t.Fatalf("original picker item was not selectively restored: %#v", item)
			}
		case "custom-model":
			seenCustom = true
		case "user-added-model":
			seenAdded = true
		}
	}
	if !seenCustom || !seenAdded {
		t.Fatalf("unrelated picker entries were lost: %#v", restoredOptions)
	}
}

func TestRestoreRejectsManagedFieldConflictWithoutPartialWrite(t *testing.T) {
	s := testStore(t)
	path := filepath.Join(t.TempDir(), "settings.json")
	profile := testProfile("alpha", "profile-a", "secret-a", "alpha")
	writeDoc(t, path, map[string]any{"env": map[string]any{"KEEP": "unchanged"}}, 0600)
	if err := Apply(s, profile, path); err != nil {
		t.Fatal(err)
	}
	before := readDoc(t, path)
	env := readObject(t, before, "env")
	env["ANTHROPIC_AUTH_TOKEN"] = "manual-token"
	writeDoc(t, path, before, 0600)
	changed := readDoc(t, path)
	if err := Restore(s, path); err == nil {
		t.Fatal("restore should reject manual change to a managed field")
	}
	after := readDoc(t, path)
	assertEqualJSON(t, after, changed)
}

func TestProfileSwitchRestoresFirstBaseline(t *testing.T) {
	s := testStore(t)
	path := filepath.Join(t.TempDir(), "settings.json")
	first := testProfile("first", "profile-a", "secret-a", "first")
	second := testProfile("second", "profile-b", "secret-b", "second")
	firstOpus := first.Models["opus"].PublicModel
	baseline := map[string]any{
		"env": map[string]any{"ANTHROPIC_API_KEY": "original-key", "CUSTOM": "value"},
		"modelPicker": map[string]any{"options": []any{
			map[string]any{"model": firstOpus, "label": "baseline label", "description": "baseline description", "origin": "user"},
		}},
		"model":        "claude-previous-model",
		"apiKeyHelper": "credential-helper",
		"other":        "untouched",
	}
	writeDoc(t, path, baseline, 0644)
	if err := Apply(s, first, path); err != nil {
		t.Fatal(err)
	}
	applyIDBefore := mustJournal(t, s).ApplyID
	if err := Apply(s, second, path); err != nil {
		t.Fatal(err)
	}
	j := mustJournal(t, s)
	if j.ApplyID != applyIDBefore || j.ProfileID != second.ID || j.ProfileName != second.Name {
		t.Fatalf("profile switch did not retain one management session: %#v", j)
	}
	live := readDoc(t, path)
	liveEnv := readObject(t, live, "env")
	if liveEnv["ANTHROPIC_AUTH_TOKEN"] != "token-b" || liveEnv["ANTHROPIC_DEFAULT_OPUS_MODEL"] != second.Models["opus"].PublicModel {
		t.Fatalf("second profile was not applied: %#v", liveEnv)
	}
	if err := Restore(s, path); err != nil {
		t.Fatal(err)
	}
	got := readDoc(t, path)
	assertEqualJSON(t, got, baseline)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0644 {
		t.Fatalf("restored permissions = %o, want original 644", info.Mode().Perm())
	}
}

func TestRestoreDoesNotDeleteOriginallyEmptySettings(t *testing.T) {
	s := testStore(t)
	path := filepath.Join(t.TempDir(), "settings.json")
	writeDoc(t, path, map[string]any{}, 0644)
	if err := Apply(s, testProfile("alpha", "profile-a", "secret-a", "alpha"), path); err != nil {
		t.Fatal(err)
	}
	if err := Restore(s, path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("pre-existing empty settings file was removed: %v", err)
	}
	assertEqualJSON(t, readDoc(t, path), map[string]any{})
}

func TestRestoreRequiresManagedRecord(t *testing.T) {
	s := testStore(t)
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := Restore(s, path); err == nil {
		t.Fatal("restore without an Apply record should fail")
	}
	if err := Apply(s, testProfile("alpha", "profile-a", "secret-a", "alpha"), path); err != nil {
		t.Fatal(err)
	}
	if err := Restore(s, path); err != nil {
		t.Fatal(err)
	}
	if err := Restore(s, path); err == nil {
		t.Fatal("repeated restore should fail")
	}
}

func TestApplyRejectsMalformedJSONWithoutChangingIt(t *testing.T) {
	s := testStore(t)
	path := filepath.Join(t.TempDir(), "settings.json")
	original := []byte("{\"env\":")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := Apply(s, testProfile("alpha", "profile-a", "secret-a", "alpha"), path); err == nil {
		t.Fatal("Apply should reject malformed JSON")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Fatalf("malformed settings changed: %q", got)
	}
}

func TestApplyRejectsSettingsSymlink(t *testing.T) {
	s := testStore(t)
	dir := t.TempDir()
	actual := filepath.Join(dir, "actual.json")
	link := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(actual, []byte(`{"keep":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(actual, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := Apply(s, testProfile("alpha", "profile-a", "secret-a", "alpha"), link); err == nil {
		t.Fatal("Apply should reject a settings symlink")
	}
	got, err := os.ReadFile(actual)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"keep":true}` {
		t.Fatalf("symlink target changed: %q", got)
	}
}

func TestInterruptedApplyIsRecoveredBeforeRestore(t *testing.T) {
	s := testStore(t)
	path := filepath.Join(t.TempDir(), "settings.json")
	profile := testProfile("alpha", "profile-a", "secret-a", "alpha")
	if err := Apply(s, profile, path); err != nil {
		t.Fatal(err)
	}
	j := mustJournal(t, s)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	j.Status = "pending"
	j.Action = "apply"
	j.PriorProfileName = ""
	j.Prior = originalValues(j.Managed)
	j.Goal = appliedValues(j.Managed)
	if err := writeJournal(s, j); err != nil {
		t.Fatal(err)
	}
	if err := Restore(s, path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("interrupted apply did not complete and restore cleanly: %v", err)
	}
	if IsManagedProfile(s, profile.Name) {
		t.Fatal("profile remained managed after interrupted operation recovery")
	}
}

func TestApplyUsesClaudeSettingsUnderTemporaryHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	s := testStore(t)
	path := filepath.Join(home, ".claude", "settings.json")
	profile := testProfile("alpha", "profile-a", "secret-a", "alpha")
	if err := Apply(s, profile, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("default settings path was not used under temporary HOME: %v", err)
	}
	if err := Restore(s, ""); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreRejectsUnmanagedSettingsPath(t *testing.T) {
	s := testStore(t)
	path := filepath.Join(t.TempDir(), "settings.json")
	wrongPath := filepath.Join(t.TempDir(), "other.json")
	profile := testProfile("alpha", "profile-a", "secret-a", "alpha")
	if err := Apply(s, profile, path); err != nil {
		t.Fatal(err)
	}
	if err := Restore(s, wrongPath); err == nil {
		t.Fatal("restore at an unmanaged settings path should fail")
	}
	if _, err := os.Stat(path); err != nil || !IsManagedProfile(s, profile.Name) {
		t.Fatalf("mismatched restore affected the managed settings: %v", err)
	}
	if err := Restore(s, path); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreRejectsMalformedSettingsWithoutChangingIt(t *testing.T) {
	s := testStore(t)
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := Apply(s, testProfile("alpha", "profile-a", "secret-a", "alpha"), path); err != nil {
		t.Fatal(err)
	}
	malformed := []byte("{invalid")
	if err := os.WriteFile(path, malformed, 0600); err != nil {
		t.Fatal(err)
	}
	if err := Restore(s, path); err == nil {
		t.Fatal("restore should reject malformed JSON")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(malformed) {
		t.Fatalf("malformed settings changed: %q", got)
	}
}

func TestInterruptedRestoreIsCompletedOnNextRestore(t *testing.T) {
	s := testStore(t)
	path := filepath.Join(t.TempDir(), "settings.json")
	profile := testProfile("alpha", "profile-a", "secret-a", "alpha")
	if err := Apply(s, profile, path); err != nil {
		t.Fatal(err)
	}
	j := mustJournal(t, s)
	j.Status = "pending"
	j.Action = "restore"
	j.Prior = appliedValues(j.Managed)
	j.Goal = originalValues(j.Managed)
	j.Applied = cloneSnapshot(j.Original)
	if err := writeJournal(s, j); err != nil {
		t.Fatal(err)
	}
	// 模拟 settings 已恢复（本例原先不存在，因此目标文件已删除），但进程在
	// 删除管理记录前中断。
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := Restore(s, path); err != nil {
		t.Fatal(err)
	}
	if IsManagedProfile(s, profile.Name) {
		t.Fatal("恢复中断后管理记录未清除")
	}
}

func TestApplyRejectsAuthenticationHeadersInCustomHeaders(t *testing.T) {
	s := testStore(t)
	path := filepath.Join(t.TempDir(), "settings.json")
	original := map[string]any{"env": map[string]any{"ANTHROPIC_CUSTOM_HEADERS": "Authorization: Bearer old-token"}}
	writeDoc(t, path, original, 0600)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := Apply(s, testProfile("alpha", "profile-a", "secret-a", "alpha"), path); err == nil {
		t.Fatal("认证类 custom headers should cause a conflict")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("conflicting custom headers were changed")
	}
}

func TestApplyUsesCurrentStoredProfileAndRejectsStaleIdentity(t *testing.T) {
	s := testStore(t)
	path := filepath.Join(t.TempDir(), "settings.json")
	stale := testProfile("alpha", "profile-a", "revoked-ref", "stale")
	if err := Apply(s, stale, path); err != nil {
		t.Fatal(err)
	}
	if token := readObject(t, readDoc(t, path), "env")["ANTHROPIC_AUTH_TOKEN"]; token != "token-a" {
		t.Fatalf("Apply used a stale caller profile instead of the stored profile: %v", token)
	}
	if err := Restore(s, path); err != nil {
		t.Fatal(err)
	}
	if err := s.Update(func(state *config.State) error {
		profile := state.Profiles["alpha"]
		profile.ID = "replacement-id"
		state.Profiles["alpha"] = profile
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := Apply(s, testProfile("alpha", "profile-a", "secret-a", "alpha"), path); err == nil {
		t.Fatal("Apply should reject a replaced profile with the same name")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("stale profile created settings: %v", err)
	}
}

func TestIsManagedProfileFailsClosedForInvalidJournal(t *testing.T) {
	s := testStore(t)
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := Apply(s, testProfile("alpha", "profile-a", "secret-a", "alpha"), path); err != nil {
		t.Fatal(err)
	}
	j := mustJournal(t, s)
	j.Status = "invalid"
	if err := writeJournal(s, j); err != nil {
		t.Fatal(err)
	}
	if !IsManagedProfile(s, "alpha") {
		t.Fatal("invalid management journal should fail closed")
	}
}

func TestIsManagedProfileUsesProfileIDWhenNamesAreReused(t *testing.T) {
	s := testStore(t)
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := Apply(s, testProfile("alpha", "profile-a", "secret-a", "alpha"), path); err != nil {
		t.Fatal(err)
	}
	if err := s.Update(func(state *config.State) error {
		profile := state.Profiles["alpha"]
		profile.ID = "replacement-id"
		state.Profiles["alpha"] = profile
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if IsManagedProfile(s, "alpha") {
		t.Fatal("management record for an old profile ID blocked its replacement")
	}
}

func mustJournal(t *testing.T, s *store.Store) *journal {
	t.Helper()
	j, err := readJournal(s)
	if err != nil {
		t.Fatal(err)
	}
	if j == nil {
		t.Fatal("management journal is missing")
	}
	return j
}

func TestFableApplySwitchAndRestore(t *testing.T) {
	s := testStore(t)
	binding := config.Binding{PublicModel: "claude-fable-test", Connection: "mock", TargetModel: "target-opus", Label: "自选 Fable", Description: "Fable 映射说明"}
	if err := s.Update(func(state *config.State) error {
		p := state.Profiles["alpha"]
		p.Models["fable"] = binding
		state.Profiles["alpha"] = p
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "settings.json")
	original := map[string]any{"env": map[string]any{"ANTHROPIC_DEFAULT_FABLE_MODEL": "original-fable", "ANTHROPIC_DEFAULT_FABLE_MODEL_NAME": "原展示名"}}
	writeDoc(t, path, original, 0600)
	if err := Apply(s, testProfile("alpha", "profile-a", "secret-a", "alpha"), path); err != nil {
		t.Fatal(err)
	}
	root := readDoc(t, path)
	env := readObject(t, root, "env")
	if env["ANTHROPIC_DEFAULT_FABLE_MODEL"] != binding.PublicModel || env["ANTHROPIC_DEFAULT_FABLE_MODEL_NAME"] != "原展示名" {
		t.Fatal("Fable 别名未应用或未受管的展示变量被修改")
	}
	if _, exists := env["ANTHROPIC_DEFAULT_FABLE_MODEL_DESCRIPTION"]; exists {
		t.Fatal("apply 不应写入 Fable 展示环境变量")
	}
	picker := readObject(t, root, "modelPicker")
	options := picker["options"].([]any)
	if len(options) != 4 {
		t.Fatal("模型菜单未包含四个档位")
	}
	found := false
	for _, option := range options {
		if item, ok := option.(map[string]any); ok && item["model"] == binding.PublicModel {
			found = true
			if item["label"] != "target-opus" || item["description"] != "Fable" {
				t.Fatal("模型菜单未应用 Fable 展示信息")
			}
		}
	}
	if !found {
		t.Fatal("模型菜单缺少 Fable")
	}
	if err := Apply(s, testProfile("second", "profile-b", "secret-b", "second"), path); err != nil {
		t.Fatal(err)
	}
	env = readObject(t, readDoc(t, path), "env")
	if env["ANTHROPIC_DEFAULT_FABLE_MODEL"] != "claude-second-fable" || env["ANTHROPIC_DEFAULT_FABLE_MODEL_NAME"] != "原展示名" {
		t.Fatal("切换 profile 后未更新 Fable 映射")
	}
	root = readDoc(t, path)
	root["unrelated"] = true
	writeDoc(t, path, root, 0600)
	if err := Restore(s, path); err != nil {
		t.Fatal(err)
	}
	original["unrelated"] = true
	assertEqualJSON(t, readDoc(t, path), original)
}
