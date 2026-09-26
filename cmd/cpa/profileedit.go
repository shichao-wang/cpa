package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/shichao-wang/cpa/internal/config"
	"github.com/shichao-wang/cpa/internal/launch"
	"github.com/shichao-wang/cpa/internal/prompt"
)

var editableFields = map[string]string{
	"--agent": "agent", "--description": "description", "--base-url": "baseUrl",
	"--api-key": "apiKey", "--family": "family", "--model": "model",
}

func cmdProfileEdit(ctx context.Context, args []string) error {
	f, err := parseFlags(args)
	if err != nil {
		return err
	}
	if len(f.rest) != 1 || strings.TrimSpace(f.rest[0]) == "" {
		return fmt.Errorf("usage: cpa profile edit <name> [--file <path>] [fields to change]")
	}
	for flag := range f.provided {
		if _, editable := editableFields[flag]; !editable && flag != "--file" && flag != "--no-discover" {
			return fmt.Errorf("%s is not supported by `cpa profile edit`", flag)
		}
	}
	if f.provided["--base-url"] && f.baseURL == "" {
		return fmt.Errorf("a profile needs a base URL")
	}
	name := f.rest[0]
	path := f.file
	if path == "" {
		path = config.UserConfigPath()
	}
	if path == "" {
		return fmt.Errorf("cannot determine where to edit; pass --file <path>")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	cfg, err := config.Parse(data)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	old, ok := cfg.Profiles[name]
	if !ok || old == nil {
		return fmt.Errorf("profile %q not found in %s; use --file to select the file defining it", name, path)
	}
	blob, err := json.Marshal(old)
	if err != nil {
		return err
	}
	var edited config.Profile
	if err := json.Unmarshal(blob, &edited); err != nil {
		return err
	}
	applyEditFlags(&edited, f)

	pr, err := prompt.New(os.Stdin, os.Stdout)
	if errors.Is(err, prompt.ErrNotATerminal) {
		changed := false
		for flag := range editableFields {
			changed = changed || f.provided[flag]
		}
		if !changed {
			return fmt.Errorf("profile edit needs at least one field flag without a terminal")
		}
	} else if err != nil {
		return err
	} else {
		defer pr.Close()
		form := newProfileForm(ctx, pr, &name, &edited, f.noDiscover)
		form.start, form.editing = stepDescription, true
		form.kind = func(agent string) config.Kind { return resolveKindAt(path, agent) }
		if err := form.run(); err != nil {
			return err
		}
		// Confirmation defaults to leaving the file untouched.
		for {
			preview := editedForAgent(path, old, &edited)
			changes := changedProfileFields(old, &preview)
			if len(changes) == 0 {
				pr.Section("No fields changed")
			} else {
				pr.Section("Changed fields: " + strings.Join(changes, ", "))
			}
			choice, err := pr.ChooseSearchDefault(
				fmt.Sprintf("Save changes to %q in %s?", name, path),
				[]string{"Cancel", "Save changes", "Back to form"}, 0)
			if errors.Is(err, prompt.ErrBack) || err == nil && choice == 2 {
				if err := form.backFromConfirmation(); err != nil {
					return err
				}
				continue
			}
			if err != nil {
				return aborted(err)
			}
			if choice == 0 {
				return fmt.Errorf("aborted; %q left untouched in %s", name, path)
			}
			break
		}
		pr.Close()
	}

	edited = editedForAgent(path, old, &edited)
	if err := validBaseURL(edited.BaseURL); err != nil {
		return err
	}
	if strings.TrimSpace(edited.Agent) == "" && (old.Agent != "" || f.provided["--agent"]) {
		return fmt.Errorf("a profile needs an agent")
	}
	if len(changedProfileFields(old, &edited)) == 0 {
		fmt.Printf("profile %q unchanged in %s\n", name, path)
		return nil
	}
	if err := patchProfileAt(path, name, data, old, &edited); err != nil {
		return err
	}
	fmt.Printf("\nupdated profile %q in %s\n", name, path)
	return nil
}

func applyEditFlags(p *config.Profile, f *flags) {
	if f.provided["--agent"] {
		p.Agent = f.agent
	}
	if f.provided["--description"] {
		p.Description = f.description
	}
	if f.provided["--base-url"] {
		p.BaseURL = f.baseURL
	}
	if f.provided["--api-key"] {
		p.APIKey = f.apiKey
		p.APIKeyEnv = ""
		p.APIKeyCmd = ""
	}
	if f.provided["--family"] {
		p.Family = f.family
	}
	if f.provided["--model"] {
		p.Model = f.model
	}
}

func editedForAgent(path string, old, edited *config.Profile) config.Profile {
	result := *edited
	if result.Agent != old.Agent && resolveKindAt(path, result.Agent) != config.KindClaude {
		result.Models = nil
		result.FallbackModel = nil
		result.Family = ""
	}
	return result
}

func changedProfileFields(old, edited *config.Profile) []string {
	before, err := profileMap(old)
	if err != nil {
		return nil
	}
	after, err := profileMap(edited)
	if err != nil {
		return nil
	}
	var changed []string
	for key, value := range before {
		if !reflect.DeepEqual(value, after[key]) {
			changed = append(changed, key)
		}
	}
	for key, value := range after {
		if _, ok := before[key]; !ok && value != nil {
			changed = append(changed, key)
		}
	}
	sort.Strings(changed)
	return changed
}

func profileMap(p *config.Profile) (map[string]interface{}, error) {
	data, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	var result map[string]interface{}
	err = json.Unmarshal(data, &result)
	return result, err
}

// patchProfileAt changes only fields the editor changed. The original JSON map
// retains fields outside the editor, including extensions unknown to this build.
func patchProfileAt(path, name string, original []byte, old, edited *config.Profile) error {
	current, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var before, raw map[string]interface{}
	if before, err = config.ParseRaw(original); err != nil {
		return err
	}
	if raw, err = config.ParseRaw(current); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	previous, _ := before["profiles"].(map[string]interface{})
	profiles, _ := raw["profiles"].(map[string]interface{})
	if !reflect.DeepEqual(previous[name], profiles[name]) {
		return fmt.Errorf("profile %q changed in %s while editing; retry", name, path)
	}
	profile, _ := profiles[name].(map[string]interface{})
	oldMap, err := profileMap(old)
	if err != nil {
		return err
	}
	newMap, err := profileMap(edited)
	if err != nil {
		return err
	}
	for key, value := range newMap {
		if !reflect.DeepEqual(oldMap[key], value) {
			profile[key] = value
		}
	}
	for key := range oldMap {
		if _, exists := newMap[key]; !exists {
			delete(profile, key)
		}
	}
	if !reflect.DeepEqual(oldMap["agent"], newMap["agent"]) ||
		!reflect.DeepEqual(oldMap["models"], newMap["models"]) ||
		!reflect.DeepEqual(oldMap["model"], newMap["model"]) ||
		!reflect.DeepEqual(oldMap["family"], newMap["family"]) {
		syncEditedPicker(path, profile, old)
	}
	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, append(out, '\n'), 0o600)
}

