package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shichao-wang/cpa/internal/gateway"
	"github.com/shichao-wang/cpa/internal/store"
	"github.com/shichao-wang/cpa/internal/webui"
)

func TestManagementRequiresLiveModelReadiness(t *testing.T) {
	st, err := store.New(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	var ready atomic.Bool
	sdk := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != gateway.ReadyPath || r.Header.Get("X-CPA-Instance-Token") != "mock-probe" {
			w.WriteHeader(404)
			return
		}
		fmt.Fprintf(w, `{"ready":%t,"instanceID":"model-test","revision":42}`, ready.Load())
	}))
	defer sdk.Close()
	runtime := gateway.RuntimeState{PID: 2, InstanceID: "model-test", StartTime: "test", Listen: "127.0.0.1:12345", ProbeToken: "mock-probe", Ready: true, ActiveRevision: 42}
	if err = store.WriteJSON(st.Path(gateway.RuntimeFileName), runtime); err != nil {
		t.Fatal(err)
	}
	c := &modelController{ctx: context.Background(), store: st, instanceID: runtime.InstanceID, mode: "running", address: strings.TrimPrefix(sdk.URL, "http://")}
	if _, active := c.active(); active {
		t.Fatal("文件中的 ready 不能代替模型服务的实际就绪状态")
	}
	ready.Store(true)
	if revision, active := c.active(); !active || revision != 42 {
		t.Fatal("应在模型服务实际就绪后报告修订已生效")
	}
}

func TestModelControllerPauseResumeRetainsManagement(t *testing.T) {
	st, err := store.New(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	// 只启动空状态下的 SDK，不连接上游或登录账户。
	if _, err = st.Read(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	c := &modelController{ctx: ctx, store: st, instanceID: "test-controller", mode: "paused"}
	t.Cleanup(func() { cancel(); _ = c.Pause(context.Background()) })
	h, err := webui.New(st, "[::1]:18317", c.active, c, ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	public := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if webui.Handles(r.URL.Path) {
			h.ServeHTTP(w, r)
		} else {
			c.serveModel(w, r)
		}
	}))
	defer public.Close()
	if err = c.Resume(ctx); err != nil {
		t.Fatal(err)
	}
	if lock, e := st.AcquireRunLock(); e == nil {
		lock.Close()
		t.Fatal("运行中必须持有运行锁")
	}
	request := func(path string, want int) {
		t.Helper()
		resp, e := http.Get(public.URL + path)
		if e != nil {
			t.Fatal(e)
		}
		defer resp.Body.Close()
		if resp.StatusCode != want {
			t.Fatalf("%s = %d，预期 %d", path, resp.StatusCode, want)
		}
	}
	// 私有 SDK 端口不能绕过公开管理接口的令牌策略。
	privateResponse, err := http.Get("http://" + c.address + "/admin/api/state")
	if err != nil {
		t.Fatal(err)
	}
	privateResponse.Body.Close()
	if privateResponse.StatusCode != http.StatusUnauthorized {
		t.Fatalf("私有管理接口无令牌状态 %d，预期 401", privateResponse.StatusCode)
	}
	request("/ui/", 200)
	request("/v1/models", 401)
	if err = c.Pause(ctx); err != nil {
		t.Fatal(err)
	}
	lock, err := st.AcquireRunLock()
	if err != nil {
		t.Fatalf("确认停机后应允许 OAuth 获得运行锁：%v", err)
	}
	lock.Close()
	request("/ui/", 200)
	request("/v1/models", 503)
	request(gateway.ReadyPath, 404)
	runtime, err := readRuntime(st)
	if err != nil {
		t.Fatal(err)
	}
	runtime.Listen = public.Listener.Addr().String()
	if _, err = probe(ctx, runtime); !errors.Is(err, errMaintenance) {
		t.Fatalf("暂停状态探测：%v", err)
	}
	if err = c.Resume(ctx); err != nil {
		t.Fatal(err)
	}
	if c.Mode() != "running" {
		t.Fatal(c.Mode())
	}
	request("/ui/", 200)
	request("/v1/models", 401)
}

func TestModelControllerPauseCancelsStreamingOutput(t *testing.T) {
	upstreamStopped := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(upstreamStopped)
	}))
	defer upstream.Close()
	u, _ := url.Parse(upstream.URL)
	proxy := httputil.NewSingleHostReverseProxy(u)
	proxy.FlushInterval = -1
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { <-runCtx.Done(); close(done) }()
	c := &modelController{mode: "running", runCtx: runCtx, cancel: cancel, done: done, proxy: proxy}
	public := httptest.NewServer(http.HandlerFunc(c.serveModel))
	defer public.Close()
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Get(public.URL + "/v1/messages")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	first := make([]byte, len("data: first\n\n"))
	if _, err = io.ReadFull(response.Body, first); err != nil {
		t.Fatal(err)
	}
	if string(first) != "data: first\n\n" {
		t.Fatal("SSE 必须即时转发")
	}
	if err = c.Pause(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-upstreamStopped:
	case <-time.After(5 * time.Second):
		t.Fatal("暂停必须取消上游在途输出")
	}
	if data, _ := io.ReadAll(response.Body); len(data) != 0 {
		t.Fatal("暂停后不应继续输出")
	}
}
