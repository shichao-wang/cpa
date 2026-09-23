package main

import (
	"reflect"
	"testing"
)

func TestParseFlagsProfileAliases(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		wantProfile string
		wantRest    []string
	}{
		{
			name:        "short alias",
			args:        []string{"-p", "deepseek"},
			wantProfile: "deepseek",
		},
		{
			name:        "short alias with equals",
			args:        []string{"-p=deepseek"},
			wantProfile: "deepseek",
		},
		{
			name:        "long option remains supported",
			args:        []string{"--profile", "deepseek"},
			wantProfile: "deepseek",
		},
		{
			name:        "long option with equals remains supported",
			args:        []string{"--profile=deepseek"},
			wantProfile: "deepseek",
		},
		{
			name:        "agent prompt flag after separator",
			args:        []string{"-p", "deepseek", "--", "-p", "explain this repo"},
			wantProfile: "deepseek",
			wantRest:    []string{"-p", "explain this repo"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseFlags(tt.args)
			if err != nil {
				t.Fatalf("parseFlags() error = %v", err)
			}
			if got.profile != tt.wantProfile {
				t.Errorf("profile = %q, want %q", got.profile, tt.wantProfile)
			}
			if !reflect.DeepEqual(got.rest, tt.wantRest) {
				t.Errorf("rest = %#v, want %#v", got.rest, tt.wantRest)
			}
		})
	}
}

func TestParseFlagsProfileAliasRequiresValue(t *testing.T) {
	if _, err := parseFlags([]string{"-p"}); err == nil {
		t.Fatal("parseFlags() error = nil, want missing-value error")
	}
}