func syncEditedPicker(path string, profile map[string]interface{}, old *config.Profile) {
	settings, _ := profile["claudeSettings"].(map[string]interface{})
	if settings == nil {
		settings = make(map[string]interface{})
	}
	current, exists := settings["modelPicker"]
	oldGenerated := interface{}(nil)
	if rows := pickerRows(old); len(rows) > 0 {
		oldGenerated = pickerSettingsValue(rows)
	}
	if exists && !reflect.DeepEqual(current, oldGenerated) {
		fmt.Fprintln(os.Stderr, "note: custom claudeSettings.modelPicker kept; check it after changing models")
		return
	}
	data, err := json.Marshal(profile)
	if err != nil {
		return
	}
	var updated config.Profile
	if json.Unmarshal(data, &updated) != nil {
		return
	}
	rows := pickerRows(&updated)
	if resolveKindAt(path, updated.Agent) == config.KindClaude && len(rows) > 0 {
		settings["modelPicker"] = pickerSettingsValue(rows)
	} else {
		delete(settings, "modelPicker")
	}
	if len(settings) > 0 {
		profile["claudeSettings"] = settings
	} else {
		delete(profile, "claudeSettings")
	}
}

func pickerSettingsValue(rows []pickerRow) interface{} {
	data, _ := json.Marshal(launch.PickerSettings(rows))
	var value interface{}
	_ = json.Unmarshal(data, &value)
	return value
}
