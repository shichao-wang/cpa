package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shichao-wang/cpa/internal/config"
	"github.com/shichao-wang/cpa/internal/proxy"
)

func TestProfileFormDetectsNameConflictAtFirstQuestion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"profiles":{"taken":{"baseUrl":"http://old"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	q := &scriptedQuestions{answers: []formAnswer{{label: "Profile name", text: "taken"}}}
	name := ""
	form := newProfileForm(context.Background(), q, &name, &config.Profile{Agent: "claude"}, true)
	form.path = path
	if err := form.run(); err == nil || !strings.Contains(err.Error(), "profile edit") {
		t.Fatalf("expected name conflict at first prompt, got %v", err)
	}
	if len(q.calls) != 1 || q.calls[0] != "Profile name" {
		t.Fatalf("unexpected questions: %v", q.calls)
	}
}

func TestProfileEditFormKeepsUnavailablePin(t *testing.T) {
	q := &scriptedQuestions{answers: []formAnswer{
		{label: "Description (optional)", text: "updated"},
		{label: agentQuestion, choice: "claude"},
		{label: "Gateway base URL", text: "http://gateway"},
		{label: "New API key (blank keeps current; env:NAME and cmd:... also work)"},
		{label: slotLabel("opus"), index: 1},
		{label: slotLabel("sonnet"), index: 0},
		{label: slotLabel("haiku"), index: 0},
		{label: slotLabel("fable"), index: 0},
		{label: fallbackPick},
	}}
	name := "dev"
	p := &config.Profile{Agent: "claude", BaseURL: "http://gateway", Models: map[string]string{"opus": "retired-model"}}
	form := newProfileForm(context.Background(), q, &name, p, false)
	form.start, form.editing = stepDescription, true
	form.lookup = func(context.Context, *config.Profile) ([]proxy.Model, string) {
		return []proxy.Model{{ID: "new-model"}}, ""
	}
	if err := form.run(); err != nil {
		t.Fatal(err)
	}
	if p.Models["opus"] != "retired-model" {
		t.Fatalf("existing pin was lost: %v", p.Models)
	}
}

func TestProfileEditBackFromOtherAgentKeepsClaudeModels(t *testing.T) {
	q := &scriptedQuestions{answers: []formAnswer{
		{label: "Description (optional)", text: "updated"},
		{label: agentQuestion, choice: "codex"},
		{label: "Gateway base URL", text: "http://gateway"},
		{label: "New API key (blank keeps current; env:NAME and cmd:... also work)"},
		{label: "Model (optional)", back: true},
		{label: "New API key (blank keeps current; env:NAME and cmd:... also work)", back: true},
		{label: "Gateway base URL", back: true},
		{label: agentQuestion, choice: "claude"},
		{label: "Gateway base URL", text: "http://gateway"},
		{label: "New API key (blank keeps current; env:NAME and cmd:... also work)"},
		{label: fallbackQuestion, text: "backup-model"},
	}}
	name := "dev"
	p := &config.Profile{Agent: "claude", BaseURL: "http://gateway", Models: map[string]string{"opus": "pinned-model"}, FallbackModel: []string{"backup-model"}}
	form := newProfileForm(context.Background(), q, &name, p, true)
	form.start, form.editing = stepDescription, true
	if err := form.run(); err != nil {
		t.Fatal(err)
	}
	if p.Models["opus"] != "pinned-model" || len(p.FallbackModel) != 1 || p.FallbackModel[0] != "backup-model" {
		t.Fatalf("returning to Claude lost routing data: %+v", p)
	}
}

func TestProfileEditUnboundProfileAllowsEmptyAgent(t *testing.T) {
	q := &scriptedQuestions{answers: []formAnswer{
		{label: "Description (optional)", text: "updated"},
		{label: agentQuestion, choice: agentUnbound},
		{label: "Gateway base URL", text: "http://gateway"},
		{label: "New API key (blank keeps current; env:NAME and cmd:... also work)"},
	}}
	name := "dev"
	p := &config.Profile{BaseURL: "http://gateway"}
	form := newProfileForm(context.Background(), q, &name, p, true)
	form.start, form.editing = stepDescription, true
	if err := form.run(); err != nil {
		t.Fatal(err)
	}
	if p.Agent != "" || p.Description != "updated" {
		t.Fatalf("unbound profile changed unexpectedly: %+v", p)
	}
}

func TestProfileEditFormUsesCustomClaudeAgentFromTarget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"agents":{"my-claude":{"kind":"claude","bin":"claude"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	q := &scriptedQuestions{answers: []formAnswer{
		{label: "Description (optional)"},
		{label: agentQuestion, choice: agentOther},
		{label: agentInputLabel, text: "my-claude"},
		{label: "Gateway base URL", text: "http://gateway"},
		{label: "New API key (blank keeps current; env:NAME and cmd:... also work)"},
		{label: fallbackQuestion, text: "backup-model"},
	}}
	name := "dev"
	p := &config.Profile{Agent: "my-claude", BaseURL: "http://gateway"}
	form := newProfileForm(context.Background(), q, &name, p, true)
	form.start, form.editing = stepDescription, true
	form.kind = func(agent string) config.Kind { return resolveKindAt(path, agent) }
	if err := form.run(); err != nil {
		t.Fatal(err)
	}
	if len(q.answers) != 0 || len(p.FallbackModel) != 1 || p.FallbackModel[0] != "backup-model" {
		t.Fatalf("custom Claude model questions were skipped: %+v, remaining %v", p, q.answers)
	}
}

func TestProfileEditFormBackAtFirstQuestionAborts(t *testing.T) {
	q := &scriptedQuestions{answers: []formAnswer{{label: "Description (optional)", back: true}}}
	name := "dev"
	form := newProfileForm(context.Background(), q, &name, &config.Profile{BaseURL: "http://gateway"}, true)
	form.start, form.editing = stepDescription, true
	if err := form.run(); err == nil || !strings.Contains(err.Error(), "aborted") {
		t.Fatalf("expected abort instead of repeated question, got %v", err)
	}
}
