package main

import (
	"context"
	"reflect"
	"testing"

	"github.com/shichao-wang/cpa/internal/config"
	"github.com/shichao-wang/cpa/internal/proxy"
)

func TestParseFallbackModels(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  []string
		valid bool
	}{
		{"", nil, true}, {"  ", nil, true},
		{" sonnet , haiku ", []string{"sonnet", "haiku"}, true},
		{"a,b,c", []string{"a", "b", "c"}, true},
		{"a,", nil, false}, {",a", nil, false}, {"a,,b", nil, false},
		{"a,a", nil, false}, {"a,b,c,d", nil, false},
	} {
		got, err := parseFallbackModels(tc.input)
		if (err == nil) != tc.valid || (tc.valid && !reflect.DeepEqual(got, tc.want)) {
			t.Errorf("parseFallbackModels(%q) = %v, %v; want %v, valid=%v", tc.input, got, err, tc.want, tc.valid)
		}
	}
}

func TestProfileFormFallbackBack(t *testing.T) {
	q := &scriptedQuestions{answers: []formAnswer{
		{label: "Profile name", text: "test"},
		{label: "Description (optional)"},
		{label: agentQuestion, text: "claude"},
		{label: "Gateway base URL", text: "http://gateway"},
		{label: keyQuestion},
		{label: fallbackQuestion, back: true},
		{label: keyQuestion},
		{label: fallbackQuestion, text: "sonnet, haiku"},
	}}
	name := ""
	p := &config.Profile{Agent: "claude"}
	form := &profileForm{ctx: context.Background(), pr: q, name: &name, profile: p, noDiscover: true,
		lookup: func(context.Context, *config.Profile) ([]proxy.Model, string) {
			t.Fatal("discovery should be skipped")
			return nil, ""
		},
	}
	if err := form.run(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p.FallbackModel, []string{"sonnet", "haiku"}) || q.backs != 1 {
		t.Errorf("fallback=%v backs=%d", p.FallbackModel, q.backs)
	}
}
