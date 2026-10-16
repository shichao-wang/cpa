// Package webui 提供与网关同生命周期的管理页面和接口。
package webui

import (
	"encoding/hex"
	"fmt"
	"os"
	"strings"

	"github.com/shichao-wang/cpa/internal/config"
	"github.com/shichao-wang/cpa/internal/store"
)

// Token 从独立的 0600 文件读取管理令牌；首次使用时在锁内生成。
// 令牌不进入网关状态、SDK 配置或服务日志。
func Token(st *store.Store) (string, error) {
	var token string
	err := store.WithLock(st.Path("admin-token.lock"), func() error {
		path := st.Path("admin-token")
		if err := store.CheckFile(path); err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			token, err = config.RandomID()
			if err != nil {
				return fmt.Errorf("生成管理令牌失败")
			}
			return store.AtomicWrite(path, []byte(token+"\n"))
		}
		if err != nil {
			return fmt.Errorf("读取管理令牌失败")
		}
		token = strings.TrimSpace(string(data))
		decoded, err := hex.DecodeString(token)
		if err != nil || len(decoded) != 32 {
			return fmt.Errorf("管理令牌文件无效")
		}
		return nil
	})
	return token, err
}
