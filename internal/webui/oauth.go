package webui

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/shichao-wang/cpa/internal/codexoauth"
	"github.com/shichao-wang/cpa/internal/config"
	"github.com/shichao-wang/cpa/internal/connection"
	"github.com/shichao-wang/cpa/internal/store"
)

// GatewayControl 将管理页面与模型服务生命周期分离，暂停后必须释放运行锁。
type GatewayControl interface {
	Pause(context.Context) error
	Resume(context.Context) error
	Mode() string
}

type OAuthStatus struct {
	ID              string         `json:"id"`
	Name            string         `json:"name"`
	Phase           string         `json:"phase"`
	Code            string         `json:"code,omitempty"`
	VerificationURL string         `json:"verificationURL,omitempty"`
	ExpiresAt       time.Time      `json:"expiresAt"`
	Message         string         `json:"message,omitempty"`
	PausesModels    bool           `json:"pausesModels"`
	Draft           bool           `json:"draft"`
	Models          []config.Model `json:"models,omitempty"`
	ModelError      string         `json:"modelError,omitempty"`
}

func (s OAuthStatus) pending() bool {
	switch s.Phase {
	case "starting", "waiting", "discovering", "creating", "resuming":
		return true
	}
	return false
}

type oauthDraft struct {
	credential config.OAuthCredential
	models     []config.Model
}

type oauthManager struct {
	draft      *oauthDraft
	expiry     *time.Timer
	loginDraft func(context.Context, func(string)) (config.OAuthCredential, error)
	ctx        context.Context
	store      *store.Store
	control    GatewayControl
	mu         sync.Mutex
	status     *OAuthStatus
	cancel     context.CancelFunc
	done       chan struct{}
	login      func(context.Context, string, string, func(string)) error
	discover   func(context.Context, config.OAuthCredential) ([]config.Model, error)
}

