package claudedesktop

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/shichao-wang/cpa/internal/config"
	"github.com/shichao-wang/cpa/internal/store"
)

func fixture(t *testing.T) (*store.Store, string, config.Profile, config.Profile) {
	t.Helper()
	s, err := store.New(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	first := testProfile("first", "id-first", "key-first")
	second := testProfile("second", "id-second", "key-second")
	err = s.Update(func(st *config.State) error {
		st.Connections["mock"] = config.Connection{ID: "conn", Name: "mock", Protocol: config.Anthropic, AuthType: config.AuthAPIKey, BaseURL: "https://example.com", CredentialRef: "upstream", Models: []config.Model{{ID: "target"}}}
		st.Secrets["upstream"] = "do-not-copy-upstream"
		st.Secrets[first.KeyRef] = "downstream-first"
		st.Secrets[second.KeyRef] = "downstream-second"
		st.Profiles[first.Name] = first
		st.Profiles[second.Name] = second
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return s, filepath.Join(t.TempDir(), "support"), first, second
}
func testProfile(name, id, key string) config.Profile {
	p := config.Profile{Name: name, ID: id, KeyRef: key, Agent: "claude-desktop", Models: map[string]config.Binding{}}
	for _, slot := range config.Slots {
		p.Models[slot] = config.Binding{PublicModel: "claude-" + name + "-" + slot, Connection: "mock", TargetModel: "target"}
	}
	return p
}
func put(t *testing.T, root, name string, data any) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, b, 0600); err != nil {
		t.Fatal(err)
	}
}
func get(t *testing.T, root, name string) map[string]any {
	t.Helper()
	snap, err := readFile(root, name)
	if err != nil {
		t.Fatal(err)
	}
	return snap.Data
}
func requireEqual(t *testing.T, got, want any) {
	t.Helper()
	if !equal(got, want) {
		t.Fatalf("got %#v; want %#v", got, want)
	}
}
func mustJournal(t *testing.T, s *store.Store) *journal {
	t.Helper()
	j, err := readJournal(s)
	if err != nil || j == nil {
		t.Fatalf("missing journal: %v", err)
	}
	return j
}

func TestApplySwitchAndRestorePreserveUserConfiguration(t *testing.T) {
	s, root, first, second := fixture(t)
	priorID := "11111111-1111-4111-8111-111111111111"
	originalMeta := map[string]any{"appliedId": priorID, "entries": []any{map[string]any{"id": priorID, "name": "Existing"}}, "hybridPointer": map[string]any{"bootstrapUrl": "https://example.com/bootstrap"}, "other": "keep"}
	put(t, root, metaFile, originalMeta)
	put(t, root, "Claude-3p/configLibrary/"+priorID+".json", map[string]any{"private": "do-not-change"})
	put(t, root, primaryModeFile, map[string]any{"deploymentMode": "1p", "mcpServers": map[string]any{"local": map[string]any{"command": "keep"}}})
	history := filepath.Join(root, "Claude-3p", "sessions", "keep.json")
	if err := os.MkdirAll(filepath.Dir(history), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(history, []byte("history"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Apply(s, first, root); err != nil {
		t.Fatal(err)
	}
	j := mustJournal(t, s)
	id := j.EntryID
	originalApplyID := j.ApplyID
	conf := get(t, root, configFile(j))
	requireEqual(t, conf["inferenceGatewayBaseUrl"], "http://127.0.0.1:8317")
	requireEqual(t, conf["inferenceGatewayApiKey"], "downstream-first")
	requireEqual(t, conf["inferenceGatewayAuthScheme"], "bearer")
	requireEqual(t, conf["chatTabEnabled"], true)
	requireEqual(t, conf["isClaudeCodeForDesktopEnabled"], true)
	requireEqual(t, conf["coworkTabEnabled"], false)
	requireEqual(t, conf["modelDiscoveryEnabled"], false)
	models := conf["inferenceModels"].([]any)
	if len(models) != 4 {
		t.Fatal(models)
	}
	for i, slot := range []string{"sonnet", "opus", "haiku", "fable"} {
		m := models[i].(map[string]any)
		requireEqual(t, m["name"], "claude-first-"+slot)
		requireEqual(t, m["anthropicFamilyTier"], slot)
		if len(m) != 3 {
			t.Fatalf("unsupported capability claim: %#v", m)
		}
	}
	all, _ := json.Marshal(j)
	if strings.Contains(string(all), "do-not-copy-upstream") {
		t.Fatal("upstream credential copied")
	}
	if err := Apply(s, first, root); err != nil {
		t.Fatal(err)
	}
	if err := Apply(s, second, root); err != nil {
		t.Fatal(err)
	}
	j = mustJournal(t, s)
	if j.EntryID != id || j.ApplyID != originalApplyID {
		t.Fatal("lost original baseline")
	}
	requireEqual(t, get(t, root, configFile(j))["inferenceGatewayApiKey"], "downstream-second")
	protected, err := ReferencesProfileID(s, first.ID)
	if err != nil || protected {
		t.Fatalf("old profile retained: %v %v", protected, err)
	}
	protected, err = ReferencesProfileID(s, second.ID)
	if err != nil || !protected {
		t.Fatalf("current profile unprotected: %v %v", protected, err)
	}
	// 模拟 Desktop 增加另一个配置项、MCP 设置及自有项中的无关字段。
	meta := get(t, root, metaFile)
	extra := map[string]any{"id": "22222222-2222-4222-8222-222222222222", "name": "Added later"}
	meta["entries"] = append(meta["entries"].([]any), extra)
	meta["newUserSetting"] = true
	put(t, root, metaFile, meta)
	mode := get(t, root, primaryModeFile)
	mode["other"] = true
	put(t, root, primaryModeFile, mode)
	conf = get(t, root, configFile(j))
	conf["customUserField"] = "keep"
	put(t, root, configFile(j), conf)
	if err := Restore(s, root); err != nil {
		t.Fatal(err)
	}
	meta = get(t, root, metaFile)
	requireEqual(t, meta["appliedId"], priorID)
	requireEqual(t, meta["hybridPointer"], originalMeta["hybridPointer"])
	requireEqual(t, meta["entries"], []any{originalMeta["entries"].([]any)[0], extra})
	requireEqual(t, meta["newUserSetting"], true)
	mode = get(t, root, primaryModeFile)
	requireEqual(t, mode["deploymentMode"], "1p")
	requireEqual(t, mode["other"], true)
	if mode["mcpServers"] == nil {
		t.Fatal("MCP removed")
	}
	requireEqual(t, get(t, root, configFile(j)), map[string]any{"customUserField": "keep"})
	if b, err := os.ReadFile(history); err != nil || string(b) != "history" {
		t.Fatal("history changed")
	}
	if _, err := os.Stat(journalPath(s)); !os.IsNotExist(err) {
		t.Fatalf("journal remains: %v", err)
	}
}

func TestRestoreRemovesOnlyFilesCreatedByApply(t *testing.T) {
	s, root, first, _ := fixture(t)
	if err := Apply(s, first, root); err != nil {
		t.Fatal(err)
	}
	j := mustJournal(t, s)
	for _, name := range fileNames(j) {
		info, err := os.Stat(filepath.Join(root, name))
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("permissions: %s %v", name, err)
		}
	}
	info, err := os.Stat(filepath.Dir(journalPath(s)))
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("journal directory permissions: %v", err)
	}
	if err := Restore(s, root); err != nil {
		t.Fatal(err)
	}
	for _, name := range fileNames(j) {
		if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Fatalf("created file remains: %s %v", name, err)
		}
	}
	if err := Restore(s, root); err == nil {
		t.Fatal("restore without successful apply accepted")
	}
}

func TestConflictRejectsWholeRestoreAndApply(t *testing.T) {
	for _, target := range []string{primaryModeFile, thirdPartyModeFile, metaFile, "config"} {
		t.Run(target, func(t *testing.T) {
			s, root, first, second := fixture(t)
			if err := Apply(s, first, root); err != nil {
				t.Fatal(err)
			}
			j := mustJournal(t, s)
			name := target
			if name == "config" {
				name = configFile(j)
			}
			doc := get(t, root, name)
			switch name {
			case primaryModeFile, thirdPartyModeFile:
				doc["deploymentMode"] = "1p"
			case metaFile:
				doc["appliedId"] = ""
			default:
				doc["inferenceGatewayBaseUrl"] = "http://localhost:9999"
			}
			put(t, root, name, doc)
			before := treeBytes(t, root, j)
			if err := Restore(s, root); err == nil {
				t.Fatal("conflict accepted")
			}
			requireEqual(t, treeBytes(t, root, j), before)
			if err := Apply(s, second, root); err == nil {
				t.Fatal("apply conflict accepted")
			}
			requireEqual(t, treeBytes(t, root, j), before)
		})
	}
}
func treeBytes(t *testing.T, root string, j *journal) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, name := range fileNames(j) {
		b, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		out[name] = string(b)
	}
	return out
}

