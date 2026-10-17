import { z } from "zod";
import {
  useEffect,
  useRef,
  useState,
  type FormEvent,
  type ReactNode,
} from "react";
import {
  Activity,
  ArrowRight,
  Check,
  Copy,
  KeyRound,
  LogOut,
  Plus,
  Plug,
  RefreshCw,
  Search,
  ShieldCheck,
  Trash2,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Badge } from "@/components/ui/badge";
import {
  AlertDialog,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { OAuthDialog } from "./oauth-dialog";
import { ConnectionEditor } from "./connection-editor";
import { ProfileEditor } from "./profile-editor";
import {
  protocols,
  oauthStatusSchema,
  oauthPending,
  type OAuthStatus,
  type Model,
  discoveredSchema,
  request,
  savedSchema,
  slots,
  sorted,
  stateSchema,
  Unauthorized,
  type Connection,
  type DiscoveryInput,
  type GatewayState,
} from "./api";

type Session = { token: string; state: GatewayState };
type Page =
  | { kind: "connections" }
  | { kind: "profiles" }
  | { kind: "profile-editor"; name: string | null };
type Feedback = { text: string; error?: boolean };
type DeleteTarget = { kind: "connections" | "profiles"; name: string };
function Brand() {
  return (
    <div className="brand">
      <span className="brand-mark">
        <Activity aria-hidden="true" />
      </span>
      <span>cpa</span>
    </div>
  );
}
function FeedbackMessage({ value }: { value: Feedback | null }) {
  return (
    value && (
      <div
        className={"feedback" + (value.error ? " error" : "")}
        role={value.error ? "alert" : "status"}
      >
        {value.text}
      </div>
    )
  );
}
function Empty({
  icon,
  title,
  text,
  children,
}: {
  icon: ReactNode;
  title: string;
  text: string;
  children?: ReactNode;
}) {
  return (
    <div className="empty-state">
      {icon}
      <h2>{title}</h2>
      <p className="muted">{text}</p>
      {children}
    </div>
  );
}

function ConnectionModels({
  connection,
  onClose,
}: {
  connection: Connection;
  onClose: () => void;
}) {
  const [query, setQuery] = useState("");
  const search = query.trim().toLowerCase();
  const models = connection.models.filter((model) =>
    (model.id + " " + (model.name || "")).toLowerCase().includes(search),
  );
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
    >
      <DialogContent className="connection-model-dialog">
        <DialogHeader>
          <DialogTitle>{connection.name} · 可用模型</DialogTitle>
          <DialogDescription>
            已启用 {connection.models.length} 个模型。可按名称或模型 ID
            搜索；如需调整选择，请编辑连接。
          </DialogDescription>
        </DialogHeader>
        <Input
          aria-label="搜索可用模型"
          placeholder="搜索模型名称或 ID…"
          value={query}
          onChange={(event) => setQuery(event.target.value)}
        />
        <p className="hint" role="status">
          {search
            ? `找到 ${models.length} / ${connection.models.length} 个模型`
            : `共 ${models.length} 个模型`}
        </p>
        <ul className="connection-model-list" aria-label="可用模型列表">
          {models.map((model) => (
            <li key={model.id}>
              <strong>{model.name || model.id}</strong>
              {model.name && model.name !== model.id && (
                <code className="hint">{model.id}</code>
              )}
            </li>
          ))}
        </ul>
        {!models.length && (
          <p className="muted">没有匹配的模型，试试其他关键词。</p>
        )}
      </DialogContent>
    </Dialog>
  );
}

