package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"

	cliproxy "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy"
	"github.com/shichao-wang/cpa/internal/config"
)

// 能力属于具体连接的已注册模型；不能借用同名模型在其他连接上的能力。
func routeSupportsWebSearch(route modelRoute) bool {
	for _, info := range cliproxy.GlobalModelRegistry().GetModelsForClient(route.authID) {
		if info.ID == route.sdkModel {
			return info.NativeCapabilities != nil && info.NativeCapabilities.WebSearch != nil && *info.NativeCapabilities.WebSearch
		}
	}
	return false
}

// 仅过滤具有明确服务端 type 的声明，不根据名称过滤客户端工具或历史消息。
func filterUnsupportedServerTools(data []byte, route modelRoute, headers http.Header) ([]byte, error) {
	if route.protocol != config.Responses && route.protocol != config.Chat {
		return data, nil
	}
	root, err := parameterObject(data)
	if err != nil {
		return nil, err
	}
	raw, exists := root["tools"]
	if !exists || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return data, nil
	}
	var tools []json.RawMessage
	if json.Unmarshal(raw, &tools) != nil {
		return nil, fmt.Errorf("tools 必须是 JSON 数组")
	}
	kept := make([]json.RawMessage, 0, len(tools))
	removedNames, keptNames := map[string]bool{}, map[string]bool{}
	disabled := []string{}
	for _, rawTool := range tools {
		tool, err := parameterObject(rawTool)
		if err != nil {
			return nil, fmt.Errorf("tools 中的工具必须是无重复字段的 JSON 对象")
		}
		var kind, name string
		if value, ok := tool["type"]; ok && json.Unmarshal(value, &kind) != nil {
			return nil, fmt.Errorf("工具 type 必须是字符串")
		}
		_ = json.Unmarshal(tool["name"], &name)
		family := ""
		for _, candidate := range []string{"web_search", "web_fetch", "code_execution"} {
			if kind == candidate || strings.HasPrefix(kind, candidate+"_") {
				family = candidate
				break
			}
		}
		if family == "" || family == "web_search" && route.protocol == config.Responses && routeSupportsWebSearch(route) {
			kept = append(kept, rawTool)
			keptNames[name] = true
			continue
		}
		removedNames[name] = true
		if !slices.Contains(disabled, family) {
			disabled = append(disabled, family)
		}
	}
	if len(disabled) == 0 {
		return data, nil
	}
	if rawChoice, ok := root["tool_choice"]; ok {
		choice, err := parameterObject(rawChoice)
		if err != nil {
			return nil, fmt.Errorf("tool_choice 必须是无重复字段的 JSON 对象")
		}
		var kind, name string
		_ = json.Unmarshal(choice["type"], &kind)
		_ = json.Unmarshal(choice["name"], &name)
		if kind != "none" && (len(kept) == 0 || kind == "tool" && removedNames[name] && !keptNames[name]) {
			mode := "auto"
			if len(kept) == 0 {
				mode = "none"
			}
			choice["type"], _ = json.Marshal(mode)
			delete(choice, "name")
			root["tool_choice"], _ = json.Marshal(choice)
		}
	} else if len(kept) == 0 {
		root["tool_choice"] = json.RawMessage(`{"type":"none"}`)
	}
	root["tools"], _ = json.Marshal(kept)
	result, err := json.Marshal(root)
	if err != nil {
		return nil, fmt.Errorf("无法更新工具声明")
	}
	// 只输出固定能力名称，不将用户输入的工具名称放入响应头。
	headers.Set("X-CPA-Disabled-Server-Tools", strings.Join(disabled, ","))
	warnings := headers.Get("X-CPA-Compatibility-Warnings")
	if warnings != "" {
		warnings += ","
	}
	headers.Set("X-CPA-Compatibility-Warnings", warnings+"unsupported-server-tools-disabled")
	return result, nil
}