func TestRejectsUnrecognizedOrUnsafeConfigurationBeforeWriting(t *testing.T) {
	for _, kind := range []string{"broken-json", "null", "bad-entries", "bad-mode", "missing-active", "symlink-file", "symlink-directory", "instance-lock"} {
		t.Run(kind, func(t *testing.T) {
			s, root, first, _ := fixture(t)
			if err := os.MkdirAll(filepath.Join(root, "Claude-3p", "configLibrary"), 0700); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "broken-json", "null":
				data := []byte("{")
				if kind == "null" {
					data = []byte("null")
				}
				if err := os.WriteFile(filepath.Join(root, metaFile), data, 0600); err != nil {
					t.Fatal(err)
				}
			case "bad-entries":
				put(t, root, metaFile, map[string]any{"entries": "unknown"})
			case "bad-mode":
				put(t, root, primaryModeFile, map[string]any{"deploymentMode": "other"})
			case "missing-active":
				put(t, root, metaFile, map[string]any{"appliedId": "11111111-1111-4111-8111-111111111111"})
			case "symlink-file":
				target := filepath.Join(t.TempDir(), "target.json")
				if err := os.WriteFile(target, []byte("{}"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, filepath.Join(root, metaFile)); err != nil {
					t.Fatal(err)
				}
			case "symlink-directory":
				if err := os.Remove(filepath.Join(root, "Claude-3p", "configLibrary")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(t.TempDir(), filepath.Join(root, "Claude-3p", "configLibrary")); err != nil {
					t.Fatal(err)
				}
			case "instance-lock":
				if err := os.Symlink("host-123", filepath.Join(root, "Claude-3p", "SingletonLock")); err != nil {
					t.Fatal(err)
				}
			}
			if err := Apply(s, first, root); err == nil {
				t.Fatal("unsafe source accepted")
			}
			if _, err := os.Stat(journalPath(s)); !os.IsNotExist(err) {
				t.Fatalf("unsafe source wrote journal: %v", err)
			}
			if _, err := os.Stat(filepath.Join(root, thirdPartyModeFile)); !os.IsNotExist(err) {
				t.Fatal("partial write before validation")
			}
		})
	}
}

func TestCorruptManagementRecordsFailClosed(t *testing.T) {
	s, root, first, _ := fixture(t)
	if err := Apply(s, first, root); err != nil {
		t.Fatal(err)
	}
	j := mustJournal(t, s)
	before := treeBytes(t, root, j)
	for _, data := range []string{"{", "{}", `{"version":999}`} {
		if err := os.WriteFile(journalPath(s), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := ReferencesProfileID(s, first.ID); err == nil {
			t.Fatal("corrupt journal did not fail closed")
		}
		if !IsManagedProfile(s, first.Name) {
			t.Fatal("corrupt journal did not protect profile")
		}
		if err := Apply(s, first, root); err == nil {
			t.Fatal("corrupt journal replaced")
		}
		if err := Restore(s, root); err == nil {
			t.Fatal("corrupt journal restored")
		}
		requireEqual(t, treeBytes(t, root, j), before)
	}
}

func TestStoredAgentAndIdentityAreAuthoritative(t *testing.T) {
	s, root, first, _ := fixture(t)
	wrong := first
	wrong.ID = "stale"
	if err := Apply(s, wrong, root); err == nil {
		t.Fatal("stale ID accepted")
	}
	if err := s.Update(func(st *config.State) error {
		p := st.Profiles[first.Name]
		p.Agent = "claude-code"
		st.Profiles[p.Name] = p
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := Apply(s, first, root); err == nil {
		t.Fatal("stale agent accepted")
	}
}

func TestRejectsDirectorySwitchAndUnsafeGateway(t *testing.T) {
	s, root, first, _ := fixture(t)
	if err := Apply(s, first, root); err != nil {
		t.Fatal(err)
	}
	if err := Apply(s, first, t.TempDir()); err == nil {
		t.Fatal("managed root changed")
	}
	if err := Restore(s, t.TempDir()); err == nil {
		t.Fatal("restored another root")
	}
	for _, listen := range []string{"0.0.0.0:8317", "https://127.0.0.1:8317", "127.0.0.1:", "127.0.0.1:8317/v1", "127.0.0.1:8317?x=y"} {
		if _, err := gatewayURL(listen); err == nil {
			t.Fatalf("invalid Desktop gateway accepted: %s", listen)
		}
	}
}

func TestManagedPolicySourcesAreNeverOverwritten(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "com.anthropic.claudefordesktop.plist")
	if err := rejectManagedPaths([]string{path}); err != nil {
		t.Fatal(err)
	}
	original := []byte("managed policy, contents must not be parsed or overwritten")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := rejectManagedPaths([]string{path}); err == nil {
		t.Fatal("managed source accepted")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(original) {
		t.Fatal("managed policy changed")
	}
}

func TestCorruptPendingGoalCannotWriteFiles(t *testing.T) {
	s, root, first, _ := fixture(t)
	if err := Apply(s, first, root); err != nil {
		t.Fatal(err)
	}
	j := mustJournal(t, s)
	before := treeBytes(t, root, j)
	j.Status = "pending"
	for _, r := range j.Files {
		r.Prior = applied(r)
		r.Goal = applied(r)
	}
	j.Files[primaryModeFile].Goal["deploymentMode"] = value{Present: true, Data: "1p"}
	if err := writeJournal(s, j); err != nil {
		t.Fatal(err)
	}
	if _, err := ReferencesProfileID(s, first.ID); err == nil {
		t.Fatal("corrupt pending goal accepted")
	}
	if err := Restore(s, root); err == nil {
		t.Fatal("corrupt pending goal applied")
	}
	requireEqual(t, treeBytes(t, root, j), before)
}

func TestOriginalSnapshotCorruptionFailsClosed(t *testing.T) {
	for _, kind := range []string{"extra-prior", "extra-goal", "original-fields", "original-exists", "original-shape"} {
		t.Run(kind, func(t *testing.T) {
			s, root, first, _ := fixture(t)
			if err := Apply(s, first, root); err != nil {
				t.Fatal(err)
			}
			j := mustJournal(t, s)
			before := treeBytes(t, root, j)
			switch kind {
			case "extra-prior", "extra-goal":
				j.Status = "pending"
				for _, r := range j.Files {
					r.Prior = applied(r)
					r.Goal = applied(r)
				}
				if kind == "extra-prior" {
					j.Files[primaryModeFile].Prior["not-managed"] = value{}
				} else {
					j.Files[primaryModeFile].Goal["not-managed"] = value{}
				}
			case "original-fields":
				r := j.Files[primaryModeFile]
				ch := r.Fields["deploymentMode"]
				ch.Original = value{Present: true, Data: "1p"}
				r.Fields["deploymentMode"] = ch
			case "original-exists":
				j.Files[configFile(j)].Original.Exists = true
			case "original-shape":
				j.Files[metaFile].Original.Data["entries"] = "bad"
			}
			if err := writeJournal(s, j); err != nil {
				t.Fatal(err)
			}
			if _, err := ReferencesProfileID(s, first.ID); err == nil {
				t.Fatal("corrupt management record accepted")
			}
			if err := Restore(s, root); err == nil {
				t.Fatal("corrupt record restored")
			}
			requireEqual(t, treeBytes(t, root, j), before)
		})
	}
}

func TestRunningDesktopUsesTheSelectedRoot(t *testing.T) {
	root := "/private/tmp/test-support"
	for _, dir := range []string{"Claude", "Claude-3p"} {
		command := "/Applications/Claude.app/Contents/MacOS/Claude --user-data-dir=" + root + "/" + dir
		if err := rejectRunningProcesses(command, root, false); err == nil {
			t.Fatal("running selected root accepted")
		}
	}
	command := "/Applications/Claude.app/Contents/MacOS/Claude --user-data-dir=/private/tmp/other/Claude-3p"
	if err := rejectRunningProcesses(command, root, false); err != nil {
		t.Fatal("unrelated temporary instance blocked fixture")
	}
	if err := rejectRunningProcesses(command, root, true); err == nil {
		t.Fatal("running real Desktop accepted")
	}
	command = "/Applications/Claude.app/Contents/MacOS/Claude --user-data-dir=" + root + "/Claude-other"
	if err := rejectRunningProcesses(command, root, false); err != nil {
		t.Fatal("different directory with same prefix blocked")
	}
}

var errInjected = errors.New("注入配置事务故障")

func interruptBoundary(cut int) transactionHook {
	written := 0
	return func(stage, name string) error {
		if stage == "before-files" && cut == 0 {
			return errInjected
		}
		if stage == "after-file" {
			written++
			if written == cut {
				return errInjected
			}
		}
		return nil
	}
}

// 通过真实事务函数注入错误，不拼造持久日志或提前写入预期结果。
func TestActualOperationsRecoverEveryFileBoundary(t *testing.T) {
	for _, action := range []string{"first-apply", "switch", "restore"} {
		for cut := 0; cut <= 4; cut++ {
			t.Run(fmt.Sprintf("%s/%d", action, cut), func(t *testing.T) {
				s, root, first, second := fixture(t)
				original := map[string]any{"deploymentMode": "1p", "mcpServers": map[string]any{"existing": true}}
				put(t, root, primaryModeFile, original)
				if action != "first-apply" {
					if err := Apply(s, first, root); err != nil {
						t.Fatal(err)
					}
				}
				var err error
				target := first
				switch action {
				case "first-apply":
					err = apply(s, first, root, interruptBoundary(cut))
				case "switch":
					target = second
					err = apply(s, second, root, interruptBoundary(cut))
				case "restore":
					err = restore(s, root, interruptBoundary(cut))
				}
				if !errors.Is(err, errInjected) {
					t.Fatalf("fault did not interrupt real operation: %v", err)
				}
				j := mustJournal(t, s)
				if j.Status != "pending" {
					t.Fatal("interrupted operation lost durable pending state")
				}
				found, err := ReferencesProfileID(s, target.ID)
				if err != nil || !found {
					t.Fatalf("pending target unprotected: %v %v", found, err)
				}
				if action == "switch" {
					found, err = ReferencesProfileID(s, first.ID)
					if err != nil || !found {
						t.Fatalf("pending prior unprotected: %v %v", found, err)
					}
				}
				// 第一个文件的真实落盘结果可以区分写入前失败和第1..4个边界失败。
				conf := get(t, root, configFile(j))
				switch action {
				case "first-apply":
					if cut == 0 {
						if len(conf) != 0 {
							t.Fatal("failure before first write changed config")
						}
					} else {
						requireEqual(t, conf["inferenceGatewayApiKey"], "downstream-first")
					}
				case "switch":
					want := "downstream-first"
					if cut > 0 {
						want = "downstream-second"
					}
					requireEqual(t, conf["inferenceGatewayApiKey"], want)
				case "restore":
					if cut == 0 {
						requireEqual(t, conf["inferenceGatewayApiKey"], "downstream-first")
					} else if len(conf) != 0 {
						t.Fatal("first restore boundary did not remove managed config")
					}
				}
				if action == "restore" {
					if err := Restore(s, root); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := Apply(s, target, root); err != nil {
						t.Fatal(err)
					}
					want := "downstream-first"
					if action == "switch" {
						want = "downstream-second"
					}
					requireEqual(t, get(t, root, configFile(j))["inferenceGatewayApiKey"], want)
					if err := Restore(s, root); err != nil {
						t.Fatal(err)
					}
				}
				requireEqual(t, get(t, root, primaryModeFile), original)
				for _, name := range []string{configFile(j), metaFile, thirdPartyModeFile} {
					if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
						t.Fatalf("first baseline not restored: %s %v", name, err)
					}
				}
				if _, err := os.Stat(journalPath(s)); !os.IsNotExist(err) {
					t.Fatal("successful recovery retained journal")
				}
			})
		}
	}
}

func TestActualFaultAfterDirectoryAndPermissionPreparation(t *testing.T) {
	for _, action := range []string{"apply", "restore"} {
		for _, prepare := range []string{"mkdir", "chmod"} {
			t.Run(action+"/"+prepare, func(t *testing.T) {
				s, root, first, _ := fixture(t)
				original := map[string]any{"deploymentMode": "1p", "user": "keep"}
				put(t, root, primaryModeFile, original)
				if err := os.Chmod(filepath.Join(root, primaryModeFile), 0644); err != nil {
					t.Fatal(err)
				}
				if action == "restore" {
					if err := Apply(s, first, root); err != nil {
						t.Fatal(err)
					}
				}
				var interruptedName string
				hook := func(stage, name string) error {
					if stage != "prepared" {
						return nil
					}
					if prepare == "mkdir" && !strings.HasPrefix(name, "Claude-3p/configLibrary/") {
						return nil
					}
					if prepare == "chmod" && name != primaryModeFile {
						return nil
					}
					interruptedName = name
					return errInjected
				}
				var err error
				if action == "apply" {
					err = apply(s, first, root, hook)
				} else {
					err = restore(s, root, hook)
				}
				if !errors.Is(err, errInjected) || interruptedName == "" {
					t.Fatalf("preparation fault did not fire: %v", err)
				}
				j := mustJournal(t, s)
				if j.Status != "pending" {
					t.Fatal("preparation failure lost journal")
				}
				if prepare == "mkdir" {
					info, err := os.Stat(filepath.Join(root, "Claude-3p", "configLibrary"))
					if err != nil || !info.IsDir() {
						t.Fatal("directory preparation did not run")
					}
					if action == "apply" {
						if _, err := os.Stat(filepath.Join(root, configFile(j))); !os.IsNotExist(err) {
							t.Fatal("config renamed before injected error")
						}
					}
				}
				if prepare == "chmod" {
					info, err := os.Stat(filepath.Join(root, primaryModeFile))
					if err != nil || info.Mode().Perm() != 0600 {
						t.Fatal("permission preparation did not run")
					}
					want := "1p"
					if action == "restore" {
						want = "3p"
					}
					requireEqual(t, get(t, root, primaryModeFile)["deploymentMode"], want)
				}
				found, err := ReferencesProfileID(s, first.ID)
				if err != nil || !found {
					t.Fatal("preparation failure did not protect profile")
				}
				if action == "apply" {
					if err := Apply(s, first, root); err != nil {
						t.Fatal(err)
					}
				}
				if err := Restore(s, root); err != nil {
					t.Fatal(err)
				}
				requireEqual(t, get(t, root, primaryModeFile), original)
				info, err := os.Stat(filepath.Join(root, primaryModeFile))
				if err != nil || info.Mode().Perm() != 0644 {
					t.Fatal("original permissions not restored")
				}
			})
		}
	}
}

func TestRejectsCustomSymlinkInConfigurationRootAncestors(t *testing.T) {
	s, _, first, _ := fixture(t)
	parent := t.TempDir()
	real := filepath.Join(parent, "real")
	root := filepath.Join(real, "support")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(parent, "alias")
	if err := os.Symlink(real, alias); err != nil {
		t.Fatal(err)
	}
	selected := filepath.Join(alias, "support")
	if _, err := ResolveConfigDir(selected); err == nil {
		t.Fatal("custom ancestor alias resolved")
	}
	if err := Apply(s, first, selected); err == nil {
		t.Fatal("custom ancestor alias accepted")
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatal("rejected alias wrote real configuration")
	}
	if _, err := os.Stat(journalPath(s)); !os.IsNotExist(err) {
		t.Fatal("rejected alias wrote management journal")
	}
	if err := Apply(s, first, root); err != nil {
		t.Fatal(err)
	}
	if err := Restore(s, selected); err == nil {
		t.Fatal("restore accepted custom ancestor alias")
	}
	if err := Restore(s, root); err != nil {
		t.Fatal(err)
	}
}

func TestSystemTemporaryDirectoryAliasIsCanonical(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS 系统路径别名")
	}
	for _, prefix := range []string{"/tmp", "/var"} {
		resolved, err := ResolveConfigDir(filepath.Join(prefix, "cpa-not-created", "support"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(resolved, "/private"+prefix+"/") {
			t.Fatalf("system alias not canonical: %s", resolved)
		}
	}
}

func TestDefaultConfigRootIgnoresHomeEnvironmentOverrides(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS 默认 Desktop 根目录")
	}
	before, err := ResolveConfigDir("")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())
	after, err := ResolveConfigDir("")
	if err != nil {
		t.Fatal(err)
	}
	if after != before || !isDefaultRoot(after, after) {
		t.Fatal("HOME override changed system Desktop root or policy guard")
	}
}

func TestRunningDesktopRecognizesSystemDirectoryAliases(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS 系统路径别名")
	}
	for _, prefix := range []string{"/tmp", "/var"} {
		for _, dir := range []string{"Claude", "Claude-3p"} {
			root := prefix + "/cpa-support"
			physicalRoot := "/private" + root
			for _, selected := range []string{root, physicalRoot} {
				for _, running := range []string{root, physicalRoot} {
					command := "/Applications/Claude.app/Contents/MacOS/Claude --user-data-dir=" + running + "/" + dir
					if err := rejectRunningProcesses(command, selected, false); err == nil {
						t.Fatalf("running alias accepted: selected=%s running=%s", selected, running)
					}
					if err := rejectRunningProcesses(command+" --disable-gpu", selected, false); err == nil {
						t.Fatal("running alias with following argument accepted")
					}
					if err := rejectRunningProcesses(command+"-other", selected, false); err != nil {
						t.Fatal("different directory with same prefix blocked")
					}
				}
			}
		}
	}
	// 只转换完整的系统路径组件，不能把相似目录当作系统别名。
	command := "/Applications/Claude.app/Contents/MacOS/Claude --user-data-dir=/tmp-other/cpa-support/Claude-3p"
	if err := rejectRunningProcesses(command, "/private/tmp-other/cpa-support", false); err != nil {
		t.Fatal("non-system prefix treated as alias")
	}
}
