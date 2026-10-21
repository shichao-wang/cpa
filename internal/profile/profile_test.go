package profile

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/shichao-wang/cpa/internal/config"
	"github.com/shichao-wang/cpa/internal/store"
)

func TestCreateDesktopProfileAndRejectUnknownAgent(t *testing.T) {
	st := testStore(t)
	if err := seedConnection(st); err != nil {
		t.Fatal(err)
	}
	for _, agent := range []string{"claude-code", "claude-desktop"} {
		file := completeFile(agent)
		file.Agent = stringPointer(agent)
		if _, err := Create(st, file); err != nil {
			t.Fatalf("合法 agent %s 创建失败：%v", agent, err)
		}
	}
	file := completeFile("unknown")
	file.Agent = stringPointer("unknown")
	if _, err := Create(st, file); err == nil {
		t.Fatal("未知 agent 不应创建")
	}
	state, err := st.Read()
	if err != nil {
		t.Fatal(err)
	}
	if state.Profiles["claude-code"].KeyRef == state.Profiles["claude-desktop"].KeyRef {
		t.Fatal("不同 agent 必须拥有独立的 profile key")
	}
}

func TestAgentChangeRequiresUnreferencedStableID(t *testing.T) {
	for _, test := range []struct {
		name  string
		check func(string) (bool, error)
	}{
		{"missing-check", nil},
		{"referenced", func(string) (bool, error) { return true, nil }},
		{"read-error", func(string) (bool, error) { return false, fmt.Errorf("管理记录损坏") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			st := testStore(t)
			if err := seedConnection(st); err != nil {
				t.Fatal(err)
			}
			if _, err := Create(st, completeFile("demo")); err != nil {
				t.Fatal(err)
			}
			before, err := st.Read()
			if err != nil {
				t.Fatal(err)
			}
			if err := Update(st, "demo", File{Agent: stringPointer("claude-desktop")}, test.check); err == nil {
				t.Fatal("无法确认未受引用时必须拒绝切换 agent")
			}
			after, err := st.Read()
			if err != nil {
				t.Fatal(err)
			}
			if after.Profiles["demo"].Agent != "claude-code" || after.Revision != before.Revision {
				t.Fatal("拒绝切换后状态发生变化")
			}
		})
	}
	st := testStore(t)
	if err := seedConnection(st); err != nil {
		t.Fatal(err)
	}
	key, err := Create(st, completeFile("demo"))
	if err != nil {
		t.Fatal(err)
	}
	before, err := st.Read()
	if err != nil {
		t.Fatal(err)
	}
	old := before.Profiles["demo"]
	called := false
	if err := Update(st, "demo", File{Agent: stringPointer("claude-desktop")}, func(id string) (bool, error) {
		called = true
		if id != old.ID {
			t.Fatalf("应按稳定 ID 查询引用，实际为 %q", id)
		}
		return false, nil
	}); err != nil {
		t.Fatal(err)
	}
	after, err := st.Read()
	if err != nil {
		t.Fatal(err)
	}
	p := after.Profiles["demo"]
	if !called || p.Agent != "claude-desktop" || p.ID != old.ID || p.KeyRef != old.KeyRef || after.Secrets[p.KeyRef] != key {
		t.Fatal("未受引用 profile 未切换 agent 或丢失身份凭证")
	}
	if err := Update(st, "demo", File{Agent: stringPointer("claude-desktop")}, func(string) (bool, error) {
		t.Fatal("相同 agent 的更新不应查询配置引用")
		return false, fmt.Errorf("不可读取")
	}); err != nil {
		t.Fatal(err)
	}
}

func TestParseFileRejectsUnknownFieldsAndExtraDocuments(t *testing.T) {
	for _, input := range []string{
		"name: demo\nkeyRef: secret-ref\n",
		"name: demo\nmodels:\n  sonnet:\n    label: 自定义名称\n",
		"name: demo\nmodels:\n  sonnet:\n    description: 自定义说明\n",
		"name: demo\nmodels: {}\nunknown: value\n",
		"name: demo\n---\nname: second\n",
		"name: demo\nmodels:\n  sonnet:\n    public_model: claude-test\n    provider: p\n    connection: c\n    target_model: m\n",
		"name: demo\nmodels:\n  sonnet:\n    public_model: claude-test\n    connection: c\n    target_model: m\n    credentialRef: should-not-be-here\n",
	} {
		if _, err := ParseFile(strings.NewReader(input)); err == nil {
			t.Errorf("应拒绝未知字段或多文档 YAML：%q", input)
		}
	}
}

func TestCreateGeneratesPrivateKeyAndUpdatePreservesUnspecifiedFields(t *testing.T) {
	st := testStore(t)
	if err := seedConnection(st); err != nil {
		t.Fatal(err)
	}
	file := completeFile("demo")
	key, err := Create(st, file)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(key, "cpa_") {
		t.Fatalf("生成 key 前缀错误：%q", key)
	}
	s, err := st.Read()
	if err != nil {
		t.Fatal(err)
	}
	p := s.Profiles["demo"]
	if s.Secrets[p.KeyRef] != key {
		t.Fatal("本地 key 未被保存到生成的引用中")
	}
	id, keyRef := p.ID, p.KeyRef
	oldOpus := p.Models["opus"]
	newSonnet := config.Binding{
		PublicModel: "claude-updated-sonnet",
		Connection:  "default",
		TargetModel: "model-a",
	}
	update := File{Models: map[string]config.Binding{"sonnet": newSonnet}}
	if err := Update(st, "demo", update, nil); err != nil {
		t.Fatal(err)
	}
	s, err = st.Read()
	if err != nil {
		t.Fatal(err)
	}
	updated := s.Profiles["demo"]
	if updated.ID != id || updated.KeyRef != keyRef || s.Secrets[keyRef] != key {
		t.Fatal("更新 profile 时 ID 或本地 key 被重置")
	}
	if updated.Models["opus"] != oldOpus || updated.Models["sonnet"] != newSonnet {
		t.Fatal("更新 profile 未保留未指定档位或未应用指定档位")
	}
}

