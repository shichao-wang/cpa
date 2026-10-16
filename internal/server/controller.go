package server

import (
	"context"
	"crypto/subtle"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"time"

	"github.com/shichao-wang/cpa/internal/gateway"
	"github.com/shichao-wang/cpa/internal/store"
	"github.com/shichao-wang/cpa/internal/webui"
)

// modelController 独立拥有 SDK 生命周期；公共管理监听不持有网关运行锁。
type modelController struct {
	ctx        context.Context
	store      *store.Store
	instanceID string
	op         sync.Mutex
	mu         sync.RWMutex
	mode       string
	cancel     context.CancelFunc
	done       chan struct{}
	runCtx     context.Context
	proxy      *httputil.ReverseProxy
	address    string
}

func (c *modelController) Mode() string        { c.mu.RLock(); defer c.mu.RUnlock(); return c.mode }
func (c *modelController) setMode(mode string) { c.mu.Lock(); c.mode = mode; c.mu.Unlock() }
func (c *modelController) active() (uint64, bool) {
	r, err := readRuntime(c.store)
	if err != nil {
		return 0, false
	}
	c.mu.RLock()
	mode, address := c.mode, c.address
	c.mu.RUnlock()
	if mode != "running" || !r.Ready || r.InstanceID != c.instanceID || address == "" {
		return r.ActiveRevision, false
	}
	// 热更新先持久化修订，随后才开放请求 gate；必须以 SDK 的实时探测为准。
	private := r
	private.Listen = address
	revision, err := probe(c.ctx, private)
	return revision, err == nil && revision == r.ActiveRevision
}
func (c *modelController) Pause(ctx context.Context) error {
	c.op.Lock()
	defer c.op.Unlock()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	c.mu.RLock()
	cancel, done := c.cancel, c.done
	c.mu.RUnlock()
	if cancel == nil {
		c.setMode("paused")
		return nil
	}
	c.setMode("pausing")
	cancel()
	// 必须确认 SDK 完全退出后才能释放 OAuth 操作；不能把超时当作已停机。
	select {
	case <-done:
		c.setMode("paused")
		return nil
	case <-time.After(40 * time.Second):
		c.setMode("failed")
		return fmt.Errorf("模型服务仍在停止，请稍后重试")
	}
}
func (c *modelController) Resume(_ context.Context) error {
	c.op.Lock()
	defer c.op.Unlock()
	if c.ctx.Err() != nil {
		return c.ctx.Err()
	}
	c.mu.RLock()
	done := c.done
	mode := c.mode
	c.mu.RUnlock()
	if done != nil {
		select {
		case <-done:
		default:
			if mode == "running" {
				return nil
			}
			return fmt.Errorf("模型服务尚未完全停止")
		}
	}
	s, err := c.store.Read()
	if err != nil {
		return err
	}
	// 内部端口也执行连通性预检查，避免 macOS 通配监听与具体地址同时绑定。
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("无法分配私有模型端口")
	}
	address := listener.Addr().String()
	listener.Close()
	if err = checkListenAvailable(c.ctx, address); err != nil {
		return err
	}
	u, _ := url.Parse("http://" + address)
	proxy := httputil.NewSingleHostReverseProxy(u)
	proxy.FlushInterval = -1
	proxy.ErrorLog = log.New(io.Discard, "", 0)
	proxy.Transport = &http.Transport{Proxy: nil}
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) {
		http.Error(w, "模型服务暂不可用", http.StatusServiceUnavailable)
	}
	runCtx, cancel := context.WithCancel(c.ctx)
	done = make(chan struct{})
	c.mu.Lock()
	c.mode = "starting"
	c.runCtx = runCtx
	c.cancel = cancel
	c.done = done
	c.proxy = proxy
	c.address = address
	c.mu.Unlock()
	go func() {
		defer close(done)
		_ = gateway.Run(runCtx, c.store, c.instanceID, address)
		proxy.Transport.(*http.Transport).CloseIdleConnections()
		if runCtx.Err() == nil {
			c.setMode("failed")
		}
	}()
	waitCtx, stop := context.WithTimeout(c.ctx, startTimeout)
	defer stop()
	ticker := time.NewTicker(30 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			c.setMode("failed")
			return fmt.Errorf("模型服务启动失败")
		case <-waitCtx.Done():
			cancel()
			c.setMode("failed")
			return fmt.Errorf("模型服务启动超时")
		case <-ticker.C:
			r, e := readRuntime(c.store)
			if e == nil && r.InstanceID == c.instanceID && r.Ready && r.Listen == s.Listen {
				// 用新实例的认证探测确认私有监听确实由 SDK 接管。
				private := r
				private.Listen = address
				if _, e = probe(waitCtx, private); e == nil {
					c.setMode("running")
					return nil
				}
			}
		}
	}
}
func (c *modelController) serveModel(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == gateway.ReadyPath {
		runtime, err := readRuntime(c.store)
		if err != nil || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CPA-Instance-Token")), []byte(runtime.ProbeToken)) != 1 || r.Method != "GET" {
			http.NotFound(w, r)
			return
		}
		if c.Mode() != "running" {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"ready":false,"maintenance":true,"instanceID":%q,"revision":%d}`, c.instanceID, runtime.ActiveRevision)
			return
		}
	}
	switch r.URL.Path {
	case gateway.ReadyPath, "/v1/models", "/v1/messages", "/v1/messages/count_tokens":
	default:
		http.NotFound(w, r)
		return
	}
	c.mu.RLock()
	mode, proxy, runCtx := c.mode, c.proxy, c.runCtx
	c.mu.RUnlock()
	if mode != "running" || proxy == nil {
		http.Error(w, "模型服务暂停中，请等待 OAuth 操作完成", 503)
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	stop := context.AfterFunc(runCtx, cancel)
	defer stop()
	proxy.ServeHTTP(w, r.WithContext(ctx))
}

func serveManaged(ctx context.Context, st *store.Store, id string) error {
	state, err := st.Read()
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", state.Listen)
	if err != nil {
		return err
	}
	defer listener.Close()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	control := &modelController{ctx: ctx, store: st, instanceID: id, mode: "paused"}
	handler, err := webui.New(st, control.active, control, ctx)
	if err != nil {
		return err
	}
	defer handler.Close()
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if webui.Handles(r.URL.Path) {
			handler.ServeHTTP(w, r)
			return
		}
		control.serveModel(w, r)
	})}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	defer func() { cancel(); _ = control.Pause(context.Background()); _ = server.Close() }()
	if err = control.Resume(ctx); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err = <-done:
		return err
	}
}
