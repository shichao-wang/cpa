package gateway

import (
	"context"
	"fmt"
	"reflect"

	cliproxy "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	executortypes "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/shichao-wang/cpa/internal/config"
	"github.com/shichao-wang/cpa/internal/sdkmodels"
)

// staticAddOnly 允许增加连接，已有静态连接的修改仍使用完整配置更新。
func staticAddOnly(before, after *snapshot) bool {
	next := map[string]connectionInfo{}
	for _, c := range after.connections {
		next[c.name] = c
	}
	for _, c := range before.connections {
		if c.authType == config.AuthAPIKey && !reflect.DeepEqual(c, next[c.name]) {
			return false
		}
	}
	return true
}

// compatExecutor 给新 chat 连接独立的路由标识，复用 SDK 的公开执行接口。
// 不向旧配置传递新 config_index，endpoint 和 key 始终来自该连接的认证记录。
type compatExecutor struct {
	coreauth.ProviderExecutor
	name string
}

func (e *compatExecutor) Identifier() string { return e.name }
func compatAuth(a *coreauth.Auth) *coreauth.Auth {
	a = a.Clone()
	delete(a.Attributes, "config_index")
	return a
}
func (e *compatExecutor) Execute(ctx context.Context, a *coreauth.Auth, r executortypes.Request, o executortypes.Options) (executortypes.Response, error) {
	return e.ProviderExecutor.Execute(ctx, compatAuth(a), r, o)
}
func (e *compatExecutor) ExecuteStream(ctx context.Context, a *coreauth.Auth, r executortypes.Request, o executortypes.Options) (*executortypes.StreamResult, error) {
	return e.ProviderExecutor.ExecuteStream(ctx, compatAuth(a), r, o)
}
func (e *compatExecutor) CountTokens(ctx context.Context, a *coreauth.Auth, r executortypes.Request, o executortypes.Options) (executortypes.Response, error) {
	return e.ProviderExecutor.CountTokens(ctx, compatAuth(a), r, o)
}

func registerStaticAdditions(ctx context.Context, manager *coreauth.Manager, previous, next *snapshot, namespace string) error {
	old := map[string]bool{}
	for _, c := range previous.connections {
		old[c.id] = true
	}
	wanted := staticAuths(next, namespace)
	for _, c := range next.connections {
		if c.authType != config.AuthAPIKey || old[c.id] {
			continue
		}
		id := staticAuthID(namespace, c.id)
		auth := wanted[id]
		if _, exists := manager.Executor(auth.Provider); !exists {
			base, ok := manager.Executor("openai-compatibility")
			if c.protocol != config.Chat || !ok {
				return fmt.Errorf("新增连接缺少协议执行器")
			}
			manager.RegisterExecutor(&compatExecutor{ProviderExecutor: base, name: auth.Provider})
		}
		if _, err := manager.Register(coreauth.WithSkipPersist(ctx), auth); err != nil {
			return fmt.Errorf("注册新增连接失败")
		}
		var models []*cliproxy.ModelInfo
		// 保留 SDK 已知模型的 reasoning 等能力，避免新增连接与重启后的行为不同。
		known := map[string]*cliproxy.ModelInfo{}
		for _, channel := range []string{"codex", "claude"} {
			catalog, err := sdkmodels.Definitions(channel)
			if err != nil {
				return err
			}
			for _, info := range catalog {
				known[info.ID] = info
			}
		}
		for _, m := range c.models {
			info := &cliproxy.ModelInfo{}
			if base := known[m.ID]; base != nil {
				*info = *base
			}
			info.ID = c.prefix + "/" + m.ID
			info.MetadataModelID = m.ID
			info.Object = "model"
			info.OwnedBy = auth.Provider
			info.DisplayName = m.Name
			if info.DisplayName == "" {
				info.DisplayName = m.ID
			}
			info.UserDefined = true
			models = append(models, info)
		}
		cliproxy.GlobalModelRegistry().RegisterClient(id, auth.Provider, models)
		manager.RefreshSchedulerEntry(id)
	}
	// 只更新 Manager 的不可变配置快照，不重新安装执行器或重新读取 OAuth。
	manager.SetConfig(next.config)
	return reconcileStaticAuths(ctx, manager, next, namespace)
}
