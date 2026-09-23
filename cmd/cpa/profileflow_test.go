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
	searches int
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
func (s *scriptedQuestions) ChooseSearchDefault(label string, options []string, _ int) (int, error) {
	s.searches++
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

const agentQuestion = "Agent this profile is for (claude, codex, or a name from \"agents\")"
const keyQuestion = "API key (optional; env:NAME and cmd:... also work)"

func TestProfileFormBackAndRediscover(t *testing.T) {
	q := &scriptedQuestions{answers: []formAnswer{
		{label: "Profile name", back: true},
		{label: "Profile name", text: "devbox"},
		{label: "Description (optional)"},
		{label: agentQuestion, text: "claude"},
		{label: "Gateway base URL", text: "http://first"},
		{label: keyQuestion},
		{label: slotLabel("opus"), index: 1},
		{label: slotLabel("sonnet"), back: true},
		{label: slotLabel("opus"), back: true},
		{label: keyQuestion, back: true},
		{label: "Gateway base URL", text: "http://second"},
		{label: keyQuestion},
		{label: slotLabel("opus"), index: 0},
		{label: slotLabel("sonnet"), index: 0},
		{label: slotLabel("haiku"), index: 0},
		{label: slotLabel("fable"), index: 0},
	}}
	calls := 0
	name := ""
	p := &config.Profile{Agent: "claude", BaseURL: "http://first"}
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
	if calls != 2 || q.backs != 3 || q.sections != 2 {
		t.Errorf("discovery calls = %d, back steps = %d, sections = %d; want 2, 3, 2", calls, q.backs, q.sections)
	}
	if name != "devbox" || p.BaseURL != "http://second" || len(p.Models) != 0 || len(q.answers) != 0 {
		t.Errorf("name=%q, baseURL=%q, pins=%v, remaining=%v", name, p.BaseURL, p.Models, q.answers)
	}
}

func TestProfileFormOfflineClaudeBack(t *testing.T) {
	q := &scriptedQuestions{answers: []formAnswer{
		{label: "Profile name", text: "offline"},
		{label: "Description (optional)"},
		{label: agentQuestion, text: "claude"},
		{label: "Gateway base URL", text: "http://gateway"},
		{label: keyQuestion},
		{label: "Upstream family (optional)", text: "deepseek"},
		{label: "Model for every slot (optional)", back: true},
		{label: "Upstream family (optional)", text: "gpt"},
		{label: "Model for every slot (optional)", text: "gpt-5"},
	}}
	name := ""
	p := &config.Profile{Agent: "claude"}
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

func TestProfileFormNoDiscoverSkipsModelQuestions(t *testing.T) {
	q := &scriptedQuestions{answers: []formAnswer{
		{label: "Profile name", text: "offline"},
		{label: "Description (optional)"},
		{label: agentQuestion, text: "claude"},
		{label: "Gateway base URL", text: "http://gateway"},
		{label: keyQuestion},
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
	if len(q.answers) != 0 || q.sections != 0 {
		t.Errorf("unconsumed answers: %v; sections: %d", q.answers, q.sections)
	}
}

func TestProfileFormOtherAgents(t *testing.T) {
	for _, tc := range []struct {
		agent string
		model bool
	}{
		{"codex", true},
		{"unknown-agent", false},
	} {
		t.Run(tc.agent, func(t *testing.T) {
			answers := []formAnswer{
				{label: "Profile name", text: "other"},
				{label: "Description (optional)"},
				{label: agentQuestion, text: tc.agent},
				{label: "Gateway base URL", text: "http://gateway"},
				{label: keyQuestion},
			}
			if tc.model {
				answers = append(answers, formAnswer{label: "Model (optional)", text: "o3"})
			}
			q := &scriptedQuestions{answers: answers}
			name := ""
			p := &config.Profile{Agent: tc.agent}
			form := &profileForm{ctx: context.Background(), pr: q, name: &name, profile: p,
				lookup: func(context.Context, *config.Profile) ([]proxy.Model, string) {
					t.Fatal("non-Claude agent must not discover Claude models")
					return nil, ""
				},
			}
			if err := form.run(); err != nil {
				t.Fatal(err)
			}
			if len(q.answers) != 0 || (p.Model != "") != tc.model {
				t.Errorf("remaining=%v, model=%q", q.answers, p.Model)
			}
		})
	}
}

func TestProfileFormChangingAgentAfterBack(t *testing.T) {
	q := &scriptedQuestions{answers: []formAnswer{
		{label: "Profile name", text: "switch"},
		{label: "Description (optional)"},
		{label: agentQuestion, text: "claude"},
		{label: "Gateway base URL", text: "http://gateway"},
		{label: keyQuestion},
		{label: slotLabel("opus"), back: true},
		{label: keyQuestion, back: true},
		{label: "Gateway base URL", back: true},
		{label: agentQuestion, text: "codex"},
		{label: "Gateway base URL", text: "http://gateway"},
		{label: keyQuestion},
		{label: "Model (optional)", text: "o3"},
	}}
	name := ""
	p := &config.Profile{Agent: "claude", Models: map[string]string{"opus": "old"}}
	form := &profileForm{ctx: context.Background(), pr: q, name: &name, profile: p,
		lookup: func(context.Context, *config.Profile) ([]proxy.Model, string) {
			return []proxy.Model{{ID: "old"}}, ""
		},
	}
	if err := form.run(); err != nil {
		t.Fatal(err)
	}
	if p.Agent != "codex" || p.Model != "o3" || len(p.Models) != 0 || q.backs != 3 {
		t.Errorf("agent=%q, model=%q, pins=%v, backs=%d", p.Agent, p.Model, p.Models, q.backs)
	}
}

func TestProfileFormBackFromOverwriteConfirmation(t *testing.T) {
	q := &scriptedQuestions{answers: []formAnswer{
		{label: "Profile name", text: "existing"},
		{label: "Description (optional)"},
		{label: agentQuestion, text: "claude"},
		{label: "Gateway base URL", text: "http://gateway"},
		{label: keyQuestion, text: "old-key"},
		{label: keyQuestion, text: "new-key"},
	}}
	name := ""
	p := &config.Profile{Agent: "claude"}
	form := newProfileForm(context.Background(), q, &name, p, true)
	if err := form.run(); err != nil {
		t.Fatal(err)
	}
	// Confirm already erased its unanswered row when it returned ErrBack.
	if err := form.backFromConfirmation(); err != nil {
		t.Fatal(err)
	}
	if p.APIKey != "new-key" || q.backs != 1 || len(q.answers) != 0 {
		t.Errorf("key=%q, backs=%d, remaining=%v", p.APIKey, q.backs, q.answers)
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

func TestProfileFormSearchChoiceKeepsModelIndex(t *testing.T) {
	q := &scriptedQuestions{answers: []formAnswer{
		{label: "Profile name", text: "searched"},
		{label: "Description (optional)"},
		{label: agentQuestion, text: "claude"},
		{label: "Gateway base URL", text: "http://gateway"},
		{label: keyQuestion},
		{label: slotLabel("opus"), index: 3},
		{label: slotLabel("sonnet"), index: 0},
		{label: slotLabel("haiku"), index: 0},
		{label: slotLabel("fable"), index: 0},
	}}
	name := ""
	p := &config.Profile{Agent: "claude"}
	form := &profileForm{ctx: context.Background(), pr: q, name: &name, profile: p,
		lookup: func(context.Context, *config.Profile) ([]proxy.Model, string) {
			return []proxy.Model{{ID: "alpha"}, {ID: "beta"}, {ID: "gamma"}}, ""
		},
	}
	if err := form.run(); err != nil {
		t.Fatal(err)
	}
	if q.searches != 4 || p.Models["opus"] != "gamma" || len(p.Models) != 1 {
		t.Errorf("searches=%d, models=%v; want four searched slots and opus=gamma", q.searches, p.Models)
	}
}

func TestValidPins(t *testing.T) {
	got := validPins(map[string]string{"opus": "old", "sonnet": "new"}, []proxy.Model{{ID: "new"}})
	if !reflect.DeepEqual(got, map[string]string{"sonnet": "new"}) {
		t.Errorf("validPins = %v", got)
	}
}
