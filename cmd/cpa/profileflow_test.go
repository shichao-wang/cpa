package main

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/shichao-wang/cpa/internal/config"
	"github.com/shichao-wang/cpa/internal/prompt"
	"github.com/shichao-wang/cpa/internal/proxy"
)

type formAnswer struct {
	label string
	text  string
	index int
	back  bool
	abort bool
}

type scriptedQuestions struct {
	answers  []formAnswer
	calls    []string
	backs    int
	sections int
}

func (s *scriptedQuestions) next(label string) (formAnswer, error) {
	if len(s.answers) == 0 {
		return formAnswer{}, fmt.Errorf("unexpected question %q", label)
	}
	a := s.answers[0]
	s.answers = s.answers[1:]
	if a.label != label {
		return a, fmt.Errorf("question = %q, want %q", label, a.label)
	}
	s.calls = append(s.calls, label)
	if a.back {
		return a, prompt.ErrBack
	}
	if a.abort {
		return a, prompt.ErrInterrupted
	}
	return a, nil
}

func (s *scriptedQuestions) Input(label, _ string, validate func(string) error) (string, error) {
	a, err := s.next(label)
	if err != nil {
		return "", err
	}
	if validate != nil {
		if err := validate(a.text); err != nil {
			return "", err
		}
	}
	return a.text, nil
}
func (s *scriptedQuestions) ChooseDefault(label string, options []string, _ int) (int, error) {
	a, err := s.next(label)
	if err != nil {
		return 0, err
	}
	if a.index >= len(options) {
		return 0, fmt.Errorf("choice %d missing for %q", a.index, label)
	}
	return a.index, nil
}
func (s *scriptedQuestions) SetIndent(int)  {}
func (s *scriptedQuestions) Section(string) { s.sections++ }
func (s *scriptedQuestions) Back()          { s.backs++ }

func TestProfileFormBackAndRediscover(t *testing.T) {
	q := &scriptedQuestions{answers: []formAnswer{
		{label: "Profile name", back: true}, // first Esc stays here
		{label: "Profile name", text: "devbox"},
		{label: "Description (optional)"},
		{label: "Gateway base URL", text: "http://first"},
		{label: "API key (optional; env:NAME and cmd:... also work)"},
		{label: "Upstream family", index: 1}, // first family
		{label: "opus", index: 1},
		{label: "sonnet", back: true},
		{label: "opus", back: true},
		{label: "Upstream family", back: true},
		{label: "API key (optional; env:NAME and cmd:... also work)", back: true},
		{label: "Gateway base URL", text: "http://second"},
		{label: "API key (optional; env:NAME and cmd:... also work)"},
		{label: "Upstream family", index: 1},
		{label: "opus", index: 0},
		{label: "sonnet", index: 0},
		{label: "haiku", index: 0},
		{label: "fable", index: 0},
	}}
	calls := 0
	name := ""
	p := &config.Profile{BaseURL: "http://first"}
	form := &profileForm{ctx: context.Background(), pr: q, name: &name, profile: p,
		lookup: func(_ context.Context, p *config.Profile) ([]proxy.Model, string) {
			calls++
			if p.BaseURL == "http://first" {
				return []proxy.Model{{ID: "old-model"}}, ""
			}
			return []proxy.Model{{ID: "new-model"}}, ""
		},
	}
	if err := form.run(); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || q.backs != 4 || q.sections != 2 {
		t.Errorf("discovery calls = %d, back steps = %d, sections = %d; want 2, 4, 2", calls, q.backs, q.sections)
	}
	if name != "devbox" || p.BaseURL != "http://second" || len(p.Models) != 0 {
		t.Errorf("name=%q, baseURL=%q, pins=%v", name, p.BaseURL, p.Models)
	}
}