export default function App() {
  const localAdmin =
    document
      .querySelector('meta[name="cpa-local-admin"]')
      ?.getAttribute("content") === "true";
  const [session, setSession] = useState<Session | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const loginRequest = useRef<AbortController | null>(null);
  useEffect(() => {
    if (!localAdmin) return;
    const controller = new AbortController();
    loginRequest.current = controller;
    setBusy(true);
    request("", controller.signal, "state", stateSchema)
      .then((state) => {
        if (!controller.signal.aborted) setSession({ token: "", state });
      })
      .catch((err) => {
        if (!controller.signal.aborted)
          setError(err instanceof Error ? err.message : "连接失败。");
      })
      .finally(() => {
        if (!controller.signal.aborted) setBusy(false);
      });
    return () => controller.abort();
  }, [localAdmin]);
  useEffect(() => () => loginRequest.current?.abort(), []);
  async function login(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = event.currentTarget;
    const tokenInput = form.elements.namedItem("token");
    const token = String(new FormData(form).get("token") || "").trim();
    if (tokenInput instanceof HTMLInputElement) tokenInput.value = "";
    const controller = new AbortController();
    loginRequest.current?.abort();
    loginRequest.current = controller;
    setBusy(true);
    setError("");
    try {
      const state = await request(
        token,
        controller.signal,
        "state",
        stateSchema,
      );
      if (!controller.signal.aborted) setSession({ token, state });
    } catch (err) {
      if (!controller.signal.aborted)
        setError(err instanceof Error ? err.message : "连接失败。");
    } finally {
      if (!controller.signal.aborted) setBusy(false);
    }
  }
  if (session)
    return (
      <Workspace
        session={session}
        onLogout={(message = "") => {
          setSession(null);
          setError(message);
        }}
      />
    );
  return (
    <div className="login-page">
      <header className="login-brand">
        <Brand />
        <span className="muted">网关管理</span>
      </header>
      <main className="login-card">
        <span className="login-symbol">
          <ShieldCheck />
        </span>
        <p className="eyebrow">连接你的网关</p>
        <h1>让模型配置井然有序</h1>
        <p className="muted">
          管理当前服务器的上游连接和模型映射。通过 SSH
          隧道访问时，更改保存在远端。
        </p>
        {localAdmin ? (
          <div role="status">
            {error ? (
              <Button onClick={() => location.reload()}>重试连接</Button>
            ) : (
              "正在连接本地网关…"
            )}
          </div>
        ) : (
          <form onSubmit={login}>
            <div className="field">
              <Label htmlFor="admin-token">管理令牌</Label>
              <Input
                id="admin-token"
                name="token"
                type="password"
                autoComplete="off"
                required
                spellCheck={false}
                autoFocus
                placeholder="输入独立管理令牌"
                disabled={busy}
              />
            </div>
            <Button className="login-submit" disabled={busy}>
              {busy ? "正在连接…" : "连接网关"}
              <ArrowRight />
            </Button>
          </form>
        )}
        {error && (
          <p className="feedback error" role="alert">
            {error}
          </p>
        )}
        {!localAdmin && (
          <div className="login-help">
            <p>在服务器上获取令牌</p>
            <code>cpa server admin-token</code>
            <p>令牌仅保留在当前页面内存中，刷新后需重新输入。</p>
          </div>
        )}
      </main>
      <footer className="login-footer">
        <span className="status-dot" />
        {location.host}
      </footer>
    </div>
  );
}