func TestCreateValidatesAllSlotsAndReferences(t *testing.T) {
	st := testStore(t)
	if err := seedConnection(st); err != nil {
		t.Fatal(err)
	}
	for _, slot := range config.Slots {
		file := completeFile("demo")
		delete(file.Models, slot)
		if _, err := Create(st, file); err == nil {
			t.Fatalf("缺少 %s 档位的 profile 不应创建", slot)
		}
	}
	file := completeFile("demo")
	b := file.Models["sonnet"]
	b.TargetModel = "not-registered"
	file.Models["sonnet"] = b
	if _, err := Create(st, file); err == nil {
		t.Fatal("引用未登记模型的 profile 不应创建")
	}
	file = completeFile("demo")
	b = file.Models["sonnet"]
	b.PublicModel = file.Models["opus"].PublicModel
	file.Models["sonnet"] = b
	if _, err := Create(st, file); err == nil {
		t.Fatal("重复 public model ID 不应创建")
	}
}

func TestRemoveKeepsSharedKeyReference(t *testing.T) {
	st := testStore(t)
	if err := seedConnection(st); err != nil {
		t.Fatal(err)
	}
	key, err := Create(st, completeFile("demo"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := st.Read()
	if err != nil {
		t.Fatal(err)
	}
	first := s.Profiles["demo"]
	second := first
	second.Name = "other"
	if err := st.Update(func(state *config.State) error { state.Profiles["other"] = second; return nil }); err != nil {
		t.Fatal(err)
	}
	wasChecked := false
	if err := Remove(st, "demo", func(id string) (bool, error) {
		wasChecked = true
		if id != first.ID {
			t.Errorf("agent 引用检查拿到意外 ID：%s", id)
		}
		return true, nil
	}); err == nil {
		t.Fatal("被 agent 引用的 profile 不应删除")
	}
	if !wasChecked {
		t.Fatal("未执行 agent 引用检查")
	}
	if err := Remove(st, "demo", func(string) (bool, error) { return false, nil }); err != nil {
		t.Fatal(err)
	}
	s, err = st.Read()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Secrets[first.KeyRef]; !ok || s.Secrets[first.KeyRef] != key {
		t.Fatal("共享 profile key 引用不应被提前回收")
	}
	if err := Remove(st, "other", func(string) (bool, error) { return false, nil }); err != nil {
		t.Fatal(err)
	}
	s, err = st.Read()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Secrets[first.KeyRef]; ok {
		t.Fatal("最后一个 profile 移除后 key 应回收")
	}
}

func testStore(t *testing.T) *store.Store {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	st, err := store.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func seedConnection(st *store.Store) error {
	return st.Update(func(s *config.State) error {
		s.Secrets["test-key"] = "upstream-key"
		s.Connections["default"] = config.Connection{
			ID: "profile-test-connection", AuthType: config.AuthAPIKey, Name: "default", Protocol: config.Chat,
			BaseURL: "https://api.example.test", CredentialRef: "test-key",
			Models: []config.Model{{ID: "model-a"}, {ID: "model-b"}},
		}
		return nil
	})
}

func completeFile(name string) File {
	return File{
		Name:  stringPointer(name),
		Agent: stringPointer("claude-code"),
		Models: map[string]config.Binding{
			"opus":   {PublicModel: "claude-example-opus", Connection: "default", TargetModel: "model-a"},
			"sonnet": {PublicModel: "claude-example-sonnet", Connection: "default", TargetModel: "model-a"},
			"haiku":  {PublicModel: "claude-example-haiku", Connection: "default", TargetModel: "model-b"},
			"fable":  {PublicModel: "claude-example-fable", Connection: "default", TargetModel: "model-a"},
		},
	}
}

func stringPointer(value string) *string { return &value }

func TestUpdateFablePreservesExistingProfile(t *testing.T) {
	st := testStore(t)
	if err := seedConnection(st); err != nil {
		t.Fatal(err)
	}
	key, err := Create(st, completeFile("demo"))
	if err != nil {
		t.Fatal(err)
	}
	before, err := st.Read()
	if err != nil {
		t.Fatal(err)
	}
	file, err := ParseFile(strings.NewReader("models:\n  fable:\n    public_model: claude-fable\n    connection: default\n    target_model: model-b\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := Update(st, "demo", file, nil); err != nil {
		t.Fatal(err)
	}
	after, err := st.Read()
	if err != nil {
		t.Fatal(err)
	}
	p := after.Profiles["demo"]
	if len(p.Models) != 4 || p.Models["fable"] != file.Models["fable"] || p.ID != before.Profiles["demo"].ID || after.Secrets[p.KeyRef] != key {
		t.Fatal("更新 Fable 未保存或改变了 profile 身份")
	}
	for _, slot := range []string{"opus", "sonnet", "haiku"} {
		if p.Models[slot] != before.Profiles["demo"].Models[slot] {
			t.Fatalf("更新 Fable 改变了 %s", slot)
		}
	}
}
