// Package agent 汇总各 Agent 配置对 profile 的引用。
package agent

import (
	"github.com/shichao-wang/cpa/internal/agent/claudecode"
	"github.com/shichao-wang/cpa/internal/agent/claudedesktop"
	"github.com/shichao-wang/cpa/internal/store"
)

// ReferencesProfileID 查询所有适配器的管理记录，不根据 profile 当前 agent 选择适配器。
// 适配器只持有自己的管理记录锁，调用方可以持有状态锁。
func ReferencesProfileID(s *store.Store, profileID string) (bool, error) {
	code, err := claudecode.ReferencesProfileID(s, profileID)
	if err != nil {
		return false, err
	}
	desktop, err := claudedesktop.ReferencesProfileID(s, profileID)
	if err != nil {
		return false, err
	}
	return code || desktop, nil
}

// IsManagedProfile 将名称解析为稳定 ID；无法确认引用时按受保护处理。
func IsManagedProfile(s *store.Store, profileName string) bool {
	if s == nil || profileName == "" {
		return false
	}
	state, err := s.Read()
	if err != nil {
		return true
	}
	profile, exists := state.Profiles[profileName]
	if !exists {
		return false
	}
	protected, err := ReferencesProfileID(s, profile.ID)
	return err != nil || protected
}