func TestProfileFormCustomFamilyBack(t *testing.T) {
	q := &scriptedQuestions{answers: []formAnswer{
		{label: "Profile name", text: "custom"},
		{label: "Description (optional)"},
		{label: "Gateway base URL", text: "http://gateway"},
		{label: "API key (optional; env:NAME and cmd:... also work)"},
		{label: "Upstream family", index: 2}, // custom after one family
		{label: "Family (matched against model ids)", back: true},
		{label: "Upstream family", index: 2},
		{label: "Family (matched against model ids)", text: "missing"},
	}}
	name := ""
	p := &config.Profile{BaseURL: "http://gateway"}
	form := &profileForm{ctx: context.Background(), pr: q, name: &name, profile: p,
		lookup: func(context.Context, *config.Profile) ([]proxy.Model, string) {
			return []proxy.Model{{ID: "deepseek-chat"}}, ""
		},
	}
	if err := form.run(); err != nil {
		t.Fatal(err)
	}
	if p.Family != "missing" || q.backs != 1 || !reflect.DeepEqual(q.calls[len(q.calls)-3:], []string{"Family (matched against model ids)", "Upstream family", "Family (matched against model ids)"}) {
		t.Errorf("family=%q, backs=%d, calls=%v", p.Family, q.backs, q.calls)
	}
}

func TestProfileFormNoDiscoverSkipsModelQuestions(t *testing.T) {
	q := &scriptedQuestions{answers: []formAnswer{
		{label: "Profile name", text: "offline"},
		{label: "Description (optional)"},
		{label: "Gateway base URL", text: "http://gateway"},
		{label: "API key (optional; env:NAME and cmd:... also work)"},
	}}
	name := ""
	p := &config.Profile{}
	form := &profileForm{ctx: context.Background(), pr: q, name: &name, profile: p, noDiscover: true,
		lookup: func(context.Context, *config.Profile) ([]proxy.Model, string) {
			t.Fatal("discovery should be skipped")
			return nil, ""
		},
	}
	if err := form.run(); err != nil {
		t.Fatal(err)
	}
	if len(q.answers) != 0 || strings.Join(q.calls, ",") == "" {
		t.Errorf("unconsumed answers: %v; calls: %v", q.answers, q.calls)
	}
}

func TestProfileFormUnavailableCatalogueAndCancel(t *testing.T) {
	q := &scriptedQuestions{answers: []formAnswer{
		{label: "Profile name", text: "offline"},
		{label: "Description (optional)"},
		{label: "Gateway base URL", text: "http://gateway"},
		{label: "API key (optional; env:NAME and cmd:... also work)"},
		{label: "Upstream family (optional)", text: "deepseek"},
		{label: "Model for every slot (optional)", back: true},
		{label: "Upstream family (optional)", text: "gpt"},
		{label: "Model for every slot (optional)", text: "gpt-5"},
	}}
	name := ""
	p := &config.Profile{}
	form := &profileForm{ctx: context.Background(), pr: q, name: &name, profile: p,
		lookup: func(context.Context, *config.Profile) ([]proxy.Model, string) { return nil, "offline" },
	}
	if err := form.run(); err != nil {
		t.Fatal(err)
	}
	if p.Family != "gpt" || p.Model != "gpt-5" || q.backs != 1 {
		t.Errorf("fallback answers: family=%q model=%q backs=%d", p.Family, p.Model, q.backs)
	}
}

func TestProfileFormInterrupt(t *testing.T) {
	q := &scriptedQuestions{answers: []formAnswer{{label: "Profile name", abort: true}}}
	name := ""
	form := &profileForm{ctx: context.Background(), pr: q, name: &name, profile: &config.Profile{}}
	if err := form.run(); err == nil || !strings.Contains(err.Error(), "nothing written") {
		t.Errorf("interrupt error = %v", err)
	}
}

func TestValidPins(t *testing.T) {
	got := validPins(map[string]string{"opus": "old", "sonnet": "new"}, []proxy.Model{{ID: "new"}})
	if !reflect.DeepEqual(got, map[string]string{"sonnet": "new"}) {
		t.Errorf("validPins = %v", got)
	}
}
