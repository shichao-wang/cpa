package config

import (
	"strings"
	"unicode"
)

// DisplayBinding 根据当前上游模型和档位生成展示信息，忽略状态中的旧展示值。
func (s *State) DisplayBinding(slot string, binding Binding) Binding {
	name := binding.TargetModel
	for _, model := range s.Connections[binding.Connection].Models {
		if model.ID == binding.TargetModel && strings.TrimSpace(model.Name) != "" {
			name = strings.TrimSpace(model.Name)
			break
		}
	}
	if strings.HasPrefix(strings.ToLower(name), "gpt-") {
		parts := strings.Split(name[4:], "-")
		for i, part := range parts {
			runes := []rune(part)
			if len(runes) > 0 {
				runes[0] = unicode.ToUpper(runes[0])
			}
			parts[i] = string(runes)
		}
		name = "GPT " + strings.Join(parts, " ")
	}
	binding.Label = name
	switch slot {
	case "opus":
		binding.Description = "Opus"
	case "sonnet":
		binding.Description = "Sonnet"
	case "haiku":
		binding.Description = "Haiku"
	case "fable":
		binding.Description = "Fable"
	default:
		binding.Description = ""
	}
	return binding
}
