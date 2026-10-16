// Package sdkmodels 通过公开 SDK 接口读取静态模型能力，不开放网络监听。
package sdkmodels

import (
	"encoding/json"
	"fmt"
	"github.com/gin-gonic/gin"
	sdkapi "github.com/router-for-me/CLIProxyAPI/v8/sdk/api"
	cliproxy "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy"
	"net/http/httptest"
)

// Definitions 返回锁定 SDK 支持的模型及能力。
func Definitions(channel string) ([]*cliproxy.ModelInfo, error) {
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
