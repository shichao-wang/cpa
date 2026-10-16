package store

import (
	"fmt"
	"strings"

	"github.com/shichao-wang/cpa/internal/config"
	"golang.org/x/sys/unix"
)

const RunLockFileName = ".gateway.lock"

type OAuthBinding struct {
	Connection    string
	ConnectionID  string
	CredentialRef string
	Generation    uint64
}

// SaveOAuth 仅通过 CAS 写入 token，不修改 State.Revision。
func (s *Store) SaveOAuth(binding OAuthBinding, credential config.OAuthCredential) (uint64, error) {
	var generation uint64
	err := WithLock(s.Path("state.lock"), func() error {
		state, err := s.Read()
		if err != nil {
			return err
		}
		c, ok := state.Connections[binding.Connection]
		if !ok || c.ID != binding.ConnectionID || c.AuthType != config.AuthCodexOAuth || c.BaseURL != config.CodexBaseURL || c.CredentialRef != binding.CredentialRef {
			return fmt.Errorf("OAuth 凭证绑定已变化")
		}
		var previous config.OAuthCredential
		if binding.CredentialRef != "" {
			var exists bool
			previous, exists = state.OAuthCredentials[binding.CredentialRef]
			if !exists || previous.Generation != binding.Generation {
				return fmt.Errorf("OAuth 凭证版本已变化")
			}
			if credential.AccountID != previous.AccountID {
				return fmt.Errorf("OAuth 账号身份已变化")
			}
		} else if binding.Generation != 0 {
			return fmt.Errorf("OAuth 凭证版本无效")
		}
		if strings.TrimSpace(binding.CredentialRef) == "" {
			return fmt.Errorf("OAuth 凭证引用不能为空")
		}
		credential.Generation = previous.Generation
		if credential == previous {
			generation = previous.Generation
			return nil
		}
		credential.Generation = binding.Generation + 1
		if err := config.ValidateOAuthCredential(credential); err != nil {
			return err
		}
		state.OAuthCredentials[binding.CredentialRef] = credential
		if err := state.Validate(); err != nil {
			return err
		}
		if err := WriteJSON(s.Path("state.json"), state); err != nil {
			return err
		}
		generation = credential.Generation
		return nil
	})
	return generation, err
}

type RunLock struct{ fd int }

// AcquireRunLock 获取与网关共用的非阻塞独占锁，防止停机操作与模型服务并行。
func (s *Store) AcquireRunLock() (*RunLock, error) {
	return s.acquireExclusive(RunLockFileName)
}

// AcquireOAuthLock 仅串行化 OAuth 登录，不占用模型服务的运行锁。
func (s *Store) AcquireOAuthLock() (*RunLock, error) {
	return s.acquireExclusive(".oauth-login.lock")
}
func (s *Store) acquireExclusive(name string) (*RunLock, error) {
	path := s.Path(name)
	if err := CheckFile(path); err != nil {
		return nil, err
	}
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	if err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("目标运行锁或 OAuth 登录会话已被占用")
	}
	return &RunLock{fd: fd}, nil
}

func (l *RunLock) Close() error {
	if l == nil || l.fd < 0 {
		return nil
	}
	_ = unix.Flock(l.fd, unix.LOCK_UN)
	err := unix.Close(l.fd)
	l.fd = -1
	return err
}

func (s *Store) OAuthBinding(connection string) (OAuthBinding, error) {
	state, err := s.Read()
	if err != nil {
		return OAuthBinding{}, err
	}
	c, ok := state.Connections[connection]
	if !ok {
		return OAuthBinding{}, fmt.Errorf("连接不存在：%s", connection)
	}
	if c.AuthType != config.AuthCodexOAuth {
		return OAuthBinding{}, fmt.Errorf("连接未声明 Codex OAuth")
	}
	generation := uint64(0)
	if cred, ok := state.OAuthCredentials[c.CredentialRef]; ok {
		generation = cred.Generation
	}
	return OAuthBinding{Connection: connection, ConnectionID: c.ID, CredentialRef: c.CredentialRef, Generation: generation}, nil
}
