package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseRejectsCommentsAndTrailingCommas(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"line comment", "{\n  // pick the fast one\n  \"a\": 1\n}"},
		{"block comment", `{/* why */ "a": 1}`},
		{"trailing comma", `{"a": 1,}`},
	}
	for _, tc := range cases {
		if _, err := Parse([]byte(tc.in)); err == nil {
			t.Errorf("%s: Parse accepted non-JSON input", tc.name)
		}
	}
}

func TestCommentErrorNamesTheCause(t *testing.T) {
	// The bare json error here is `invalid character '/'`, which tells a user
	// nothing. Settings used to be JSONC, so this failure needs explaining.
	_, err := Parse([]byte("{\n  // a leftover comment\n  \"a\": 1\n}"))
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "plain JSON") {
		t.Errorf("error does not explain the format change: %v", err)
	}
}

func TestGenuineSyntaxErrorsAreNotBlamedOnComments(t *testing.T) {
	// A missing brace must not be reported as a comment problem.
	_, err := Parse([]byte(`{"a": 1`))
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "plain JSON") {
		t.Errorf("misleading message for a real syntax error: %v", err)
	}
}

func TestURLsInStringsAreNotMistakenForComments(t *testing.T) {
	cfg, err := Parse([]byte(`{"profiles": {"a": {"baseUrl": "http://127.0.0.1:8317"}}}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := cfg.Profiles["a"].BaseURL; got != "http://127.0.0.1:8317" {
		t.Errorf("baseUrl = %q", got)
	}
}

func TestParseRawDoesNotMergeNeighbours(t *testing.T) {
	// ParseRaw reads exactly one document. If it went through Load, the
	// project-local profile below would appear and then be written back into
	// the user's file on the next upsert.
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	doc := []byte(`{
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

// jsonTags lists the JSON names a struct declares, so a drift check can
// compare them against the schema without a hand-maintained list.
func jsonTags(v interface{}) map[string]bool {
	out := map[string]bool{}
	t := reflect.TypeOf(v)
	for i := 0; i < t.NumField(); i++ {
		tag := t.Field(i).Tag.Get("json")
		name, _, _ := strings.Cut(tag, ",")
		if name != "" && name != "-" {
			out[name] = true
		}
	}
	return out
}

// TestSchemaCoversEveryField keeps the published schema from drifting away
// from the struct it describes: every json tag config knows about must be
// documented, and the schema must not invent fields that do not exist.
func TestSchemaCoversEveryField(t *testing.T) {
	path := filepath.Join("..", "..", "examples", "settings.schema.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	schema, err := ParseRaw(data)
	if err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}

	objProps := func(v interface{}) map[string]bool {
		out := map[string]bool{}
		obj, _ := v.(map[string]interface{})
		for k := range obj {
			out[k] = true
		}
		return out
	}
	compare := func(what string, declared map[string]bool, documented map[string]bool) {
		for key := range declared {
			if !documented[key] {
				t.Errorf("%s field %q is missing from the schema", what, key)
			}
		}
		for key := range documented {
			if !declared[key] {
				t.Errorf("schema documents %s.%s, which the struct does not have", what, key)
			}
		}
	}

	compare("Config", jsonTags(Config{}), objProps(schema["properties"]))

	defs, _ := schema["definitions"].(map[string]interface{})
	profile, _ := defs["profile"].(map[string]interface{})
	if profile == nil {
		t.Fatal("schema has no definitions.profile")
	}
	compare("Profile", jsonTags(Profile{}), objProps(profile["properties"]))
}

// TestExamplesAreStrictJSON guards the shipped examples, which cpa's own
// parser must be able to read.
func TestExamplesAreStrictJSON(t *testing.T) {
	for _, name := range []string{"settings.json", "settings.schema.json"} {
		path := filepath.Join("..", "..", "examples", name)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if _, err := ParseRaw(data); err != nil {
			t.Errorf("%s is not plain JSON: %v", name, err)
		}
	}
}
