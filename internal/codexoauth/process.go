package codexoauth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/shichao-wang/cpa/internal/config"
	"github.com/shichao-wang/cpa/internal/store"
)

const VerificationURL = "https://auth.openai.com/codex/device"

var deviceCodePattern = regexp.MustCompile(`^[A-Z0-9-]{4,32}$`)

// LoginDraftProcess 只取得短期授权结果，不创建 connection 或写入 cpa 状态。
// 子进程通过 0700 临时目录中的 0600 文件交接凭证，读取后立即移除。
func LoginDraftProcess(ctx context.Context, publish func(string)) (config.OAuthCredential, error) {
	dir, err := os.MkdirTemp("", "cpa-oauth-")
	if err != nil {
		return config.OAuthCredential{}, fmt.Errorf("无法创建临时授权会话")
	}
	defer os.RemoveAll(dir)
	exe, err := os.Executable()
	if err != nil {
		return config.OAuthCredential{}, fmt.Errorf("无法启动 OAuth 登录进程")
	}
	path := filepath.Join(dir, "credential.json")
	cmd := exec.CommandContext(ctx, exe, "connection", "oauth-login-worker", path)
	if err = runLoginProcess(ctx, cmd, publish); err != nil {
		return config.OAuthCredential{}, err
	}
	if err = store.CheckFile(path); err != nil {
		return config.OAuthCredential{}, fmt.Errorf("无法读取临时授权结果")
	}
	f, err := os.Open(path)
	if err != nil {
		return config.OAuthCredential{}, fmt.Errorf("无法读取临时授权结果")
	}
	defer f.Close()
	var credential config.OAuthCredential
	if err = json.NewDecoder(io.LimitReader(f, 1<<20)).Decode(&credential); err != nil {
		return config.OAuthCredential{}, fmt.Errorf("临时授权结果无效")
	}
	if err = config.ValidateOAuthCredential(credential); err != nil {
		return config.OAuthCredential{}, fmt.Errorf("临时授权结果无效")
	}
	return credential, nil
}

// LoginProcess 在独立进程调用公开 SDK，避免重定向主服务的全局 stdout。
// 只发布设备码；SDK 的其他输出及错误正文均不进入管理响应或日志。
func LoginProcess(ctx context.Context, name string, publish func(string)) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("无法启动 OAuth 登录进程")
	}
	cmd := exec.CommandContext(ctx, exe, "connection", "login", name, "--no-browser")
	return runLoginProcess(ctx, cmd, publish)
}

func runLoginProcess(ctx context.Context, cmd *exec.Cmd, publish func(string)) error {
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 3 * time.Second
	cmd.Stdout = &deviceCodeWriter{publish: publish}
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("Codex OAuth 登录失败，请检查网络或重新授权")
	}
	return nil
}

type deviceCodeWriter struct {
	line    []byte
	discard bool
	publish func(string)
}

func (w *deviceCodeWriter) Write(data []byte) (int, error) {
	for _, b := range data {
		if b == '\n' {
			if !w.discard {
				line := strings.TrimSuffix(string(w.line), "\r")
				if code, ok := strings.CutPrefix(line, "Codex device code: "); ok && deviceCodePattern.MatchString(code) {
					w.publish(code)
				}
			}
			w.line = nil
			w.discard = false
		} else if !w.discard {
			if len(w.line) >= 256 {
				w.line = nil
				w.discard = true
			} else {
				w.line = append(w.line, b)
			}
		}
	}
	return len(data), nil
}
