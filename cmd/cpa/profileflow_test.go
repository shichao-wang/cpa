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
	// choice names the row of a list by its option text, which is how the
	// agent question is answered: the rows around a named agent depend on what
	// the settings declare, so a name is stabler than a count.
	choice string
	// picks is the answer to a pickable list, as positions into it. It is
	// distinct from index so a test can pick several, and none at all.
	picks []int
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

// ChooseDefault answers the agent question, which is a plain two-row list: a
// test names the row it means rather than counting, so the answer reads as the
// agent it picks.
func (s *scriptedQuestions) ChooseDefault(label string, options []string, def int) (int, error) {
	a, err := s.next(label)
	if err != nil {
		return 0, err
	}
	if a.choice == "" {
		return 0, fmt.Errorf("agent question %q needs a choice, got index %d", label, a.index)
	}
	for i, opt := range options {
		if opt == a.choice {
			return i, nil
		}
	}
	return 0, fmt.Errorf("choice %q missing for %q; options %v", a.choice, label, options)
}

func (s *scriptedQuestions) ChooseSearchDefault(label string, options []string, def int) (int, error) {
	s.searches++
	a, err := s.next(label)
	if err != nil {
		return 0, err
	}
	// A list answered by label names the row it means, so a test does not have
	// to count rows whose number depends on what the user's settings declare.
	if a.choice != "" {
		for i, opt := range options {
			if opt == a.choice {
				return i, nil
			}
		}
		return 0, fmt.Errorf("choice %q missing for %q; options %v", a.choice, label, options)
	}
	if a.index >= len(options) {
		return 0, fmt.Errorf("choice %d missing for %q", a.index, label)
	}
	return a.index, nil
}
func (s *scriptedQuestions) ChooseMultiSearch(label string, options []string, _ []int, max int) ([]int, error) {
	s.searches++
	a, err := s.next(label)
	if err != nil {
		return nil, err
	}
	if len(a.picks) > max {
		return nil, fmt.Errorf("%d picks for %q exceeds the limit of %d", len(a.picks), label, max)
	}
	for _, i := range a.picks {
		if i >= len(options) {
			return nil, fmt.Errorf("pick %d missing for %q", i, label)
		}
	}
	return a.picks, nil
}
func (s *scriptedQuestions) SetIndent(int)  {}
func (s *scriptedQuestions) Section(string) { s.sections++ }
func (s *scriptedQuestions) Back()          { s.backs++ }

const agentQuestion = "Agent this profile is for"
const keyQuestion = "API key (optional; env:NAME and cmd:... also work)"

// fallbackQuestion is what is asked with no catalogue to list; fallbackPick is
// the pickable list used when discovery found models to offer.
const fallbackQuestion = fallbackInputLabel
const fallbackPick = fallbackLabel

