package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/shichao-wang/cpa/internal/config"
	"github.com/shichao-wang/cpa/internal/prompt"
	"github.com/shichao-wang/cpa/internal/proxy"
)

// Completed questions form a stack so Escape returns to the last one shown.
type profileStep int

const (
	stepName profileStep = iota
	stepDescription
	stepAgent
	stepBaseURL
	stepAPIKey
	stepDownstream // transition, not a question
	stepFamily     // offline Claude fallback only
	stepModel      // offline Claude fallback only
	stepOtherModel // OpenAI-compatible agent
	stepOpus
	stepSonnet
	stepHaiku
	stepFable
	stepFallbackModel
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
	history         []profileStep
}

func newProfileForm(ctx context.Context, pr profileQuestions, name *string, p *config.Profile, noDiscover bool) *profileForm {
	if p.BaseURL == "" {
		p.BaseURL = "http://127.0.0.1:8317"
	}
	return &profileForm{ctx: ctx, pr: pr, lookup: discover, name: name, profile: p, noDiscover: noDiscover}
}

func (f *profileForm) run() error {
	return f.runFrom(stepName)
}

// A confirmation is another question after the form. Esc returns to the last
// question actually shown, keeping the answers and discovery cache intact.
func (f *profileForm) backFromConfirmation() error {
	if len(f.history) == 0 {
		return aborted(prompt.ErrBack)
	}
	step := f.history[len(f.history)-1]
	f.history = f.history[:len(f.history)-1]
	f.pr.Back()
	return f.runFrom(step)
}

func (f *profileForm) runFrom(step profileStep) error {
	for step != stepDone {
		if step == stepDownstream {
			step = f.downstream()
			if step == stepDone {
				break
			}
			if !f.noDiscover {
				f.pr.SetIndent(0)
				f.pr.Section("Model configuration")
			}
		}
		indent := 0
		if step >= stepFamily {
			indent = 2
			if step >= stepOpus && step <= stepFable {
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
		f.history = append(f.history, step)
		step = next
	}
	return nil
}

// Only Claude has four slots. A missing catalogue keeps the existing offline
// family/model questions; other agents either ask one model or no model at all.
func (f *profileForm) downstream() profileStep {
	switch resolveKind(f.profile.Agent) {
	case config.KindClaude:
		if f.noDiscover {
			return stepFallbackModel
		}
		f.discover()
		if len(f.available) == 0 {
			return stepFamily
		}
		candidates := matching(f.available, f.profile.Family)
		if len(candidates) == 0 {
			f.profile.Models = nil
			fmt.Fprintf(os.Stderr, "note: no advertised model matches family %q; leaving the slots unset\n", f.profile.Family)
			return stepFallbackModel
		}
		f.profile.Models = validPins(f.profile.Models, candidates)
		return stepOpus
	case config.KindOpenAI:
		f.profile.FallbackModel = nil
		f.profile.Models = nil
		return stepOtherModel
	default:
		f.profile.FallbackModel = nil
		f.profile.Models = nil
		return stepDone
	}
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
	// A gateway that is temporarily unavailable cannot disprove an explicit pin.
	// Keep it for the offline fallback rather than dropping it on discovery failure.
	if len(f.available) > 0 {
		p.Models = validPins(p.Models, f.available)
	}
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
		return input("Description (optional)", &p.Description, nil, stepAgent)
	case stepAgent:
		return input("Agent this profile is for (claude, codex, or a name from \"agents\")", &p.Agent, notBlank("a profile needs an agent"), stepBaseURL)
	case stepBaseURL:
		return input("Gateway base URL", &p.BaseURL, validBaseURL, stepAPIKey)
	case stepAPIKey:
		return input("API key (optional; env:NAME and cmd:... also work)", &p.APIKey, nil, stepDownstream)
	case stepFamily:
		return input("Upstream family (optional)", &p.Family, nil, stepModel)
	case stepModel:
		return input("Model for every slot (optional)", &p.Model, nil, stepFallbackModel)
	case stepOtherModel:
		return input("Model (optional)", &p.Model, nil, stepDone)
	case stepFallbackModel:
		answer, err := f.pr.Input("Fallback models (optional; comma-separated, in order, max 3)", strings.Join(p.FallbackModel, ", "), func(s string) error {
			_, err := parseFallbackModels(s)
			return err
		})
		if err != nil {
			return stepDone, err
		}
		p.FallbackModel, _ = parseFallbackModels(answer)
		return stepDone, nil
	case stepOpus, stepSonnet, stepHaiku, stepFable:
		slot := config.Slots[int(step-stepOpus)]
		candidates := matching(f.available, p.Family)
		labels := []string{leaveUnset}
		values := []string{""}
		for _, m := range candidates {
			labels = append(labels, candidateLabel(m, slot))
			values = append(values, m.ID)
		}
		picked, err := f.pr.ChooseDefault(slotLabel(slot), labels, defaultSlotChoice(slot, candidates, p.Models[slot]))
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

func parseFallbackModels(s string) ([]string, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	parts := strings.Split(s, ",")
	if len(parts) > 3 {
		return nil, fmt.Errorf("at most 3 fallback models are allowed")
	}
	seen := make(map[string]bool, len(parts))
	for i, part := range parts {
		parts[i] = strings.TrimSpace(part)
		if parts[i] == "" {
			return nil, fmt.Errorf("fallback models cannot contain an empty entry")
		}
		if seen[parts[i]] {
			return nil, fmt.Errorf("duplicate fallback model %q", parts[i])
		}
		seen[parts[i]] = true
	}
	return parts, nil
}

// A changed gateway or family cannot keep slot pins it no longer offers.
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
