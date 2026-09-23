package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestStripJSONCLeavesURLsAlone(t *testing.T) {
	in := `{
  // a line comment
  "baseUrl": "http://127.0.0.1:8317", // trailing comment
  /* block
     comment */
  "a": "has // inside a string",
  "b": "escaped \" // not a comment",
  "list": [1, 2, 3,],
}`
	var got map[string]interface{}
	if err := json.Unmarshal(stripJSONC([]byte(in)), &got); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if got["baseUrl"] != "http://127.0.0.1:8317" {
		t.Errorf("baseUrl mangled: %v", got["baseUrl"])
	}
	if got["a"] != "has // inside a string" {
		t.Errorf("string content mangled: %v", got["a"])
	}
	if got["b"] != `escaped " // not a comment` {
		t.Errorf("escape handling mangled: %v", got["b"])
	}
	if n := len(got["list"].([]interface{})); n != 3 {
		t.Errorf("trailing comma broke the array: %d elements", n)
	}
}

func TestLoadMergesProjectOverUser(t *testing.T) {
	dir := t.TempDir()
	user := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(user, []byte(`{
	  "defaultProfile": "deepseek",
	  "profiles": {
	    "deepseek": {"baseUrl": "http://user", "family": "deepseek"},
	    "local":    {"baseUrl": "http://user-local"}
	  }
	}`), 0o600); err != nil {
		t.Fatal(err)
	}

	loaded, err := loadFile(user)
	if err != nil {
		t.Fatal(err)
	}
	project, err := loadFile(writeTemp(t, dir, `{
	  "defaultProfile": "local",
	  "profiles": {"local": {"baseUrl": "http://project"}}
	}`))
	if err != nil {
		t.Fatal(err)
	}

	loaded.Merge(project)
	if loaded.DefaultProfile != "local" {
		t.Errorf("defaultProfile = %q, want local", loaded.DefaultProfile)
	}
	if got := loaded.Profiles["local"].BaseURL; got != "http://project" {
		t.Errorf("project profile should win, got %q", got)
	}
	if got := loaded.Profiles["deepseek"].BaseURL; got != "http://user" {
		t.Errorf("user-only profile should survive, got %q", got)
	}
}

func writeTemp(t *testing.T, dir, content string) string {
	t.Helper()
	path := filepath.Join(dir, "project.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestResolveAPIKeyIndirections(t *testing.T) {
	t.Setenv("CPA_TEST_KEY", "from-env")

	cases := []struct {
		name    string
		profile Profile
		want    string
		wantErr bool
	}{
		{"literal", Profile{APIKey: "plain"}, "plain", false},
		{"env shorthand", Profile{APIKey: "env:CPA_TEST_KEY"}, "from-env", false},
		{"missing env shorthand", Profile{APIKey: "env:CPA_NOPE"}, "", true},
		{"apiKeyEnv", Profile{APIKeyEnv: "CPA_TEST_KEY"}, "from-env", false},
		{"apiKeyCmd", Profile{APIKeyCmd: "echo cmd-key"}, "cmd-key", false},
		{"failing cmd", Profile{APIKeyCmd: "exit 3"}, "", true},
		{"none is allowed", Profile{}, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.profile.ResolveAPIKey()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
