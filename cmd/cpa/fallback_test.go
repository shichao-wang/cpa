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
		{label: agentQuestion, choice: "claude"},
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

// With a catalogue to list, the fallback question becomes a pick, and the
// answer is written in the order the picks were made — a fallback list is
// tried in order, so the order is part of the answer.
func TestProfileFormPicksFallbackModelsInPickOrder(t *testing.T) {
	q := &scriptedQuestions{answers: []formAnswer{
		{label: "Profile name", text: "picked"},
		{label: "Description (optional)"},
		{label: agentQuestion, choice: "claude"},
		{label: "Gateway base URL", text: "http://gateway"},
		{label: keyQuestion},
		{label: slotLabel("opus"), index: 0},
		{label: slotLabel("sonnet"), index: 0},
		{label: slotLabel("haiku"), index: 0},
		{label: slotLabel("fable"), index: 0},
		{label: fallbackPick, picks: []int{2, 0}},
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
	if want := []string{"gamma", "alpha"}; !reflect.DeepEqual(p.FallbackModel, want) {
		t.Errorf("fallbackModel = %v, want the pick order %v", p.FallbackModel, want)
	}
}

// A fallback is usually a different upstream to retry on, so the list must not
// be narrowed to the profile's family the way the slot rows are.
func TestProfileFormFallbackIgnoresTheFamilyFilter(t *testing.T) {
	var offered []string
	q := &scriptedQuestions{answers: []formAnswer{
		{label: "Profile name", text: "family"},
		{label: "Description (optional)"},
		{label: agentQuestion, choice: "claude"},
		{label: "Gateway base URL", text: "http://gateway"},
		{label: keyQuestion},
		{label: slotLabel("opus"), index: 0},
		{label: slotLabel("sonnet"), index: 0},
		{label: slotLabel("haiku"), index: 0},
		{label: slotLabel("fable"), index: 0},
		{label: fallbackPick, picks: []int{1}},
	}}
	name := ""
	p := &config.Profile{Agent: "claude", Family: "deepseek"}
	form := &profileForm{ctx: context.Background(), pr: q, name: &name, profile: p,
		lookup: func(context.Context, *config.Profile) ([]proxy.Model, string) {
			return []proxy.Model{{ID: "deepseek-chat"}, {ID: "claude-haiku-4-5"}}, ""
		},
		kind: func(string) config.Kind { return config.KindClaude },
	}
	// The pick is taken from the full catalogue, so the second entry is the one
	// the family filter would have hidden.
	form.pr = &recordingQuestions{inner: q, options: &offered}
	if err := form.run(); err != nil {
		t.Fatal(err)
	}
	if want := []string{"claude-haiku-4-5"}; !reflect.DeepEqual(p.FallbackModel, want) {
		t.Errorf("fallbackModel = %v, want %v", p.FallbackModel, want)
	}
	if len(offered) != 2 {
		t.Errorf("the fallback list offered %d models, want the whole catalogue of 2: %v", len(offered), offered)
	}
}

// recordingQuestions notes the options the fallback list was built from.
type recordingQuestions struct {
	inner   profileQuestions
	options *[]string
}

func (r *recordingQuestions) Input(label, def string, v func(string) error) (string, error) {
	return r.inner.Input(label, def, v)
}
func (r *recordingQuestions) ChooseSearchDefault(label string, options []string, def int) (int, error) {
	return r.inner.ChooseSearchDefault(label, options, def)
}
func (r *recordingQuestions) ChooseMultiSearch(label string, options []string, picked []int, max int) ([]int, error) {
	*r.options = options
	return r.inner.ChooseMultiSearch(label, options, picked, max)
}
func (r *recordingQuestions) SetIndent(int)  {}
func (r *recordingQuestions) Section(string) {}
func (r *recordingQuestions) Back()          {}

// A fallback already configured stays offered — and stays picked — even when
// the gateway no longer advertises it, or reopening the form would quietly
// drop a choice the user made deliberately.
func TestProfileFormFallbackKeepsAnUnadvertisedPick(t *testing.T) {
	q := &scriptedQuestions{answers: []formAnswer{
		{label: "Profile name", text: "existing"},
		{label: "Description (optional)"},
		{label: agentQuestion, choice: "claude"},
		{label: "Gateway base URL", text: "http://gateway"},
		{label: keyQuestion},
		{label: slotLabel("opus"), index: 0},
		{label: slotLabel("sonnet"), index: 0},
		{label: slotLabel("haiku"), index: 0},
		{label: slotLabel("fable"), index: 0},
		// The retired fallback is appended after the advertised one, so the
		// picks are made in the order the profile already had them.
		{label: fallbackPick, picks: []int{1, 0}},
	}}
	name := ""
	p := &config.Profile{Agent: "claude", FallbackModel: []string{"retired-model"}}
	form := &profileForm{ctx: context.Background(), pr: q, name: &name, profile: p,
		lookup: func(context.Context, *config.Profile) ([]proxy.Model, string) {
			return []proxy.Model{{ID: "current-model"}}, ""
		},
	}
	if err := form.run(); err != nil {
		t.Fatal(err)
	}
	if want := []string{"retired-model", "current-model"}; !reflect.DeepEqual(p.FallbackModel, want) {
		t.Errorf("fallbackModel = %v, want %v", p.FallbackModel, want)
	}
}

func TestFallbackChoicesListsCurrentFirstOnlyWhenUnadvertised(t *testing.T) {
	labels, values, picked := fallbackChoices(
		[]proxy.Model{{ID: "advertised"}},
		[]string{"gone", "advertised"},
	)
	if !reflect.DeepEqual(values, []string{"advertised", "gone"}) {
		t.Fatalf("values = %v, want the advertised model first and the retired one appended", values)
	}
	if !reflect.DeepEqual(picked, []int{1, 0}) {
		t.Errorf("picked = %v, want the profile's own order mapped onto the list", picked)
	}
	if labels[1] != "gone (not advertised)" {
		t.Errorf("an unavailable model is not marked as such: %q", labels[1])
	}
}