function Workspace({
  session,
  onLogout,
}: {
  session: Session;
  onLogout: (message?: string) => void;
}) {
  const [state, setState] = useState(session.state);
  const [page, setPage] = useState<Page>({ kind: "connections" });
  const [query, setQuery] = useState("");
  const [protocol, setProtocol] = useState("all");
  const [viewingModels, setViewingModels] = useState<string | null>(null);
  const [editor, setEditor] = useState<{
    connection: Connection | null;
  } | null>(null);
  const [deleting, setDeleting] = useState<DeleteTarget | null>(null);
  const [oauthJob, setOAuthJob] = useState<OAuthStatus | null>(null);
  const [oauthVisible, setOAuthVisible] = useState(false);
  const [oauthConfirm, setOAuthConfirm] = useState<{
    name: string;
    action: "login" | "logout" | "delete";
  } | null>(null);
  const [feedback, setFeedback] = useState<Feedback | null>(null);
  const [createdKey, setCreatedKey] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);
  const [busy, setBusy] = useState(false);
  const controller = useRef(new AbortController());
  const operation = useRef(false);
  const keyField = useRef<HTMLInputElement>(null);
  useEffect(() => () => controller.current.abort(), []);
  const connections = sorted(state.connections);
  const profiles = sorted(state.profiles);
  const active = state.ready && state.activeRevision >= state.revision;
  const isConnections = page.kind === "connections";
  function handleError(err: unknown) {
    if (controller.current.signal.aborted) return;
    if (err instanceof Unauthorized) {
      controller.current.abort();
      onLogout(err.message);
    } else
      setFeedback({
        text: err instanceof Error ? err.message : "操作失败，请重试。",
        error: true,
      });
  }
  async function refresh() {
    if (operation.current) return;
    operation.current = true;
    setBusy(true);
    try {
      const next = await request(
        session.token,
        controller.current.signal,
        "state",
        stateSchema,
      );
      if (!controller.current.signal.aborted) {
        setState(next);
        setFeedback(null);
      }
    } catch (err) {
      handleError(err);
    } finally {
      operation.current = false;
      setBusy(false);
    }
  }
  async function save(path: string, method: string, body?: unknown) {
    if (operation.current) throw new Error("请等待当前操作完成。");
    operation.current = true;
    setBusy(true);
    setFeedback(null);
    try {
      const result = await request(
        session.token,
        controller.current.signal,
        path,
        savedSchema,
        method,
        body,
      );
      if (controller.current.signal.aborted) return;
      // 一次性 key 必须先展示，后续刷新失败也不能丢失。
      if (result.profileKey) {
        setCreatedKey(result.profileKey);
        setCopied(false);
      }
      setState((previous) => ({ ...previous, revision: result.revision }));
      try {
        const next = await request(
          session.token,
          controller.current.signal,
          "state",
          stateSchema,
        );
        if (!controller.current.signal.aborted) {
          setState(next);
          setFeedback({
            text:
              next.ready && next.activeRevision >= result.revision
                ? "配置已保存，并已在网关生效。"
                : "配置已保存，网关尚未确认生效。请稍后刷新状态。",
            error: !(next.ready && next.activeRevision >= result.revision),
          });
        }
      } catch (err) {
        if (err instanceof Unauthorized) handleError(err);
        else if (!controller.current.signal.aborted)
          setFeedback({
            text: "配置已保存，但刷新失败。请先保存页面上的新 key，再刷新状态确认。",
            error: true,
          });
      }
    } catch (err) {
      handleError(err);
      throw err;
    } finally {
      operation.current = false;
      setBusy(false);
    }
  }
  async function discover(input: DiscoveryInput) {
    try {
      const result = await request(
        session.token,
        controller.current.signal,
        "connections/test",
        discoveredSchema,
        "POST",
        input,
      );
      return result.models;
    } catch (err) {
      if (err instanceof Unauthorized) handleError(err);
      throw err;
    }
  }
  useEffect(() => {
    if (!state.oauthAvailable) return;
    let stopped = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    async function poll() {
      try {
        const next = await request(
          session.token,
          controller.current.signal,
          "oauth/session",
          oauthStatusSchema.nullable(),
        );
        if (stopped) return;
        if (oauthPending(next) || next?.phase === "selecting") {
          setOAuthJob(next);
          if (next?.phase === "selecting") {
            setEditor({ connection: null });
            setOAuthVisible(false);
          } else setOAuthVisible(true);
          timer = setTimeout(poll, 1000);
        } else if (next) {
          const fresh = await request(
            session.token,
            controller.current.signal,
            "state",
            stateSchema,
          );
          if (!stopped) {
            if (
              next.phase === "succeeded" &&
              fresh.connections[next.name] &&
              !state.connections[next.name]
            )
              setEditor(null);
            setState(fresh);
            setOAuthJob(next);
          }
        } else setOAuthJob(null);
      } catch (err) {
        if (!stopped) {
          handleError(err);
          if (!(err instanceof Unauthorized)) timer = setTimeout(poll, 1500);
        }
      }
    }
    void poll();
    return () => {
      stopped = true;
      clearTimeout(timer);
    };
  }, [state.oauthAvailable, oauthJob?.id]);
  async function startOAuth(
    name: string,
    action: "authorize" | "login" | "logout" | "delete" = "login",
  ) {
    try {
      const next = await request(
        session.token,
        controller.current.signal,
        action === "login" || action === "authorize"
          ? "oauth/" + action
          : "oauth/connections/" + encodeURIComponent(name),
        oauthStatusSchema,
        "POST",
        action === "login" || action === "authorize" ? { name } : { action },
      );
      setOAuthJob(next);
      setOAuthVisible(true);
      setFeedback(null);
    } catch (err) {
      handleError(err);
      throw err;
    }
  }
  async function createOAuthConnection(models: Model[]) {
    if (!oauthJob) return;
    const next = await request(
      session.token,
      controller.current.signal,
      "oauth/session/" + encodeURIComponent(oauthJob.id) + "/connection",
      oauthStatusSchema,
      "POST",
      { models },
    );
    setOAuthJob(next);
    setOAuthVisible(true);
  }
  async function cancelOAuth() {
    if (!oauthJob) return;
    await request(
      session.token,
      controller.current.signal,
      "oauth/session/" + encodeURIComponent(oauthJob.id),
      z.object({ cancelled: z.literal(true) }),
      "DELETE",
    );
  }
  async function resumeModels() {
    setBusy(true);
    try {
      await request(
        session.token,
        controller.current.signal,
        "gateway/resume",
        z.object({ resumed: z.literal(true) }),
        "POST",
      );
      await refresh();
    } catch (err) {
      handleError(err);
    } finally {
      setBusy(false);
    }
  }
  async function confirmOAuth() {
    if (!oauthConfirm) return;
    setBusy(true);
    try {
      await startOAuth(oauthConfirm.name, oauthConfirm.action);
      setOAuthConfirm(null);
    } catch {
    } finally {
      setBusy(false);
    }
  }
  function navigate(kind: "connections" | "profiles") {
    if (page.kind === "profile-editor") return;
    setPage({ kind });
    setQuery("");
    setFeedback(null);
  }
  async function remove() {
    if (!deleting) return;
    try {
      await save(
        deleting.kind + "/" + encodeURIComponent(deleting.name),
        "DELETE",
      );
      setDeleting(null);
    } catch {
      /* 保留确认框与错误，供用户修正后重试。 */
    }
  }
  async function copyKey() {
    if (!createdKey) return;
    try {
      await navigator.clipboard.writeText(createdKey);
      setCopied(true);
    } catch {
      keyField.current?.focus();
      keyField.current?.select();
      setCopied(false);
    }
  }
  const filteredConnections = connections.filter(
    (c) =>
      c.name.toLowerCase().includes(query.toLowerCase()) &&
      (protocol === "all" || c.protocol === protocol),
  );
  const filteredProfiles = profiles.filter((p) =>
    p.name.toLowerCase().includes(query.toLowerCase()),
  );
  const selectedProfile =
    page.kind === "profile-editor" && page.name
      ? state.profiles[page.name]
      : null;
  return (
    <div className="app-shell">
      <aside className="sidebar">
        <Brand />
        <div className="sidebar-section">
          <p className="eyebrow">配置管理</p>
          <nav aria-label="主导航">
            <Button
              variant="ghost"
              className={isConnections ? "nav-item selected" : "nav-item"}
              onClick={() => navigate("connections")}
              disabled={busy || page.kind === "profile-editor"}
              aria-current={isConnections ? "page" : undefined}
            >
              <Plug />
              连接<span>{connections.length}</span>
            </Button>
            <Button
              variant="ghost"
              className={!isConnections ? "nav-item selected" : "nav-item"}
              onClick={() => navigate("profiles")}
              disabled={busy || page.kind === "profile-editor"}
              aria-current={!isConnections ? "page" : undefined}
            >
              <KeyRound />
              Profiles<span>{profiles.length}</span>
            </Button>
          </nav>
        </div>
        <div className="server-info">
          <Badge
            variant="outline"
            className={active ? "status-badge good" : "status-badge"}
          >
            <span className="status-dot" />
            {state.gatewayMode !== "running"
              ? "模型服务暂停 / 恢复中"
              : active
                ? "网关运行中"
                : "等待配置加载"}
          </Badge>
          <p className="mono">{location.host}</p>
          <p className="hint">远端监听 {state.listen}</p>
          {session.token && (
            <Button
              variant="ghost"
              className="logout"
              onClick={() => {
                if (
                  page.kind !== "profile-editor" ||
                  window.confirm("退出后将丢弃未保存的编辑，确定退出？")
                ) {
                  controller.current.abort();
                  onLogout();
                }
              }}
              disabled={busy}
            >
              <LogOut />
              退出管理
            </Button>
          )}
        </div>
      </aside>
      <div className="workspace">
        <header className="topbar">
          <div className="breadcrumb">
            <span className="muted">工作空间</span>
            <span>/</span>
            <span>{isConnections ? "连接" : "Profiles"}</span>
            {page.kind === "profile-editor" && (
              <>
                <span>/</span>
                <span>模型映射</span>
              </>
            )}
          </div>
          <div className="actions">
            <span className={"revision" + (active ? " good" : "")}>
              <span className="status-dot" />
              已保存 r{state.revision}
              <span className="revision-divider">/</span>已生效 r
              {state.activeRevision}
            </span>
            <Button
              variant="ghost"
              size="icon"
              aria-label="刷新状态"
              disabled={busy || page.kind === "profile-editor"}
              onClick={refresh}
            >
              <RefreshCw className={busy ? "spinning" : ""} />
            </Button>
          </div>
        </header>
        <main className="main-content">
          <FeedbackMessage value={feedback} />
          {state.gatewayMode !== "running" && (
            <div className="feedback" role="status">
              模型服务当前暂停或正在恢复，管理页面可继续使用。
              {!oauthPending(oauthJob) && (
                <Button
                  variant="outline"
                  size="sm"
                  disabled={busy}
                  onClick={resumeModels}
                >
                  恢复模型服务
                </Button>
              )}
            </div>
          )}
          {page.kind === "profile-editor" ? (
            <ProfileEditor
              key={page.name || "new"}
              state={state}
              profile={selectedProfile || null}
              onClose={() => {
                setPage({ kind: "profiles" });
                setQuery("");
              }}
              onSave={save}
            />
          ) : (
            <>
              <div className="page-heading">
                <div>
                  <p className="eyebrow">
                    {isConnections ? "CONNECTIONS" : "PROFILES"}
                  </p>
                  <h1>{isConnections ? "上游连接" : "下游 Profiles"}</h1>
                  <p className="muted">
                    {isConnections
                      ? "管理独立的上游地址、认证方式和模型清单。"
                      : "为不同 Agent 分配专属 key 与模型映射。"}
                  </p>
                </div>
                <Button
                  disabled={
                    busy ||
                    (!isConnections && (!connections.length || !!createdKey))
                  }
                  onClick={() =>
                    isConnections
                      ? setEditor({ connection: null })
                      : setPage({ kind: "profile-editor", name: null })
                  }
                >
                  <Plus />
                  {isConnections ? "添加连接" : "创建 profile"}
                </Button>
              </div>
              <div className="list-toolbar">
                <div className="search-field">
                  <Search aria-hidden="true" />
                  <Input
                    aria-label={isConnections ? "搜索连接" : "搜索 profile"}
                    placeholder={
                      isConnections ? "搜索连接名称…" : "搜索 profile 名称…"
                    }
                    value={query}
                    onChange={(e) => setQuery(e.target.value)}
                  />
                </div>
                {isConnections && (
                  <select
                    className="native-select protocol-filter"
                    aria-label="筛选协议"
                    value={protocol}
                    onChange={(e) => setProtocol(e.target.value)}
                  >
                    <option value="all">所有协议</option>
                    {Object.entries(protocols).map(([id, label]) => (
                      <option key={id} value={id}>
                        {label}
                      </option>
                    ))}
                  </select>
                )}
                <span className="list-count">
                  {isConnections
                    ? filteredConnections.length
                    : filteredProfiles.length}{" "}
                  个{isConnections ? "连接" : " profile"}
                </span>
              </div>
              {isConnections ? (
                <>
                  {filteredConnections.length ? (
                    <div className="table-container">
                      <table>
                        <thead>
                          <tr>
                            <th>连接名称</th>
                            <th>协议 / 认证</th>
                            <th>模型</th>
                            <th>凭证</th>
                            <th>
                              <span className="sr-only">操作</span>
                            </th>
                          </tr>
                        </thead>
                        <tbody>
                          {filteredConnections.map((c) => (
                            <tr key={c.name}>
                              <td>
                                <strong>{c.name}</strong>
                                <p className="table-url">{c.baseURL}</p>
                              </td>
                              <td>
                                <span>{protocols[c.protocol]}</span>
                                <div className="cell-badges">
                                  <Badge variant="outline">
                                    {c.authType === "api-key"
                                      ? "API_KEY"
                                      : "OAuth"}
                                  </Badge>
                                </div>
                              </td>
                              <td>
                                <Button
                                  variant="ghost"
                                  size="sm"
                                  className="model-count"
                                  aria-label={"查看 " + c.name + " 的全部模型"}
                                  onClick={() => setViewingModels(c.name)}
                                >
                                  {c.models.length} 个模型{" "}
                                  <Search aria-hidden="true" />
                                </Button>
                                <p className="hint model-summary">
                                  {c.models
                                    .slice(0, 3)
                                    .map((m) => m.name || m.id)
                                    .join("、")}
                                  {c.models.length > 3 &&
                                    ` 等 ${c.models.length} 个`}
                                </p>
                              </td>
                              <td>
                                <span
                                  className={
                                    c.credentialConfigured
                                      ? "credential-good"
                                      : "credential-missing"
                                  }
                                >
                                  {c.credentialConfigured ? "已配置" : "未配置"}
                                </span>
                              </td>
                              <td>
                                <div className="row-actions">
                                  <Button
                                    variant="ghost"
                                    size="sm"
                                    disabled={busy}
                                    aria-label={"编辑连接 " + c.name}
                                    onClick={() => setEditor({ connection: c })}
                                  >
                                    编辑
                                  </Button>
                                  {c.authType === "codex-oauth" &&
                                    state.oauthAvailable && (
                                      <>
                                        <Button
                                          variant="ghost"
                                          size="sm"
                                          aria-label={
                                            (c.credentialConfigured
                                              ? "重新登录 "
                                              : "登录 ") + c.name
                                          }
                                          disabled={
                                            busy || oauthPending(oauthJob)
                                          }
                                          onClick={() =>
                                            setOAuthConfirm({
                                              name: c.name,
                                              action: "login",
                                            })
                                          }
                                        >
                                          {c.credentialConfigured
                                            ? "重新登录"
                                            : "登录"}
                                        </Button>
                                        {c.credentialConfigured && (
                                          <Button
                                            variant="ghost"
                                            size="sm"
                                            disabled={
                                              busy || oauthPending(oauthJob)
                                            }
                                            onClick={() =>
                                              setOAuthConfirm({
                                                name: c.name,
                                                action: "logout",
                                              })
                                            }
                                          >
                                            退出授权
                                          </Button>
                                        )}
                                        <Button
                                          variant="ghost"
                                          size="icon"
                                          aria-label={
                                            "删除 OAuth 连接 " + c.name
                                          }
                                          disabled={
                                            busy || oauthPending(oauthJob)
                                          }
                                          onClick={() =>
                                            setOAuthConfirm({
                                              name: c.name,
                                              action: "delete",
                                            })
                                          }
                                        >
                                          <Trash2 />
                                        </Button>
                                      </>
                                    )}
                                  {c.authType === "api-key" && (
                                    <Button
                                      variant="ghost"
                                      size="icon"
                                      disabled={busy}
                                      aria-label={"删除连接 " + c.name}
                                      onClick={() => {
                                        setFeedback(null);
                                        setDeleting({
                                          kind: "connections",
                                          name: c.name,
                                        });
                                      }}
                                    >
                                      <Trash2 />
                                    </Button>
                                  )}
                                </div>
                              </td>
                            </tr>
                          ))}
                        </tbody>
                      </table>
                    </div>
                  ) : (
                    <Empty
                      icon={<Plug />}
                      title={
                        connections.length
                          ? "没有匹配的连接"
                          : "添加第一个上游连接"
                      }
                      text={
                        connections.length
                          ? "试试其他名称或协议。"
                          : "先配置连接和模型清单，再为 Agent 创建 profile。"
                      }
                    />
                  )}
                  <div className="page-note">
                    <ShieldCheck />
                    <p>
                      {state.oauthAvailable
                        ? "新增连接与 OAuth 登录支持热更新，已有请求持续运行。退出与删除 OAuth 连接会暂停模型服务，结束后自动恢复。认证方式切换仍通过 CLI 完成。"
                        : "OAuth 认证操作需要停止网关后使用 CLI。"}
                    </p>
                  </div>
                </>
              ) : (
                <>
                  {filteredProfiles.length ? (
                    <div className="table-container">
                      <table>
                        <thead>
                          <tr>
                            <th>Profile</th>
                            <th>模型映射</th>
                            <th>远端配置</th>
                            <th>
                              <span className="sr-only">操作</span>
                            </th>
                          </tr>
                        </thead>
                        <tbody>
                          {filteredProfiles.map((p) => (
                            <tr key={p.name}>
                              <td>
                                <strong>{p.name}</strong>
                                <p className="hint">独立下游 key</p>
                              </td>
                              <td>
                                <div className="profile-summary">
                                  {slots.map((slot) => (
                                    <div key={slot}>
                                      <span>{slot}</span>
                                      <code>{p.models[slot].public_model}</code>
                                      <ArrowRight />
                                      <span>
                                        {p.models[slot].connection} /{" "}
                                        {p.models[slot].target_model}
                                      </span>
                                    </div>
                                  ))}
                                </div>
                              </td>
                              <td>
                                {p.managed ? (
                                  <Badge variant="outline">受管</Badge>
                                ) : (
                                  <span className="muted">未应用</span>
                                )}
                              </td>
                              <td>
                                <div className="row-actions">
                                  <Button
                                    variant="ghost"
                                    size="sm"
                                    disabled={busy}
                                    aria-label={"编辑映射 " + p.name}
                                    onClick={() =>
                                      setPage({
                                        kind: "profile-editor",
                                        name: p.name,
                                      })
                                    }
                                  >
                                    编辑映射
                                  </Button>
                                  <Button
                                    variant="ghost"
                                    size="icon"
                                    disabled={busy || p.managed}
                                    aria-label={"删除 profile " + p.name}
                                    onClick={() => {
                                      setFeedback(null);
                                      setDeleting({
                                        kind: "profiles",
                                        name: p.name,
                                      });
                                    }}
                                  >
                                    <Trash2 />
                                  </Button>
                                </div>
                              </td>
                            </tr>
                          ))}
                        </tbody>
                      </table>
                    </div>
                  ) : (
                    <Empty
                      icon={<KeyRound />}
                      title={
                        profiles.length
                          ? "没有匹配的 profile"
                          : "为 Agent 创建专属配置"
                      }
                      text={
                        profiles.length
                          ? "试试其他名称。"
                          : connections.length
                            ? "为 opus、sonnet、haiku 三个档位设置模型映射。"
                            : "请先添加至少一个上游连接。"
                      }
                    />
                  )}
                  <div className="page-note">
                    <ShieldCheck />
                    <p>
                      新建 profile 的下游 key 仅展示一次。配置应用请在运行 Agent
                      的机器上完成；远端 CLI 的 profile apply 修改的是远端设置。
                    </p>
                  </div>
                </>
              )}
            </>
          )}
        </main>
      </div>
      {viewingModels && state.connections[viewingModels] && (
        <ConnectionModels
          key={viewingModels}
          connection={state.connections[viewingModels]}
          onClose={() => setViewingModels(null)}
        />
      )}
      {editor && (
        <ConnectionEditor
          connection={editor.connection}
          onClose={async () => {
            if (oauthJob?.draft && oauthJob.phase === "selecting")
              await cancelOAuth();
            setEditor(null);
          }}
          onSave={save}
          onDiscover={discover}
          oauthEnabled={state.oauthAvailable}
          oauthDraft={
            oauthJob?.draft &&
            (oauthPending(oauthJob) || oauthJob.phase === "selecting")
              ? oauthJob
              : null
          }
          onOAuthCreate={createOAuthConnection}
          onOAuthLogin={(name) => startOAuth(name, "authorize")}
        />
      )}
      {oauthJob && oauthVisible && (
        <OAuthDialog
          key={oauthJob.id}
          status={oauthJob}
          onCancel={cancelOAuth}
          onClose={() => {
            setOAuthVisible(false);
            if (
              (oauthJob.models?.length || oauthJob.modelError) &&
              state.connections[oauthJob.name]
            )
              setEditor({ connection: state.connections[oauthJob.name] });
          }}
        />
      )}
      <AlertDialog
        open={!!oauthConfirm}
        onOpenChange={(open) => {
          if (!open && !busy) setOAuthConfirm(null);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {oauthConfirm?.action === "login"
                ? "登录"
                : oauthConfirm?.action === "logout"
                  ? "退出授权"
                  : "删除"}{" "}
              OAuth · {oauthConfirm?.name}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {oauthConfirm?.action === "login"
                ? "登录期间模型服务持续运行。授权成功后自动更新该连接，已有请求继续完成。"
                : "操作期间会暂停模型请求并中断在途输出，结束后自动恢复。退出授权仅清除本地凭证；删除连接无法撤销。"}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <FeedbackMessage value={feedback?.error ? feedback : null} />
          <AlertDialogFooter>
            <AlertDialogCancel disabled={busy}>取消</AlertDialogCancel>
            <Button disabled={busy} onClick={confirmOAuth}>
              {oauthConfirm?.action === "login"
                ? "确认登录"
                : "确认并暂停模型服务"}
            </Button>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
      <AlertDialog
        open={!!deleting}
        onOpenChange={(open) => {
          if (!open && !busy) setDeleting(null);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              删除{deleting?.kind === "connections" ? "连接" : " profile"}「
              {deleting?.name}」？
            </AlertDialogTitle>
            <AlertDialogDescription>
              {deleting?.kind === "connections"
                ? "此操作无法撤销。被 profile 引用的连接无法删除，请先调整映射。"
                : "删除后此 profile 的下游 key 将失效。受管 profile 需先恢复对应配置。"}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <FeedbackMessage value={feedback?.error ? feedback : null} />
          <AlertDialogFooter>
            <AlertDialogCancel disabled={busy}>取消</AlertDialogCancel>
            <Button variant="destructive" disabled={busy} onClick={remove}>
              {busy ? "正在删除…" : "确认删除"}
            </Button>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
      <Dialog open={!!createdKey}>
        <DialogContent
          showCloseButton={false}
          onEscapeKeyDown={(e) => e.preventDefault()}
          onInteractOutside={(e) => e.preventDefault()}
          className="key-dialog"
        >
          <DialogHeader>
            <div className="key-symbol">
              <KeyRound />
            </div>
            <DialogTitle>profile 已创建，请保存专属 key</DialogTitle>
            <DialogDescription>
              此 key 仅展示一次。关闭提示、退出或刷新后无法再次查看。
            </DialogDescription>
          </DialogHeader>
          <div className="field">
            <Label htmlFor="profile-created-key">下游 key</Label>
            <Input
              ref={keyField}
              id="profile-created-key"
              readOnly
              value={createdKey || ""}
              autoComplete="off"
              spellCheck={false}
            />
            <Button variant="outline" onClick={copyKey}>
              {copied ? <Check /> : <Copy />}
              {copied ? "已复制 key" : "复制 key"}
            </Button>
            <p className="hint">
              如无法自动复制，请手动复制输入框中已选中的 key。
            </p>
          </div>
          <div className="key-address">
            <span className="hint">本地 Agent 的网关地址</span>
            <code>{location.origin}</code>
          </div>
          <Button
            onClick={() => {
              if (keyField.current) keyField.current.value = "";
              setCreatedKey(null);
              setCopied(false);
            }}
          >
            我已保存，关闭提示
          </Button>
        </DialogContent>
      </Dialog>
    </div>
  );
}
