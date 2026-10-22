package integration

import (
	"io"
	"net"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEmptyBinaryStartsFailClosed(t *testing.T) {
	if testing.Short() {
		t.Skip("短测试模式跳过子进程验收")
	}
	tmp := t.TempDir()
	binary := filepath.Join(tmp, "cpa")
	build := exec.Command("go", "build", "-o", binary, "./cmd/cpa")
	build.Dir = filepath.Join("..", "..")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, endpoint := range []struct{ name, network, address string }{
		{"IPv4", "tcp4", "127.0.0.1:0"},
		{"IPv6", "tcp6", "[::1]:0"},
	} {
		t.Run(endpoint.name, func(t *testing.T) {
			configHome := filepath.Join(tmp, endpoint.name)
			t.Setenv("XDG_CONFIG_HOME", configHome)
			call := func(args ...string) (string, error) {
				out, err := exec.Command(binary, args...).CombinedOutput()
				return string(out), err
			}
			socket, err := net.Listen(endpoint.network, endpoint.address)
			if err != nil {
				if endpoint.network == "tcp6" {
					t.Skipf("当前环境不支持 IPv6：%v", err)
				}
				t.Fatal(err)
			}
			address := socket.Addr().String()
			socket.Close()
			if out, err := call("server", "start", "--listen", address); err != nil {
				t.Fatalf("空配置必须允许启动：%v %s", err, out)
			}
			t.Cleanup(func() { _, _ = call("server", "stop") })
			if _, err := call("server", "start"); err == nil {
				t.Fatal("重复启动必须拒绝")
			}
			client := &http.Client{Timeout: 3 * time.Second}
			for path, want := range map[string]int{"/v1/models": 401, "/__cpa/ready": 404, "/v8/management/config": 404, "/ui/": 404, "/ui/app.js": 404, "/admin/api/state": 404, "/management.html": 404} {
				req, err := http.NewRequest("GET", "http://"+address+path, nil)
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("X-Api-Key", "not-a-profile-key")
				response, err := client.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				_, _ = io.Copy(io.Discard, response.Body)
				response.Body.Close()
				if response.StatusCode != want {
					t.Errorf("%s = %d，预期 %d", path, response.StatusCode, want)
				}
			}
			if out, err := call("server", "stop"); err != nil {
				t.Fatalf("%v %s", err, out)
			}
			if out, err := call("server", "status"); err != nil || !strings.Contains(out, `"state":"stopped"`) {
				t.Fatalf("%v %s", err, out)
			}
		})
	}
}
