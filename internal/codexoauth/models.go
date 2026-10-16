package codexoauth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"time"

	"github.com/shichao-wang/cpa/internal/config"
	"github.com/shichao-wang/cpa/internal/sdkmodels"
)

// 与锁定 SDK 的 Codex 目录客户端版本一致，仅请求官方账号目录。
const codexClientVersion = "0.159.0"

// DiscoverModels 获取当前 OAuth 账号的模型目录，不发送推理请求或刷新凭证。
func DiscoverModels(ctx context.Context, credential config.OAuthCredential) ([]config.Model, error) {
	return discoverModels(ctx, credential, http.DefaultTransport)
}

func discoverModels(ctx context.Context, credential config.OAuthCredential, transport http.RoundTripper) ([]config.Model, error) {
	if err := config.ValidateOAuthCredential(credential); err != nil {
		return nil, fmt.Errorf("OAuth 凭证无效，请重新登录")
	}
	if !credential.ExpiresAt.After(time.Now()) {
		return nil, fmt.Errorf("OAuth 凭证已过期，请重新登录")
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, config.CodexBaseURL+"/models?client_version="+codexClientVersion, nil)
	if err != nil {
		return nil, fmt.Errorf("无法创建 OAuth 模型目录请求")
	}
	request.Header.Set("Authorization", "Bearer "+credential.AccessToken)
	request.Header.Set("Chatgpt-Account-Id", credential.AccountID)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Originator", "codex_cli_rs")
	request.Header.Set("User-Agent", "codex_cli_rs/"+codexClientVersion)
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("OAuth 模型目录请求失败，请检查网络后重试")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("OAuth 模型目录返回 HTTP %d，请重试或重新登录", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (4<<20)+1))
	if err != nil || len(data) > 4<<20 {
		return nil, fmt.Errorf("OAuth 模型目录读取失败或超过 4 MiB")
	}
	var catalog struct {
		Models []struct {
			ID         string `json:"slug"`
			Name       string `json:"display_name"`
			Visibility string `json:"visibility"`
		} `json:"models"`
	}
	if err = json.Unmarshal(data, &catalog); err != nil || catalog.Models == nil {
		return nil, fmt.Errorf("OAuth 模型目录格式无效")
	}
	if len(catalog.Models) > 10000 {
		return nil, fmt.Errorf("OAuth 模型目录超过 10000 个模型")
	}
	definitions, err := sdkmodels.Definitions("codex")
	if err != nil {
		return nil, err
	}
	supported := map[string]bool{}
	for _, model := range definitions {
		supported[model.ID] = true
	}
	models := make([]config.Model, 0, len(catalog.Models))
	seen := map[string]bool{}
	for _, item := range catalog.Models {
		// OAuth 账号使用 ChatGPT 目录，不套用 API_KEY 的 supported_in_api 过滤。
		if item.Visibility != "list" {
			continue
		}
		model := config.Model{ID: item.ID, Name: item.Name}
		if err = config.ValidateModels([]config.Model{model}); err != nil {
			return nil, fmt.Errorf("OAuth 模型目录包含无效模型")
		}
		if supported[model.ID] && !seen[model.ID] {
			models = append(models, model)
			seen[model.ID] = true
		}
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("OAuth 账号未返回当前 SDK 支持的可用模型，请检查账号权限或升级 cpa")
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models, nil
}
