package connection

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"

	"github.com/shichao-wang/cpa/internal/config"
)

// DiscoverModels 获取 API key 连接的完整模型目录，不保存配置或调用推理接口。
func DiscoverModels(ctx context.Context, c config.Connection, apiKey string) ([]config.Model, error) {
	if c.AuthType != config.AuthAPIKey {
		return nil, fmt.Errorf("仅 API key 连接支持模型目录发现")
	}
	switch c.Protocol {
	case config.Chat, config.Responses, config.Anthropic:
	default:
		return nil, fmt.Errorf("不支持的连接协议")
	}
	if err := config.ValidateURL(c.BaseURL); err != nil {
		return nil, err
	}
	if err := config.ValidateKey(apiKey); err != nil {
		return nil, fmt.Errorf("连接缺少有效的 API key")
	}
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	models := make([]config.Model, 0)
	seen := map[string]bool{}
	cursors := map[string]bool{}
	after := ""
	remaining := int64(4 << 20)
	for range 100 {
		resp, err := modelDirectoryResponse(ctx, c, apiKey, after)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			resp.Body.Close()
			return nil, fmt.Errorf("模型目录返回 HTTP %d，请检查地址、API key 与访问权限", resp.StatusCode)
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, remaining+1))
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("模型目录读取失败，请重新测试连接")
		}
		remaining -= int64(len(data))
		if remaining < 0 {
			return nil, fmt.Errorf("模型目录响应超过 4 MiB，无法完整获取")
		}
		var page struct {
			Data []struct {
				ID          string `json:"id"`
				Name        string `json:"name"`
				DisplayName string `json:"display_name"`
				Lifecycle   string `json:"lifecycle"`
			} `json:"data"`
			HasMore bool   `json:"has_more"`
			LastID  string `json:"last_id"`
		}
		if err := json.Unmarshal(data, &page); err != nil || page.Data == nil {
			return nil, fmt.Errorf("上游未返回有效的模型目录，请确认地址支持模型列表接口")
		}
		for _, item := range page.Data {
			name := item.Name
			if c.Protocol == config.Anthropic {
				if item.Lifecycle == "retired" {
					continue
				}
				if item.DisplayName != "" {
					name = item.DisplayName
				}
			}
			model := config.Model{ID: item.ID, Name: name}
			if err := config.ValidateModels([]config.Model{model}); err != nil {
				return nil, fmt.Errorf("模型目录包含无效的模型 ID 或展示名")
			}
			if !seen[model.ID] {
				models = append(models, model)
				seen[model.ID] = true
			}
		}
		if len(models) > 10000 {
			return nil, fmt.Errorf("模型目录超过 10000 个模型，无法完整获取")
		}
		if !page.HasMore {
			if len(models) == 0 {
				return nil, fmt.Errorf("连接可访问，但未返回可用模型，请检查 API key 的模型权限")
			}
			sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
			return models, nil
		}
		if c.Protocol != config.Anthropic || page.LastID == "" || cursors[page.LastID] {
			return nil, fmt.Errorf("模型目录分页无效，无法完整获取")
		}
		after = page.LastID
		cursors[after] = true
	}
	return nil, fmt.Errorf("模型目录超过分页限制，无法完整获取")
}
