package codexoauth

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestDeviceCodeWriterOnlyPublishesAllowlistedCode(t *testing.T) {
	var codes []string
	w := &deviceCodeWriter{publish: func(code string) { codes = append(codes, code) }}
	for _, part := range []string{"access_token: do-not-return\nCodex device co", "de: ABCD-1234\nCodex device URL: https://foreign.example\n", "Codex device code: invalid secret\n", strings.Repeat("x", 5000) + "\n", "Codex device code: EFGH-5678\r\n"} {
		w.Write([]byte(part))
	}
	if !reflect.DeepEqual(codes, []string{"ABCD-1234", "EFGH-5678"}) {
		t.Fatal("设备码输出白名单失效")
	}
	if len(w.line) != 0 {
		t.Fatal("行缓冲未清理")
	}
}
func TestLoginProcessHelper(t *testing.T) {
	if os.Getenv("CPA_OAUTH_HELPER") != "1" {
		return
	}
	fmt.Println("secret-mock-access-token")
	fmt.Println("Codex device code: TEST-1234")
	time.Sleep(20 * time.Second)
	os.Exit(0)
}
func TestLoginProcessCancellationDoesNotExposeOutput(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLoginProcessHelper$")
	cmd.Env = append(os.Environ(), "CPA_OAUTH_HELPER=1")
	got := make(chan string, 1)
	done := make(chan error, 1)
	go func() { done <- runLoginProcess(ctx, cmd, func(code string) { got <- code }) }()
	select {
	case code := <-got:
		if code != "TEST-1234" {
			t.Fatal("设备码错误")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("未接收到设备码")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil || strings.Contains(err.Error(), "secret-mock") {
			t.Fatal("取消结果必须脱敏")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("取消未停止独立登录进程")
	}
}
