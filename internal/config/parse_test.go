package config

import "testing"

func TestHasCommentsSeesOnlyRealComments(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"plain json", `{"a": 1}`, false},
		{"url is not a comment", `{"baseUrl": "http://127.0.0.1:8317"}`, false},
		{"line comment", "{\n  // pick the fast one\n  \"a\": 1\n}", true},
		{"block comment", `{/* why */ "a": 1}`, true},
	}
	for _, tc := range cases {
		if got := HasComments([]byte(tc.in)); got != tc.want {
			t.Errorf("%s: HasComments = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestParseRawDoesNotMergeNeighbours(t *testing.T) {
	// ParseRaw reads exactly one document. If it went through Load, the
	// project-local profile below would appear and then be written back into
	// the user's file on the next upsert.
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	doc := []byte(`{
  // our own file
  "defaultProfile": "mine",
  "profiles": {"mine": {"baseUrl": "http://a"}}
}`)
	raw, err := ParseRaw(doc)
	if err != nil {
		t.Fatalf("ParseRaw: %v", err)
	}
	if got := raw["defaultProfile"]; got != "mine" {
		t.Errorf("defaultProfile = %v, want mine", got)
	}
	profiles, ok := raw["profiles"].(map[string]interface{})
	if !ok {
		t.Fatalf("profiles is %T, want object", raw["profiles"])
	}
	if len(profiles) != 1 {
		t.Errorf("profiles has %d entries, want 1: %v", len(profiles), profiles)
	}
}

func TestParseMatchesLoadForOneDocument(t *testing.T) {
	doc := []byte(`{"defaultProfile": "x", "profiles": {"x": {"baseUrl": "http://a",},}}`)
	cfg, err := Parse(doc)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.DefaultProfile != "x" || cfg.Profiles["x"].BaseURL != "http://a" {
		t.Errorf("Parse lost data: %+v", cfg)
	}
}