func TestProfileFormBackAndRediscover(t *testing.T) {
	q := &scriptedQuestions{answers: []formAnswer{
		{label: "Profile name", back: true},
		{label: "Profile name", text: "devbox"},
		{label: "Description (optional)"},
		{label: agentQuestion, choice: "claude"},
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
		{label: fallbackPick, picks: []int{0}},
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
	if name != "devbox" || p.BaseURL != "http://second" || len(p.Models) != 0 || !reflect.DeepEqual(p.FallbackModel, []string{"new-model"}) || len(q.answers) != 0 {
		t.Errorf("name=%q, baseURL=%q, pins=%v, fallback=%v, remaining=%v", name, p.BaseURL, p.Models, p.FallbackModel, q.answers)
	}
}

func TestProfileFormOfflineClaudeBack(t *testing.T) {
	q := &scriptedQuestions{answers: []formAnswer{
		{label: "Profile name", text: "offline"},
		{label: "Description (optional)"},
		{label: agentQuestion, choice: "claude"},
		{label: "Gateway base URL", text: "http://gateway"},
		{label: keyQuestion},
		{label: "Upstream family (optional)", text: "deepseek"},
		{label: "Model for every slot (optional)", back: true},
		{label: "Upstream family (optional)", text: "gpt"},
		{label: "Model for every slot (optional)", text: "gpt-5"},
		{label: fallbackQuestion},
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
		{label: agentQuestion, choice: "claude"},
		{label: "Gateway base URL", text: "http://gateway"},
		{label: keyQuestion},
		{label: fallbackQuestion, text: "opus, haiku"},
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

// Only Claude Code and Codex are wired up, so those are the two rows the agent
// question offers. A profile for another application cannot be written from
// the prompt at all — --agent still takes a name, but the form does not.
func TestProfileFormAgentOffersClaudeAndCodex(t *testing.T) {
	for _, tc := range []struct {
		agent   string
		offline bool
	}{
		{"claude", true},
		{"codex", false},
	} {
		t.Run(tc.agent, func(t *testing.T) {
			answers := []formAnswer{
				{label: "Profile name", text: "other"},
				{label: "Description (optional)"},
				{label: agentQuestion, choice: tc.agent},
				{label: "Gateway base URL", text: "http://gateway"},
				{label: keyQuestion},
			}
			if tc.offline {
				// A Claude profile with no catalogue asks the offline rows.
				answers = append(answers,
					formAnswer{label: "Upstream family (optional)", text: "deepseek"},
					formAnswer{label: "Model for every slot (optional)", text: "deepseek-v4"},
					formAnswer{label: fallbackQuestion},
				)
			} else {
				answers = append(answers, formAnswer{label: "Model (optional)", text: "o3"})
			}
			q := &scriptedQuestions{answers: answers}
			name := ""
			p := &config.Profile{}
			form := &profileForm{ctx: context.Background(), pr: q, name: &name, profile: p,
				lookup: func(ctx context.Context, p *config.Profile) ([]proxy.Model, string) {
					if tc.agent == "codex" {
						t.Fatal("an OpenAI-kind agent must not discover Claude models")
					}
					return nil, ""
				},
			}
			if err := form.run(); err != nil {
				t.Fatal(err)
			}
			if len(q.answers) != 0 || p.Agent != tc.agent {
				t.Errorf("remaining=%v, agent=%q", q.answers, p.Agent)
			}
		})
	}
}

func TestProfileFormChangingAgentAfterBack(t *testing.T) {
	q := &scriptedQuestions{answers: []formAnswer{
		{label: "Profile name", text: "switch"},
		{label: "Description (optional)"},
		{label: agentQuestion, choice: "claude"},
		{label: "Gateway base URL", text: "http://gateway"},
		{label: keyQuestion},
		{label: slotLabel("opus"), back: true},
		{label: keyQuestion, back: true},
		{label: "Gateway base URL", back: true},
		{label: agentQuestion, choice: "codex"},
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
		{label: agentQuestion, choice: "claude"},
		{label: "Gateway base URL", text: "http://gateway"},
		{label: keyQuestion, text: "old-key"},
		{label: fallbackQuestion},
		{label: fallbackQuestion, text: "new-model"},
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
	if !reflect.DeepEqual(p.FallbackModel, []string{"new-model"}) || q.backs != 1 || len(q.answers) != 0 {
		t.Errorf("fallback=%v, backs=%d, remaining=%v", p.FallbackModel, q.backs, q.answers)
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

// The agent question offers the agents the settings declare, and a name the
// file does not carry is reached through the typed row rather than by typing
// into the list itself — an undeclared name does not launch, so it is not a
// row that can be picked directly.
func TestProfileFormAgentPickNamesTheAgent(t *testing.T) {
	q := &scriptedQuestions{answers: []formAnswer{
		{label: "Profile name", text: "picked"},
		{label: "Description (optional)"},
		{label: agentQuestion, choice: "codex"},
		{label: "Gateway base URL", text: "http://gateway"},
		{label: keyQuestion},
		{label: fallbackPick},
	}}
	name := ""
	p := &config.Profile{}
	form := &profileForm{ctx: context.Background(), pr: q, name: &name, profile: p,
		lookup: func(context.Context, *config.Profile) ([]proxy.Model, string) { return nil, "" },
	}
	form.kind = func(string) config.Kind { return config.KindGeneric }
	if err := form.run(); err != nil {
		t.Fatal(err)
	}
	if p.Agent != "codex" {
		t.Fatalf("agent = %q, want codex (remaining %v)", p.Agent, q.answers)
	}
}

func TestProfileFormSearchChoiceKeepsModelIndex(t *testing.T) {
	q := &scriptedQuestions{answers: []formAnswer{
		{label: "Profile name", text: "searched"},
		{label: "Description (optional)"},
		{label: agentQuestion, choice: "claude"},
		{label: "Gateway base URL", text: "http://gateway"},
		{label: keyQuestion},
		{label: slotLabel("opus"), index: 3},
		{label: slotLabel("sonnet"), index: 0},
		{label: slotLabel("haiku"), index: 0},
		{label: slotLabel("fable"), index: 0},
		{label: fallbackPick},
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
	// Four slot rows and the fallback list: the agent row is a plain two-option
	// list, so it is not one of the searched ones.
	if q.searches != 5 || p.Models["opus"] != "gamma" || len(p.Models) != 1 {
		t.Errorf("searches=%d, models=%v; want five searched rows and opus=gamma", q.searches, p.Models)
	}
}

func TestValidPins(t *testing.T) {
	got := validPins(map[string]string{"opus": "old", "sonnet": "new"}, []proxy.Model{{ID: "new"}})
	if !reflect.DeepEqual(got, map[string]string{"sonnet": "new"}) {
		t.Errorf("validPins = %v", got)
	}
}
