package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLooksLikeJSONCDistinguishesTheOldFormat guards the migration hint: it
// must fire for a file that would parse as JSONC, and stay quiet for every
// other failure so a real syntax error is not blamed on comments.
func TestLooksLikeJSONCDistinguishesTheOldFormat(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"plain json", `{"a": 1}`, false},
		{"url is not a comment", `{"baseUrl": "http://127.0.0.1:8317"}`, false},
		{"line comment", "{\n  // note\n  \"a\": 1\n}", true},
		{"block comment", `{/* why */ "a": 1}`, true},
		{"trailing comma", `{"a": 1,}`, true},
		{"genuinely broken", `{"a": 1`, false},
		{"not json at all", `hello`, false},
	}
	for _, tc := range cases {
		if got := looksLikeJSONC([]byte(tc.in)); got != tc.want {
			t.Errorf("%s: looksLikeJSONC = %v, want %v", tc.name, got, tc.want)
		}
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

func TestCandidatesFollowXDG(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	t.Setenv("XDG_CONFIG_HOME", "")
	got := Candidates()
	want := filepath.Join(home, ".config", "cpa", "settings.json")
	if len(got) == 0 || got[0] != want {
		t.Errorf("without XDG_CONFIG_HOME: Candidates()[0] = %q, want %q", got, want)
	}

	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	got = Candidates()
	want = filepath.Join(xdg, "cpa", "settings.json")
	if len(got) == 0 || got[0] != want {
		t.Errorf("with XDG_CONFIG_HOME: Candidates()[0] = %q, want %q", got, want)
	}

	// The pre-XDG location must not be consulted at all. Project-local
	// candidates are relative (".cpa/settings.json"), so match on the
	// rooted form only.
	for _, p := range got {
		if strings.Contains(p, string(filepath.Separator)+".cpa"+string(filepath.Separator)) {
			t.Errorf("legacy ~/.cpa path is still a candidate: %q", got)
		}
	}
}

func TestUserConfigPathIsNotTheLegacyLocation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")

	if UserConfigPath() == LegacyConfigPath() {
		t.Fatalf("UserConfigPath() must differ from LegacyConfigPath(), both %q", UserConfigPath())
	}
	if got := LegacyConfigPath(); got != filepath.Join(home, ".cpa", "settings.json") {
		t.Errorf("LegacyConfigPath() = %q", got)
	}
}