func newOAuthManager(ctx context.Context, st *store.Store, control GatewayControl) *oauthManager {
	return &oauthManager{ctx: ctx, store: st, control: control, login: codexoauth.LoginProcess, loginDraft: codexoauth.LoginDraftProcess, discover: codexoauth.DiscoverModels}
}
func (m *oauthManager) snapshot() *OAuthStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.status == nil {
		return nil
	}
	result := *m.status
	result.Models = append([]config.Model(nil), result.Models...)
	return &result
}
func (m *oauthManager) update(fn func(*OAuthStatus)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	fn(m.status)
}
func (m *oauthManager) begin(name string, action string) (*OAuthStatus, error) {
	if err := config.ValidateName(name); err != nil {
		return nil, fmt.Errorf("连接名称无效")
	}
	s, err := m.store.Read()
	if err != nil {
		return nil, fmt.Errorf("无法读取连接配置")
	}
	c, exists := s.Connections[name]
	if action == "authorize" && exists {
		return nil, fmt.Errorf("连接名称已存在，请使用其他名称")
	}
	if exists && c.AuthType != config.AuthCodexOAuth {
		return nil, fmt.Errorf("目标不是 Codex OAuth 连接")
	}
	if !exists {
		if action != "authorize" {
			return nil, fmt.Errorf("连接不存在")
		}
	}

	if action == "delete" && len(s.References(name, "")) > 0 {
		return nil, fmt.Errorf("连接仍被 profile 引用，请先调整模型映射")
	}
	id, err := config.RandomID()
	if err != nil {
		return nil, fmt.Errorf("无法创建授权会话")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.status != nil && (m.status.pending() || m.draft != nil) {
		return nil, fmt.Errorf("已有 OAuth 操作正在进行")
	}
	ctx, cancel := context.WithTimeout(m.ctx, 15*time.Minute)
	m.status = &OAuthStatus{ID: id, Name: name, Phase: "starting", Draft: !exists, PausesModels: action == "logout" || action == "delete", ExpiresAt: time.Now().Add(15 * time.Minute)}
	m.cancel = cancel
	m.done = make(chan struct{})
	result := *m.status
	go m.run(ctx, cancel, name, !exists, action, m.done)
	return &result, nil
}
func (m *oauthManager) run(ctx context.Context, cancel context.CancelFunc, name string, create bool, action string, done chan struct{}) {
	defer close(done)
	defer cancel()
	oldRef := ""
	if s, err := m.store.Read(); err == nil {
		oldRef = s.Connections[name].CredentialRef
	}
	stopRequired := action == "logout" || action == "delete"
	err := ctx.Err()
	if stopRequired {
		err = m.control.Pause(ctx)
	}
	if err == nil && ctx.Err() != nil {
		err = ctx.Err()
	}
	if err == nil && create {
		err = m.authorizeDraft(ctx)
	}
	if err == nil && !create {
		switch action {
		case "login":
			err = m.login(ctx, m.store.Dir, name, func(code string) {
				if ctx.Err() != nil {
					return
				}
				m.update(func(s *OAuthStatus) {
					s.Phase = "waiting"
					s.Code = code
					s.VerificationURL = codexoauth.VerificationURL
				})
			})
			// 取消可能与原子提交同时到达，凭证已成功提交时必须如实报告成功。
			credentialSaved := false
			if state, readErr := m.store.Read(); readErr == nil {
				c := state.Connections[name]
				if c.CredentialRef != "" && c.CredentialRef != oldRef {
					_, keyErr := state.Credential(c)
					credentialSaved = keyErr == nil
				}
			}
			if credentialSaved {
				err = nil
				m.update(func(s *OAuthStatus) { s.Phase = "discovering"; s.Code = ""; s.VerificationURL = "" })
				found, discoverErr := m.syncModels(ctx, name)
				m.update(func(s *OAuthStatus) {
					s.Models = found
					if discoverErr != nil {
						s.ModelError = "授权已保存，但模型目录获取失败；请在编辑连接中重新获取模型。"
					}
				})
			} else if err == nil {
				err = fmt.Errorf("授权未保存有效凭证")
			}

		case "logout":
			err = connection.LogoutOAuth(m.store, name)
		case "delete":
			err = connection.Remove(m.store, name)
		}
	}
	m.update(func(s *OAuthStatus) {
		if stopRequired {
			s.Phase = "resuming"
		}
		s.Code = ""
		s.VerificationURL = ""
	})
	var resumeErr error
	if stopRequired && m.ctx.Err() == nil {
		resumeErr = m.control.Resume(m.ctx)
	}
	m.update(func(s *OAuthStatus) {
		if err == nil {
			s.Phase = "succeeded"
			s.Message = "OAuth 操作已完成，模型服务已恢复。"
			if !stopRequired {
				s.Message = "OAuth 登录已完成，模型服务持续运行。"
			}
			if create {
				s.Phase = "selecting"
				s.Message = "登录成功，请选择模型后创建 connection。模型服务持续运行。"
			}
		} else if ctx.Err() != nil {
			s.Phase = "cancelled"
			s.Message = "授权已取消或超时，模型服务已恢复。"
			if !stopRequired {
				s.Message = "授权已取消或超时，原有模型服务持续运行。"
			}
		} else {
			s.Phase = "failed"
			s.Message = "OAuth 操作失败，请检查网络或连接配置后重试；模型服务已恢复。"
			if !stopRequired {
				s.Message = "OAuth 登录失败，请检查网络或连接配置后重试；原有模型服务持续运行。"
			}
		}
		if resumeErr != nil {
			m.draft = nil
			s.Phase = "failed"
			s.Message = "OAuth 操作已结束，但模型服务未能恢复，请点击恢复模型服务重试。"
		}
	})
	if create && err == nil && resumeErr == nil && m.ctx.Err() == nil {
		m.mu.Lock()
		if m.status.Phase == "selecting" && m.draft != nil {
			id := m.status.ID
			m.expiry = time.AfterFunc(time.Until(m.status.ExpiresAt), func() { m.expireDraft(id) })
		}
		m.mu.Unlock()
	}
}
func (m *oauthManager) stop(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.status == nil || m.status.ID != id {
		return fmt.Errorf("授权会话不存在")
	}
	if m.draft != nil && !m.status.pending() {
		m.draft = nil
		if m.expiry != nil {
			m.expiry.Stop()
		}
		m.status.Phase = "cancelled"
		m.status.Models = nil
		m.status.Message = "已放弃创建，未保存 connection 或凭证。"
	}
	if m.status.pending() && m.status.Phase != "resuming" {
		m.cancel()
	}
	return nil
}

// syncModels 在线读取账号目录；联网不持状态锁，保存时固定连接及凭证代次。
func (m *oauthManager) syncModels(ctx context.Context, name string) ([]config.Model, error) {
	state, err := m.store.Read()
	if err != nil {
		return nil, err
	}
	c := state.Connections[name]
	_, err = state.Credential(c)
	if err != nil {
		return nil, err
	}
	credential := state.OAuthCredentials[c.CredentialRef]
	found, err := m.discover(ctx, credential)
	if err != nil {
		return nil, err
	}
	if err = config.ValidateModels(found); err != nil {
		return nil, err
	}
	// 已保存但目录暂未返回的模型继续保留，避免破坏已有 profile 引用。
	seen := map[string]bool{}
	for _, model := range found {
		seen[model.ID] = true
	}
	for _, model := range c.Models {
		if !seen[model.ID] {
			found = append(found, model)
		}
	}
	err = m.store.UpdateContext(ctx, func(s *config.State) error {
		current, ok := s.Connections[name]
		if !ok || current.ID != c.ID || current.AuthType != config.AuthCodexOAuth || current.CredentialRef != c.CredentialRef || s.OAuthCredentials[c.CredentialRef].Generation != credential.Generation || !slices.Equal(current.Models, c.Models) {
			return fmt.Errorf("OAuth 连接或凭证已改变，请重新获取模型")
		}
		current.Models = found
		s.Connections[name] = current
		return nil
	})
	if err != nil {
		return nil, err
	}
	return found, nil
}
func (m *oauthManager) close() {
	m.mu.Lock()
	m.draft = nil
	if m.expiry != nil {
		m.expiry.Stop()
	}
	if m.cancel != nil {
		m.cancel()
	}
	done := m.done
	m.mu.Unlock()
	if done != nil {
		<-done
	}
}
func (h *Handler) oauthLogin(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &in) {
		return
	}
	h.startOAuth(w, r, in.Name, "login")
}
func (h *Handler) oauthAuthorize(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &in) {
		return
	}
	h.startOAuth(w, r, in.Name, "authorize")
}
func (h *Handler) startOAuth(w http.ResponseWriter, r *http.Request, name string, action string) {
	if h.oauth == nil {
		fail(w, 409, "当前服务不支持网页 OAuth，请重启新版 cpa")
		return
	}
	status, err := h.oauth.begin(name, action)
	if err != nil {
		fail(w, 409, err.Error())
		return
	}
	respond(w, 202, status)
}
func (h *Handler) oauthAction(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Action string `json:"action"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Action != "logout" && in.Action != "delete" {
		fail(w, 400, "不支持的 OAuth 操作")
		return
	}
	h.startOAuth(w, r, r.PathValue("name"), in.Action)
}
func (h *Handler) oauthStatus(w http.ResponseWriter, r *http.Request) {
	if h.oauth == nil {
		respond(w, 200, nil)
		return
	}
	respond(w, 200, h.oauth.snapshot())
}
func (h *Handler) oauthCancel(w http.ResponseWriter, r *http.Request) {
	if h.oauth == nil {
		fail(w, 409, "没有可取消的授权会话")
		return
	}
	if err := h.oauth.stop(r.PathValue("id")); err != nil {
		fail(w, 409, err.Error())
		return
	}
	respond(w, 200, map[string]bool{"cancelled": true})
}
func (h *Handler) resumeGateway(w http.ResponseWriter, r *http.Request) {
	if h.oauth == nil {
		fail(w, 409, "当前管理服务不支持恢复模型服务")
		return
	}
	if s := h.oauth.snapshot(); s != nil && s.pending() {
		fail(w, 409, "请先完成或取消当前 OAuth 操作")
		return
	}
	if err := h.oauth.control.Resume(r.Context()); err != nil {
		fail(w, 503, "模型服务恢复失败，请检查配置后重试")
		return
	}
	respond(w, 200, map[string]bool{"resumed": true})
}

// authorizeDraft 只暂存授权和目录，登录锁与模型服务运行锁独立。
func (m *oauthManager) authorizeDraft(ctx context.Context) error {
	lock, err := m.store.AcquireOAuthLock()
	if err != nil {
		return err
	}
	defer lock.Close()
	credential, err := m.loginDraft(ctx, func(code string) {
		if ctx.Err() == nil {
			m.update(func(s *OAuthStatus) {
				s.Phase = "waiting"
				s.Code = code
				s.VerificationURL = codexoauth.VerificationURL
			})
		}
	})
	if err != nil {
		return err
	}
	credential.Generation = 1
	if err = config.ValidateOAuthCredential(credential); err != nil {
		return fmt.Errorf("OAuth 授权结果无效")
	}
	m.update(func(s *OAuthStatus) { s.Phase = "discovering"; s.Code = ""; s.VerificationURL = "" })
	models, err := m.discover(ctx, credential)
	if err != nil {
		return err
	}
	if err = config.ValidateModels(models); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err = ctx.Err(); err != nil {
		return err
	}
	m.draft = &oauthDraft{credential: credential, models: append([]config.Model(nil), models...)}
	m.status.Models = append([]config.Model(nil), models...)
	if credential.ExpiresAt.Before(m.status.ExpiresAt) {
		m.status.ExpiresAt = credential.ExpiresAt
	}
	return nil
}
func (m *oauthManager) expireDraft(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.status != nil && m.status.ID == id && m.status.Phase == "selecting" {
		m.draft = nil
		m.status.Models = nil
		m.status.Phase = "cancelled"
		m.status.Message = "授权草稿已过期，未创建 connection，请重新登录。"
	}
}
func (m *oauthManager) createConnection(id string, selected []config.Model) (*OAuthStatus, error) {
	if err := config.ValidateModels(selected); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.status == nil || m.status.ID != id || m.status.Phase != "selecting" || m.draft == nil {
		return nil, fmt.Errorf("没有可创建的授权草稿，请重新登录")
	}
	if time.Now().After(m.status.ExpiresAt) {
		m.draft = nil
		m.status.Phase = "cancelled"
		return nil, fmt.Errorf("授权草稿已过期，请重新登录")
	}
	catalog := map[string]config.Model{}
	for _, model := range m.draft.models {
		catalog[model.ID] = model
	}
	models := make([]config.Model, 0, len(selected))
	for _, model := range selected {
		known, ok := catalog[model.ID]
		if !ok {
			return nil, fmt.Errorf("只能选择本次登录获取的模型")
		}
		models = append(models, known)
	}
	draft := m.draft
	m.draft = nil
	if m.expiry != nil {
		m.expiry.Stop()
	}
	m.status.Phase = "creating"
	ctx, cancel := context.WithTimeout(m.ctx, 2*time.Minute)
	m.cancel = cancel
	m.done = make(chan struct{})
	done := m.done
	result := *m.status
	go func() {
		defer close(done)
		defer cancel()
		err := connection.CreateOAuth(ctx, m.store, result.Name, models, draft.credential)
		draft.credential = config.OAuthCredential{}
		m.update(func(s *OAuthStatus) {
			s.Models = nil
			s.Draft = false
			if err != nil {
				s.Phase = "failed"
				s.Message = "创建失败，未保存 connection，请检查名称或配置后重新登录。"
			} else {
				s.Phase = "succeeded"
				s.Message = "connection 已保存，所选模型将自动热更新，原有请求保持运行。"
			}
		})
	}()
	return &result, nil
}
func (h *Handler) oauthCreateConnection(w http.ResponseWriter, r *http.Request) {
	if h.oauth == nil {
		fail(w, 409, "当前服务不支持网页 OAuth")
		return
	}
	var in struct {
		Models []config.Model `json:"models"`
	}
	if !decode(w, r, &in) {
		return
	}
	status, err := h.oauth.createConnection(r.PathValue("id"), in.Models)
	if err != nil {
		fail(w, 409, err.Error())
		return
	}
	respond(w, 202, status)
}
