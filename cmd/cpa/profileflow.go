package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/shichao-wang/cpa/internal/config"
	"github.com/shichao-wang/cpa/internal/prompt"
	"github.com/shichao-wang/cpa/internal/proxy"
)

// Completed steps form a stack: Escape revisits the last question actually
// shown, including the optional custom-family question.
type profileStep int

const (
	stepName profileStep = iota
	stepDescription
	stepBaseURL
	stepAPIKey
	stepFamily
	stepCustomFamily
	stepModel
	stepOpus
	stepSonnet
	stepHaiku
	stepFable
	stepDone
)

type profileQuestions interface {
	Input(string, string, func(string) error) (string, error)
	ChooseDefault(string, []string, int) (int, error)
	SetIndent(int)
	Section(string)
	Back()
}

type profileForm struct {
	ctx             context.Context
	pr              profileQuestions
	lookup          func(context.Context, *config.Profile) ([]proxy.Model, string)
	name            *string
	profile         *config.Profile
	noDiscover      bool
	available       []proxy.Model
	catalogueLoaded bool
	discoveredURL   string
	discoveredKey   string
	customFamily    bool
	history         []profileStep
}

func interactiveProfile(ctx context.Context, pr *prompt.Prompter, name *string, p *config.Profile, noDiscover bool) error {
	if p.BaseURL == "" {
		p.BaseURL = "http://127.0.0.1:8317"
	}
	form := &profileForm{ctx: ctx, pr: pr, lookup: discover, name: name, profile: p, noDiscover: noDiscover}
	return form.run()
}

func (f *profileForm) run() error {
	step := stepName
	showModelSection := false
	for step != stepDone {
		if step == stepFamily && f.noDiscover {
			break
		}
		if step == stepFamily {
			f.discover()
			if showModelSection {
				f.pr.Section("Model configuration")
				showModelSection = false
			}
		}
		indent := 0
		if step >= stepFamily {
			indent = 2
			if step >= stepOpus {
				indent = 4
			}
		}
		f.pr.SetIndent(indent)
		next, err := f.ask(step)
		if errors.Is(err, prompt.ErrBack) {
			if len(f.history) > 0 {
				step = f.history[len(f.history)-1]
				f.history = f.history[:len(f.history)-1]
				f.pr.Back()
			}
			continue
		}
		if err != nil {
			return aborted(err)
		}
		if step == stepAPIKey {
			showModelSection = true
		}
		f.history = append(f.history, step)
		step = next
	}
	return nil
}

func (f *profileForm) discover() {
	p := f.profile
	if f.catalogueLoaded && f.discoveredURL == p.BaseURL && f.discoveredKey == p.APIKey {
		return
	}
	var note string
	f.available, note = f.lookup(f.ctx, p)
	if note != "" {
		fmt.Fprintf(os.Stderr, "note: %s\n", note)
	}
	f.catalogueLoaded, f.discoveredURL, f.discoveredKey = true, p.BaseURL, p.APIKey
	p.Models = validPins(p.Models, f.available)
}

func (f *profileForm) ask(step profileStep) (profileStep, error) {
	p := f.profile
	input := func(label string, value *string, validate func(string) error, next profileStep) (profileStep, error) {
		answer, err := f.pr.Input(label, *value, validate)
		if err == nil {
			*value = answer
		}
		return next, err
	}
	switch step {
	case stepName:
		return input("Profile name", f.name, notBlank("a profile needs a name"), stepDescription)
	case stepDescription:
		return input("Description (optional)", &p.Description, nil, stepBaseURL)
	case stepBaseURL:
		return input("Gateway base URL", &p.BaseURL, validBaseURL, stepAPIKey)
	case stepAPIKey:
		return input("API key (optional; env:NAME and cmd:... also work)", &p.APIKey, nil, stepFamily)
	case stepFamily:
		if len(f.available) == 0 {
			return input("Upstream family (optional)", &p.Family, nil, stepModel)
		}
		labels, values := familyChoices(f.available, p.Family)
		def := 0
		if f.customFamily {
			def = len(values) - 1
		} else {
			for i, v := range values {
				if v == p.Family {
					def = i
					break
				}
			}
		}
		picked, err := f.pr.ChooseDefault("Upstream family", labels, def)
		if err != nil {
			return stepFamily, err
		}
		f.customFamily = values[picked] == familyCustom
		if f.customFamily {
			return stepCustomFamily, nil
		}
		if values[picked] == familyAll {
			p.Family = ""
		} else {
			p.Family = values[picked]
		}
		p.Models = validPins(p.Models, matching(f.available, p.Family))
		return f.firstSlot(), nil
	case stepCustomFamily:
		next, err := input("Family (matched against model ids)", &p.Family, nil, stepDone)
		if err == nil {
			p.Models = validPins(p.Models, matching(f.available, p.Family))
			next = f.firstSlot()
		}
		return next, err
	case stepModel:
		return input("Model for every slot (optional)", &p.Model, nil, stepDone)
	case stepOpus, stepSonnet, stepHaiku, stepFable:
		slot := config.Slots[int(step-stepOpus)]
		labels := []string{followFamily}
		values := []string{""}
		for _, m := range matching(f.available, p.Family) {
			labels = append(labels, m.Label())
			values = append(values, m.ID)
		}
		def := 0
		for i, v := range values {
			if v != "" && v == p.Models[slot] {
				def = i
				break
			}
		}
		picked, err := f.pr.ChooseDefault(slot, labels, def)
		if err != nil {
			return step, err
		}
		if values[picked] == "" {
			delete(p.Models, slot)
		} else {
			if p.Models == nil {
				p.Models = make(map[string]string)
			}
			p.Models[slot] = values[picked]
		}
		if len(p.Models) == 0 {
			p.Models = nil
		}
		return step + 1, nil
	}
	return stepDone, fmt.Errorf("unknown profile step %d", step)
}

func (f *profileForm) firstSlot() profileStep {
	if len(matching(f.available, f.profile.Family)) == 0 {
		fmt.Fprintf(os.Stderr, "note: no advertised model matches family %q; leaving the slots unset\n", f.profile.Family)
		return stepDone
	}
	return stepOpus
}

// A changed family or gateway cannot keep pins absent from its catalogue.
func validPins(pins map[string]string, candidates []proxy.Model) map[string]string {
	valid := make(map[string]bool, len(candidates))
	for _, m := range candidates {
		valid[m.ID] = true
	}
	var kept map[string]string
	for slot, model := range pins {
		if valid[model] {
			if kept == nil {
				kept = make(map[string]string)
			}
			kept[slot] = model
		}
	}
	return kept
}
