package webui

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/shichao-wang/cpa/internal/agent/claudecode"
	"github.com/shichao-wang/cpa/internal/codexoauth"
	"github.com/shichao-wang/cpa/internal/config"
	"github.com/shichao-wang/cpa/internal/connection"
	"github.com/shichao-wang/cpa/internal/profile"
	"github.com/shichao-wang/cpa/internal/store"
)

//go:embed assets/*
var assets embed.FS

type Handler struct {
	store     *store.Store
	tokenHash [32]byte
	active    func() (uint64, bool)
	mux       *http.ServeMux
	oauth     *oauthManager
}

// New 使用公开状态操作，active 返回模型网关实际生效的修订及就绪状态。
func New(st *store.Store, active func() (uint64, bool), control GatewayControl, ctx context.Context) (*Handler, error) {
	token, err := Token(st)
	if err != nil {
		return nil, err
	}
	h := &Handler{store: st, tokenHash: sha256.Sum256([]byte(token)), active: active, mux: http.NewServeMux()}
	if control != nil {
		h.oauth = newOAuthManager(ctx, st, control)
	}
	h.mux.HandleFunc("POST /admin/api/oauth/login", h.oauthLogin)
	h.mux.HandleFunc("POST /admin/api/oauth/authorize", h.oauthAuthorize)
	h.mux.HandleFunc("GET /admin/api/oauth/session", h.oauthStatus)
	h.mux.HandleFunc("DELETE /admin/api/oauth/session/{id}", h.oauthCancel)
	h.mux.HandleFunc("POST /admin/api/oauth/session/{id}/connection", h.oauthCreateConnection)
	h.mux.HandleFunc("POST /admin/api/oauth/connections/{name}", h.oauthAction)
	h.mux.HandleFunc("POST /admin/api/gateway/resume", h.resumeGateway)
	h.mux.HandleFunc("GET /admin/api/state", h.state)
	h.mux.HandleFunc("POST /admin/api/connections/test", h.testConnection)
	h.mux.HandleFunc("POST /admin/api/connections", h.createConnection)
	h.mux.HandleFunc("PATCH /admin/api/connections/{name}", h.updateConnection)
	h.mux.HandleFunc("DELETE /admin/api/connections/{name}", h.deleteConnection)
	h.mux.HandleFunc("POST /admin/api/profiles", h.createProfile)
	h.mux.HandleFunc("PATCH /admin/api/profiles/{name}", h.updateProfile)
	h.mux.HandleFunc("DELETE /admin/api/profiles/{name}", h.deleteProfile)
	return h, nil
}

// Handles 仅接管明确的页面和管理命名空间，不放行其他 SDK 路由。
func Handles(path string) bool {
	return path == "/ui" || strings.HasPrefix(path, "/ui/") || path == "/admin/api" || strings.HasPrefix(path, "/admin/api/")
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// SDK 的通用 CORS 策略不适用于管理接口。
	for name := range w.Header() {
		if strings.HasPrefix(strings.ToLower(name), "access-control-") {
			w.Header().Del(name)
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	const policy = "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"
	w.Header().Set("Content-Security-Policy", policy)
	// 校验浏览器实际访问的 Host，不信任转发头；允许 SSH 隧道使用不同本地端口。
	host := r.Host
	if hostname, _, err := net.SplitHostPort(host); err == nil {
		host = hostname
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		fail(w, http.StatusForbidden, "管理界面仅允许通过 loopback 或 SSH 隧道访问")
		return
	}
	scheme := "http"
	// SDK 的明文连接包装器也提供空 TLS 状态，只有握手完成才表示 HTTPS。
	if r.TLS != nil && r.TLS.HandshakeComplete {
		scheme = "https"
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || u.Scheme != scheme || u.Host != r.Host || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
			fail(w, http.StatusForbidden, "拒绝跨来源管理请求")
			return
		}
	}
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		fail(w, http.StatusForbidden, "拒绝跨来源管理请求")
		return
	}
	if r.URL.RawPath != "" || r.URL.RawQuery != "" {
		http.NotFound(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/ui") {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		path := r.URL.Path
		if path == "/ui" {
			http.Redirect(w, r, "/ui/", http.StatusTemporaryRedirect)
			return
		}
		file, contentType := "", ""
		switch path {
		case "/ui/":
			file, contentType = "index.html", "text/html; charset=utf-8"
		case "/ui/app.js":
			file, contentType = "app.js", "text/javascript; charset=utf-8"
		case "/ui/style.css":
			file, contentType = "style.css", "text/css; charset=utf-8"
		default:
			http.NotFound(w, r)
			return
		}
		data, err := assets.ReadFile("assets/" + file)
		if err != nil {
			fail(w, 500, "页面资源不可用")
			return
		}
		if file == "index.html" {
			// 弹层滚动锁需要动态样式，仅允许本次页面请求的随机 nonce。
			var random [16]byte
			if _, err := rand.Read(random[:]); err != nil {
				fail(w, 500, "无法生成页面样式凭证")
				return
			}
			nonce := base64.RawStdEncoding.EncodeToString(random[:])
			w.Header().Set("Content-Security-Policy", strings.Replace(policy, "style-src 'self'", "style-src 'self' 'nonce-"+nonce+"'", 1))
			data = bytes.Replace(data, []byte("</head>"), []byte(`<meta name="cpa-style-nonce" content="`+nonce+`"></head>`), 1)
		}
		w.Header().Set("Content-Type", contentType)
		// Gin 的未注册路径预设为 404，页面资源需要显式提交成功状态。
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			_, _ = w.Write(data)
		}
		return
	}
	auth := r.Header.Get("Authorization")
	provided := sha256.Sum256([]byte(strings.TrimPrefix(auth, "Bearer ")))
	if len(r.Header.Values("Authorization")) != 1 || !strings.HasPrefix(auth, "Bearer ") || subtle.ConstantTimeCompare(provided[:], h.tokenHash[:]) != 1 {
		fail(w, http.StatusUnauthorized, "管理令牌无效，请在远端执行 cpa server admin-token 获取")
		return
	}
	h.mux.ServeHTTP(w, r)
}

