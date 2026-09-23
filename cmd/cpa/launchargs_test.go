package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseLaunchArgs(t *testing.T) {
	tests := []struct {
		name          string
		args          []string
		agent         string
		profile       string
		dryRun        bool
		noDiscover    bool
		allowConflict bool
		rest          []string
	}{
		{name: "bare agent", args: []string{"claude"}, agent: "claude"},
		{name: "short profile and agent prompt", args: []string{"-p", "deepseek", "claude", "-p", "explain this repo"}, agent: "claude", profile: "deepseek", rest: []string{"-p", "explain this repo"}},
		{name: "short profile with equals", args: []string{"-p=deepseek", "claude", "--resume"}, agent: "claude", profile: "deepseek", rest: []string{"--resume"}},
		{name: "long profile and agent flags", args: []string{"--profile=deepseek", "--dry-run", "--no-discover", "--allow-settings-conflict", "claude", "--profile", "other", "--dry-run", "--resume"}, agent: "claude", profile: "deepseek", dryRun: true, noDiscover: true, allowConflict: true, rest: []string{"--profile", "other", "--dry-run", "--resume"}},
		{name: "separate long profile", args: []string{"--profile", "gpt", "codex", "--", "-p", "prompt"}, agent: "codex", profile: "gpt", rest: []string{"--", "-p", "prompt"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent, f, err := parseLaunchArgs(tt.args)
			if err != nil {
				t.Fatal(err)
			}
			if agent != tt.agent || f.profile != tt.profile || f.dryRun != tt.dryRun || f.noDiscover != tt.noDiscover || f.allowSettingsConflict != tt.allowConflict || !reflect.DeepEqual(f.rest, tt.rest) {
				t.Errorf("parseLaunchArgs(%q) = %q, %+v; want agent=%q profile=%q rest=%q", tt.args, agent, f, tt.agent, tt.profile, tt.rest)
			}
		})
	}
}

func TestParseLaunchArgsErrors(t *testing.T) {
	for _, tt := range []struct {
		args []string
		want string
	}{
		{nil, "missing agent"},
		{[]string{"--profile"}, "needs a value"},
		{[]string{"-p"}, "needs a value"},
		{[]string{"-p", "deepseek"}, "missing agent"},
		{[]string{"--unknown", "claude"}, "unknown cpa flag"},
	} {
		_, _, err := parseLaunchArgs(tt.args)
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("parseLaunchArgs(%q) error = %v; want %q", tt.args, err, tt.want)
		}
	}
}
