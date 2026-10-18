package store

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/shichao-wang/cpa/internal/config"
)

func TestStrictStateSchema(t *testing.T) {
	for _, data := range []string{
		`{}`,
		`{"schemaVersion":1,"providers":{},"profiles":{},"secrets":{}}`,
		`{"schemaVersion":2,"providers":{},"connections":{},"profiles":{},"secrets":{}}`,
		`{"schemaVersion":2,"connections":{},"profiles":{},"secrets":{},"unknown":true}`,
		`{"schemaVersion":2,"connections":null,"profiles":{},"secrets":{}}`,
		`{"schemaVersion":2,"connections":{},"profiles":null,"secrets":{}}`,
		`{"schemaVersion":2,"connections":{},"profiles":{},"secrets":{}} {}`,
	} {
		t.Run(data, func(t *testing.T) {
			s := testStore(t)
			if err := AtomicWrite(s.Path("state.json"), []byte(data)); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Read(); err == nil {
				t.Fatal("无效状态不应通过校验")
			}
		})
	}
}

func TestLegacyStateIsNeverOverwritten(t *testing.T) {
	for _, data := range []string{
		`{"schemaVersion":1,"revision":3,"listen":"127.0.0.1:8317","providers":{},"profiles":{},"secrets":{}}`,
		`{"schemaVersion":2,"revision":3,"listen":"127.0.0.1:8317","connections":{},"profiles":{"old":{"models":{"opus":{"provider":"p"}}}},"secrets":{}}`,
	} {
		s := testStore(t)
		original := []byte(data)
		if err := AtomicWrite(s.Path("state.json"), original); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Read(); err == nil || !strings.Contains(err.Error(), "将旧状态目录移至备份位置") {
			t.Fatalf("旧状态错误应提示将旧状态目录移至备份位置：%v", err)
		}
		if err := s.Update(func(st *config.State) error { return nil }); err == nil {
			t.Fatal("旧格式状态不得被覆盖")
		}
		got, err := os.ReadFile(s.Path("state.json"))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != data {
			t.Fatal("拒绝旧格式后原文件字节发生变化")
		}
	}
}

func TestUpdateRollback(t *testing.T) {
	s := testStore(t)
	if err := s.Update(func(st *config.State) error { st.Listen = "127.0.0.1:9999"; return nil }); err != nil {
		t.Fatal(err)
	}
	before, err := s.Read()
	if err != nil {
		t.Fatal(err)
	}
	err = s.Update(func(st *config.State) error { st.Listen = "127.0.0.1:10000"; return config.ValidateName("bad/name") })
	if err == nil {
		t.Fatal("操作应失败")
	}
	after, err := s.Read()
	if err != nil {
		t.Fatal(err)
	}
	if after.Revision != before.Revision || after.Listen != before.Listen {
		t.Fatal("失败事务不应影响持久状态")
	}
}

func TestThreeSlotSchema3RequiresNewStateDirectory(t *testing.T) {
	st := testStore(t)
	data := []byte(`{
		"schemaVersion":3,"revision":7,"listen":"127.0.0.1:8317",
		"connections":{"mock":{"id":"connection-id","name":"mock","authType":"api-key","protocol":"anthropic-messages","baseURL":"https://mock.example","credentialRef":"upstream","models":[{"id":"actual"}]}},
		"profiles":{"daily":{"name":"daily","id":"profile-id","agent":"claude-code","keyRef":"downstream","models":{
			"opus":{"public_model":"claude-opus","connection":"mock","target_model":"actual"},
			"sonnet":{"public_model":"claude-sonnet","connection":"mock","target_model":"actual"},
			"haiku":{"public_model":"claude-haiku","connection":"mock","target_model":"actual"}
		}}},"secrets":{"upstream":"mock-upstream","downstream":"mock-downstream"},"oauthCredentials":{}
	}`)
	if err := AtomicWrite(st.Path("state.json"), data); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Read(); err == nil || !strings.Contains(err.Error(), "将旧状态目录移至备份位置") || !strings.Contains(err.Error(), "原版本") {
		t.Fatalf("旧三档状态应明确提示使用原版本恢复并切换新目录：%v", err)
	}
	called := false
	change := func(state *config.State) error { called = true; state.Listen = "127.0.0.1:9999"; return nil }
	if st.Update(change) == nil || st.UpdateContext(context.Background(), change) == nil || called {
		t.Fatal("旧三档状态不得进入更新事务或被覆盖")
	}
	got, err := os.ReadFile(st.Path("state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(data) {
		t.Fatal("拒绝旧状态后原文件发生变化")
	}
	fresh := testStore(t)
	if err := fresh.Update(func(state *config.State) error {
		if err := json.Unmarshal(data, state); err != nil {
			return err
		}
		state.Profiles["daily"].Models["fable"] = config.Binding{PublicModel: "claude-fable", Connection: "mock", TargetModel: "actual"}
		return nil
	}); err != nil {
		t.Fatalf("新状态目录应可配置四档映射：%v", err)
	}
	state, err := fresh.Read()
	if err != nil || len(state.Profiles["daily"].Models) != 4 {
		t.Fatal("新状态目录未保存四档映射")
	}
}