func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		fail(w, 415, "请求必须使用 application/json")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		fail(w, 400, "JSON 字段无效或请求超过 1 MiB")
		return false
	}
	if dec.Decode(new(any)) != io.EOF {
		fail(w, 400, "请求只能包含一个 JSON 对象")
		return false
	}
	return true
}
func respond(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func fail(w http.ResponseWriter, status int, message string) {
	respond(w, status, map[string]string{"error": message})
}

type connectionView struct {
	Name                 string         `json:"name"`
	AuthType             string         `json:"authType"`
	Protocol             string         `json:"protocol"`
	BaseURL              string         `json:"baseURL"`
	Models               []config.Model `json:"models"`
	CredentialConfigured bool           `json:"credentialConfigured"`
}
type profileView struct {
	Name    string                    `json:"name"`
	Agent   string                    `json:"agent"`
	Models  map[string]config.Binding `json:"models"`
	Managed bool                      `json:"managed"`
}

func (h *Handler) state(w http.ResponseWriter, r *http.Request) {
	s, err := h.store.Read()
	if err != nil {
		fail(w, 500, "无法读取配置")
		return
	}
	connections := map[string]connectionView{}
	for name, c := range s.Connections {
		_, err := s.Credential(c)
		connections[name] = connectionView{name, c.AuthType, c.Protocol, c.BaseURL, append([]config.Model{}, c.Models...), err == nil}
	}
	profiles := map[string]profileView{}
	for name, p := range s.Profiles {
		profiles[name] = profileView{name, p.Agent, p.Models, claudecode.IsManagedProfile(h.store, name)}
	}
	revision, ready := h.active()
	mode := "running"
	if h.oauth != nil {
		mode = h.oauth.control.Mode()
	}
	respond(w, 200, map[string]any{"oauthAvailable": h.oauth != nil, "gatewayMode": mode, "revision": s.Revision, "activeRevision": revision, "ready": ready, "listen": s.Listen, "connections": connections, "profiles": profiles})
}

func (h *Handler) saved(w http.ResponseWriter, r *http.Request, err error) {
	h.savedResult(w, r, err, saveResult{})
}

type saveResult struct {
	Saved      bool   `json:"saved"`
	Active     bool   `json:"active"`
	Revision   uint64 `json:"revision"`
	ProfileKey string `json:"profileKey,omitempty"`
}

func (h *Handler) savedResult(w http.ResponseWriter, r *http.Request, err error, result saveResult) {
	if err != nil {
		// 不把底层错误或用户输入写入响应，避免凭证随错误内容泄漏。
		fail(w, 409, "保存失败：请检查字段、名称是否重复、模型引用及受管 profile；OAuth 操作需停止网关后使用 CLI")
		return
	}
	s, err := h.store.Read()
	if err != nil {
		fail(w, 500, "配置已保存，但无法回读状态，请刷新确认")
		return
	}
	result.Saved = true
	result.Revision = s.Revision
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		revision, ready := h.active()
		if ready && revision >= s.Revision {
			result.Active = true
			respond(w, 200, result)
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-timer.C:
			respond(w, 202, result)
			return
		case <-ticker.C:
		}
	}
}

type connectionInput struct {
	Name     string         `json:"name"`
	Protocol string         `json:"protocol"`
	BaseURL  string         `json:"baseURL"`
	Models   []config.Model `json:"models"`
	APIKey   string         `json:"apiKey"`
	AuthType string         `json:"authType"`
}

