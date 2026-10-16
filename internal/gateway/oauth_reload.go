package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"reflect"

	"github.com/gin-gonic/gin"
	sdkapi "github.com/router-for-me/CLIProxyAPI/v8/sdk/api"
	cliproxy "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/shichao-wang/cpa/internal/codexoauth"
	"github.com/shichao-wang/cpa/internal/config"
)

// sdkCodexModels 通过公开 SDK 的只读处理器取得模型能力，不开放管理路由或网络监听。
func sdkCodexModels() ([]*cliproxy.ModelInfo, error) { return sdkStaticModels("codex") }

func sdkStaticModels(channel string) ([]*cliproxy.ModelInfo, error) {
	router := gin.New()
	router.GET("/models", new(sdkapi.Handler).GetStaticModelDefinitions)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest("GET", "/models?channel="+channel, nil))
	var body struct {
		Models []*cliproxy.ModelInfo `json:"models"`
	}
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &body) != nil || len(body.Models) == 0 {
		return nil, fmt.Errorf("无法读取 SDK 模型能力目录")
	}
	return body.Models, nil
}

func oauthBindings(s *snapshot) map[string]connectionInfo {
	result := map[string]connectionInfo{}
	for _, c := range s.connections {
		if c.authType == config.AuthCodexOAuth {
			result[c.name] = c
		}
	}
	return result
}

// oauthHotUpdateAllowed 只允许新增、登录凭证替换及模型编辑；删除、身份替换和认证切换仍需停机。
func oauthHotUpdateAllowed(previous, next *snapshot) bool {
	after := oauthBindings(next)
	for name, old := range oauthBindings(previous) {
		c, exists := after[name]
		if !exists || old.id != c.id || old.baseURL != c.baseURL || old.protocol != c.protocol {
			return false
		}
	}
	return true
}

// reconcileOAuth 只注册新增凭证，不调用 Load 或替换已有执行器；静态更新不能复活失效认证。
func reconcileOAuth(ctx context.Context, manager *coreauth.Manager, credentials *codexoauth.SDKStore, previous, next *snapshot) ([]string, []string, error) {
	before := oauthBindings(previous)
	var added, retired []string
	var records map[string]*coreauth.Auth
	rollback := func(err error) ([]string, []string, error) {
		for _, id := range added {
			manager.Remove(ctx, id)
			cliproxy.GlobalModelRegistry().UnregisterClient(id)
		}
		return nil, nil, err
	}
	for name, c := range oauthBindings(next) {
		old, existed := before[name]
		changed := !existed || old.credentialRef != c.credentialRef
		if changed && existed && old.credentialRef != "" {
			retired = append(retired, credentials.AuthID(old.credentialRef))
		}
		if c.credentialRef == "" {
			continue
		}
		id := credentials.AuthID(c.credentialRef)
		if !credentials.Healthy(id) {
			continue
		}
		if changed {
			if records == nil {
				auths, err := credentials.List(ctx)
				if err != nil {
					return rollback(fmt.Errorf("读取新增 OAuth 认证失败"))
				}
				records = map[string]*coreauth.Auth{}
				for _, a := range auths {
					records[a.ID] = a
				}
			}
			auth := records[id]
			if auth == nil || auth.Metadata["cpa_connection_id"] != c.id {
				return rollback(fmt.Errorf("新增 OAuth 认证绑定已改变"))
			}
			if _, exists := manager.GetByID(id); exists {
				return rollback(fmt.Errorf("OAuth 凭证引用不得复用"))
			}
			if _, err := manager.Register(coreauth.WithSkipPersist(ctx), auth); err != nil {
				return rollback(fmt.Errorf("注册新增 OAuth 认证失败"))
			}
			added = append(added, id)
		}
		if !changed && reflect.DeepEqual(old.models, c.models) {
			continue
		}
		if _, exists := manager.GetByID(id); !exists {
			continue
		}
		models, err := sdkCodexModels()
		if err != nil {
			return rollback(err)
		}
		selected := map[string]bool{}
		for _, m := range c.models {
			selected[m.ID] = true
		}
		var registered []*cliproxy.ModelInfo
		for _, m := range models {
			if selected[m.ID] {
				m.MetadataModelID = m.ID
				m.ID = c.prefix + "/" + m.ID
				registered = append(registered, m)
			}
		}
		cliproxy.GlobalModelRegistry().RegisterClient(id, "codex", registered)
		manager.RefreshSchedulerEntry(id)
	}
	return added, retired, nil
}
