package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/shichao-wang/cpa/internal/config"
	"github.com/shichao-wang/cpa/internal/sdkmodels"
)

// Codex 转换器不执行输出 token 上限。保留普通 Messages 调用，同时明确告知限制；
// 必须执行上限的调用方可要求入口直接拒绝，不能用流式截断伪造预算执行。
func checkRequestCompatibility(r *http.Request, data []byte, route modelRoute, headers http.Header) error {
	if route.protocol != config.Responses {
		return nil
	}
	headers.Set("X-CPA-Output-Limit", "not-enforced")
	headers.Set("X-CPA-Compatibility-Warnings", "output-limit-not-enforced,context-window-unverified")
	if len(r.Header.Values("X-CPA-Require-Output-Limit")) > 1 {
		return fmt.Errorf("X-CPA-Require-Output-Limit 不允许重复")
	}
	requireLimit := strings.TrimSpace(r.Header.Get("X-CPA-Require-Output-Limit"))
	if requireLimit != "" && !strings.EqualFold(requireLimit, "false") {
		if !strings.EqualFold(requireLimit, "true") {
			return fmt.Errorf("X-CPA-Require-Output-Limit 必须为 true 或 false")
		}
		return fmt.Errorf("当前 Responses 路径不执行输出 token 上限，无法满足严格输出预算；请使用支持该限制的连接")
	}
	params, err := parameterObject(data)
	if err != nil {
		return err
	}
	var maxTokens int64
	if value, exists := params["max_tokens"]; exists {
		maxTokens, err = tokenInteger(value, 1)
		if err != nil {
			return fmt.Errorf("max_tokens 必须是 1 到 2147483647 之间的 JSON 整数")
		}
	}
	mode, effort := "", ""
	var budget int64
	hasBudget := false
	if value, exists := params["thinking"]; exists {
		thinking, err := parameterObject(value)
		if err != nil {
			return fmt.Errorf("thinking 必须是无重复字段的 JSON 对象")
		}
		if json.Unmarshal(thinking["type"], &mode) != nil || (mode != "enabled" && mode != "disabled" && mode != "adaptive") {
			return fmt.Errorf("thinking.type 必须为 enabled、disabled 或 adaptive")
		}
		if value, exists := thinking["budget_tokens"]; exists {
			hasBudget = true
			if mode != "enabled" {
				return fmt.Errorf("budget_tokens 仅允许用于 thinking.type=enabled；adaptive 或 disabled 请移除预算字段")
			}
			budget, err = tokenInteger(value, 1024)
			if err != nil {
				return fmt.Errorf("budget_tokens 必须是 1024 到 2147483647 之间的 JSON 整数")
			}
			if maxTokens == 0 || budget >= maxTokens {
				return fmt.Errorf("Responses 兼容请求的 budget_tokens 必须小于 max_tokens；这些预算只参与参数映射，不代表上游执行 token 上限")
			}
		}
	}
	if value, exists := params["output_config"]; exists {
		output, err := parameterObject(value)
		if err != nil {
			return fmt.Errorf("output_config 必须是无重复字段的 JSON 对象")
		}
		if value, exists := output["effort"]; exists {
			if json.Unmarshal(value, &effort) != nil || strings.TrimSpace(effort) == "" {
				return fmt.Errorf("output_config.effort 必须是非空字符串")
			}
			switch strings.ToLower(strings.TrimSpace(effort)) {
			case "none", "auto", "minimal", "low", "medium", "high", "xhigh", "max":
			default:
				return fmt.Errorf("output_config.effort 不支持该档位；请参照模型能力目录")
			}
		}
	}
	capability := sdkmodels.ResponsesCapabilities(route.targetModel)
	if capability.Reasoning == "unavailable" && (mode == "enabled" || mode == "adaptive" || effort != "") {
		return fmt.Errorf("SDK 未识别该模型的推理能力，拒绝静默丢弃 thinking / effort；请移除推理参数或选择已识别模型")
	}
	if effort != "" && !slices.Contains(capability.Efforts, effort) {
		return fmt.Errorf("output_config.effort 不属于该模型支持的档位；请参照模型能力目录")
	}
	warnings := []string{"output-limit-not-enforced", "context-window-unverified"}
	if mode == "disabled" {
		if capability.ThinkingOff == "lowest-effort" {
			warnings = append(warnings, "thinking-disabled-mapped")
		} else if capability.ThinkingOff == "unverified" {
			warnings = append(warnings, "thinking-off-unverified")
		}
	}
	if mode == "adaptive" {
		warnings = append(warnings, "adaptive-effort-mapping")
	}
	if hasBudget {
		warnings = append(warnings, "thinking-budget-mapped")
		if effort != "" {
			warnings = append(warnings, "thinking-budget-overrides-effort")
		}
	}
	if effort != "" && mode == "" {
		warnings = append(warnings, "effort-without-thinking-ignored")
	}
	headers.Set("X-CPA-Compatibility-Warnings", strings.Join(warnings, ","))
	return nil
}

// 不使用 Value() 等宽松转换，避免字符串、null、小数或溢出被当作零预算。
func tokenInteger(raw json.RawMessage, minimum int64) (int64, error) {
	var value int64
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &value) != nil || value < minimum || value > 2147483647 {
		return 0, fmt.Errorf("无效 token 预算")
	}
	return value, nil
}

func parameterObject(raw []byte) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	open, err := decoder.Token()
	if err != nil || open != json.Delim('{') {
		return nil, fmt.Errorf("参数必须是 JSON 对象")
	}
	result := map[string]json.RawMessage{}
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return nil, fmt.Errorf("无法读取参数字段")
		}
		name, ok := key.(string)
		if !ok {
			return nil, fmt.Errorf("无效参数字段")
		}
		if _, exists := result[name]; exists {
			return nil, fmt.Errorf("参数对象不允许重复字段")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, fmt.Errorf("无法读取参数值")
		}
		result[name] = value
	}
	if _, err := decoder.Token(); err != nil {
		return nil, fmt.Errorf("参数对象不完整")
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, fmt.Errorf("参数对象包含多份 JSON")
	}
	return result, nil
}