// testConnection 只读取凭证快照，联网期间不持状态锁，也不保存输入或目录结果。
func (h *Handler) testConnection(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Connection string `json:"connection"`
		Protocol   string `json:"protocol"`
		BaseURL    string `json:"baseURL"`
		APIKey     string `json:"apiKey"`
	}
	if !decode(w, r, &in) {
		return
	}
	c := config.Connection{Protocol: in.Protocol, BaseURL: in.BaseURL, AuthType: config.AuthAPIKey}
	if in.Connection != "" {
		s, err := h.store.Read()
		if err != nil {
			fail(w, 500, "无法读取连接配置")
			return
		}
		stored, ok := s.Connections[in.Connection]
		if !ok {
			fail(w, 404, "连接不存在")
			return
		}
		if stored.AuthType != config.AuthAPIKey {
			if in.APIKey != "" || (in.BaseURL != "" && in.BaseURL != stored.BaseURL) || (in.Protocol != "" && in.Protocol != stored.Protocol) {
				fail(w, 400, "OAuth 模型发现不接受 API key、自定义地址或协议")
				return
			}
			_, err := s.Credential(stored)
			if err != nil {
				fail(w, 409, "OAuth 连接尚未登录，请先登录")
				return
			}
			credential := s.OAuthCredentials[stored.CredentialRef]
			discover := codexoauth.DiscoverModels
			if h.oauth != nil {
				discover = h.oauth.discover
			}
			models, err := discover(r.Context(), credential)
			if err != nil {
				fail(w, 400, "OAuth 模型目录获取失败，请重试或重新登录")
				return
			}
			respond(w, 200, map[string]any{"models": models})
			return
		}
		if in.Protocol != "" && in.Protocol != stored.Protocol {
			fail(w, 400, "已有连接不能切换协议")
			return
		}
		c.Protocol = stored.Protocol
		if c.BaseURL == "" {
			c.BaseURL = stored.BaseURL
		}
		if in.APIKey == "" {
			// 已保存的凭证只能发送到原地址，修改地址后须显式提供该地址的 key。
			if c.BaseURL != stored.BaseURL {
				fail(w, 400, "测试新地址时请填写对应的 API key")
				return
			}
			in.APIKey, err = s.Key(stored)
			if err != nil {
				fail(w, 409, "连接缺少有效的 API key")
				return
			}
		}
	}
	models, err := connection.DiscoverModels(r.Context(), c, in.APIKey)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	respond(w, 200, map[string]any{"models": models})
}

func (h *Handler) createConnection(w http.ResponseWriter, r *http.Request) {
	var in connectionInput
	if !decode(w, r, &in) {
		return
	}
	if in.AuthType != "" && in.AuthType != config.AuthAPIKey {
		fail(w, 409, "OAuth 创建需停止网关后使用 CLI")
		return
	}
	c := config.Connection{Name: in.Name, Protocol: in.Protocol, BaseURL: in.BaseURL, Models: in.Models, AuthType: config.AuthAPIKey}
	h.saved(w, r, connection.Create(r.Context(), h.store, c, in.APIKey))
}
func (h *Handler) updateConnection(w http.ResponseWriter, r *http.Request) {
	var in struct {
		BaseURL *string         `json:"baseURL"`
		Models  *[]config.Model `json:"models"`
		APIKey  *string         `json:"apiKey"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.BaseURL == nil && in.Models == nil && in.APIKey == nil {
		fail(w, 400, "请指定更新字段")
		return
	}
	h.saved(w, r, connection.Patch(h.store, r.PathValue("name"), in.BaseURL, in.Models, in.APIKey, nil))
}
func (h *Handler) deleteConnection(w http.ResponseWriter, r *http.Request) {
	h.saved(w, r, connection.Remove(h.store, r.PathValue("name")))
}

type profileInput struct {
	Name   *string                   `json:"name"`
	Agent  *string                   `json:"agent"`
	Models map[string]config.Binding `json:"models"`
}

func (in profileInput) file() profile.File {
	return profile.File{Name: in.Name, Agent: in.Agent, Models: in.Models}
}
func (h *Handler) createProfile(w http.ResponseWriter, r *http.Request) {
	var in profileInput
	if !decode(w, r, &in) {
		return
	}
	key, err := profile.Create(h.store, in.file())
	h.savedResult(w, r, err, saveResult{ProfileKey: key})
}
func (h *Handler) updateProfile(w http.ResponseWriter, r *http.Request) {
	var in profileInput
	if !decode(w, r, &in) {
		return
	}
	if in.Name == nil && in.Agent == nil && in.Models == nil {
		fail(w, 400, "请指定更新字段")
		return
	}
	h.saved(w, r, profile.Update(h.store, r.PathValue("name"), in.file()))
}
func (h *Handler) deleteProfile(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	h.saved(w, r, profile.Remove(h.store, name, func(string) (bool, error) {
		return claudecode.IsManagedProfile(h.store, name), nil
	}))
}

var _ http.Handler = (*Handler)(nil)

// Close 取消正在等待的设备授权，停止登录进程并释放运行锁。
func (h *Handler) Close() {
	if h.oauth != nil {
		h.oauth.close()
	}
}
