package sdkmodels

import (
	"slices"
	"strings"
	"sync"

	cliproxy "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy"
)

// ResponsesCompatibility 描述本工具锁定的 SDK 路径，不代表上游实测容量或原生能力。
type ResponsesCompatibility struct {
	OutputLimit   string   `json:"outputLimit"`
	Reasoning     string   `json:"reasoning"`
	Efforts       []string `json:"efforts"`
	ThinkingOff   string   `json:"thinkingOff"`
	ContextWindow string   `json:"contextWindow"`
	Warnings      []string `json:"warnings"`
}

var codexDefinitions = sync.OnceValue(func() map[string]*cliproxy.ModelInfo {
	models, err := Definitions("codex")
	if err != nil {
		return nil
	}
	result := make(map[string]*cliproxy.ModelInfo, len(models))
	for _, model := range models {
		result[model.ID] = model
	}
	return result
})

func ResponsesCapabilities(model string) ResponsesCompatibility {
	c := ResponsesCompatibility{
		OutputLimit: "not-enforced", Reasoning: "unavailable", Efforts: []string{},
		ThinkingOff: "unverified", ContextWindow: "unverified",
		Warnings: []string{
			"输出预算：当前 Responses 路径不执行 max_tokens / max_output_tokens 上限；这些参数不能用于控制消耗。",
			"上下文：客户端的 200K / 1M 显示未经上游容量验证，[1m] 不会扩容。",
			"托管工具：自动关闭 web_fetch / code_execution；web_search 仅在当前连接模型明确支持原生搜索时保留，其 max_uses 不映射。客户端 Bash / WebFetch 等普通工具不受影响。",
		},
	}
	info := codexDefinitions()[model]
	if info == nil || info.Thinking == nil || len(info.Thinking.Levels) == 0 {
		c.Warnings = append(c.Warnings, "推理：SDK 未识别该模型的推理能力；显式启用 thinking 或 effort 会被拒绝，普通对话仍可使用。")
		return c
	}
	c.Reasoning = "effort-mapping"
	c.Efforts = slices.Clone(info.Thinking.Levels)
	c.ThinkingOff = "lowest-effort"
	if slices.Contains(c.Efforts, "none") {
		c.ThinkingOff = "none"
	}
	c.Warnings = append(c.Warnings, "推理：effort 可用值为 "+strings.Join(c.Efforts, "、")+"；adaptive 和 budget 按 SDK 转换为离散 effort，不等同于 Anthropic 原生 adaptive。")
	if c.ThinkingOff == "lowest-effort" {
		c.Warnings = append(c.Warnings, "关闭 thinking 会降到最低支持档，不能保证完全关闭内部推理。")
	}
	return c
}
