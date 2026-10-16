import { useEffect, useRef, useState, type FormEvent } from "react";
import { Check, LoaderCircle, Radio } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
  SheetDescription,
} from "@/components/ui/sheet";
import {
  protocols,
  type Connection,
  type Model,
  type DiscoveryInput,
  oauthPending,
  type OAuthStatus,
} from "./api";

type Discovery =
  { kind: "idle" } | { kind: "testing" } | { kind: "success"; models: Model[] };

export function ConnectionEditor({
  connection,
  onClose,
  onSave,
  onDiscover,
  onOAuthLogin,
  oauthEnabled,
  oauthDraft,
  onOAuthCreate,
}: {
  connection: Connection | null;
  onClose: () => void | Promise<void>;
  onSave: (path: string, method: string, body: unknown) => Promise<void>;
  onDiscover: (input: DiscoveryInput) => Promise<Model[]>;
  onOAuthLogin: (name: string) => Promise<void>;
  oauthEnabled: boolean;
  oauthDraft: OAuthStatus | null;
  onOAuthCreate: (models: Model[]) => Promise<void>;
}) {
  const [creationAuth, setCreationAuth] = useState(
    oauthDraft ? "codex-oauth" : "api-key",
  );
  const [protocol, setProtocol] = useState(
    connection?.protocol || "chat-completions",
  );
  const oauth =
    connection?.authType === "codex-oauth" ||
    (!connection && creationAuth === "codex-oauth");
  const [selected, setSelected] = useState(
    () => new Set(connection?.models.map((m) => m.id) || []),
  );
  const [search, setSearch] = useState("");
  const [dirty, setDirty] = useState(false);
  const [saving, setSaving] = useState(false);
  const [discovery, setDiscovery] = useState<Discovery>({ kind: "idle" });
  const [credentialsChanged, setCredentialsChanged] = useState(false);
  const [error, setError] = useState("");
  const keyInput = useRef<HTMLInputElement>(null);
  const formRef = useRef<HTMLFormElement>(null);
  const busy =
    saving || discovery.kind === "testing" || oauthPending(oauthDraft);
  const draftReady = oauthDraft?.phase === "selecting";
  useEffect(() => {
    if (oauthDraft?.phase === "selecting")
      setSelected(new Set(oauthDraft.models?.map((m) => m.id) || []));
  }, [oauthDraft?.id, oauthDraft?.phase]);
  const needsDiscovery = !oauth && (!connection || credentialsChanged);
  const discoveredIDs = new Set(
    discovery.kind === "success" ? discovery.models.map((m) => m.id) : [],
  );
  const catalog = draftReady
    ? oauthDraft.models || []
    : discovery.kind === "success"
      ? [
          ...discovery.models,
          ...(connection?.models || []).filter(
            (saved) => !discoveredIDs.has(saved.id),
          ),
        ]
      : connection?.models || [];
  const selectedModels = catalog.filter((m) => selected.has(m.id));
  const visibleModels = catalog.filter((m) =>
    (m.id + " " + (m.name || ""))
      .toLowerCase()
      .includes(search.toLowerCase().trim()),
  );
  function toggleModel(id: string, checked: boolean) {
    setSelected((previous) => {
      const next = new Set(previous);
      if (checked) next.add(id);
      else next.delete(id);
      return next;
    });
    setDirty(true);
  }
  function keepSavedSelections() {
    setSelected(
      (previous) =>
        new Set(
          (connection?.models || [])
            .filter((m) => previous.has(m.id))
            .map((m) => m.id),
        ),
    );
  }
  function invalidateDiscovery() {
    setDiscovery({ kind: "idle" });
    setCredentialsChanged(true);
    setError("");
    keepSavedSelections();
  }
  async function discover() {
    const form = formRef.current;
    if (!form || busy) return;
    for (const name of ["baseURL", "apiKey"]) {
      const input = form.elements.namedItem(name);
      if (input instanceof HTMLInputElement && !input.reportValidity()) return;
    }
    const values = new FormData(form);
    const key = String(values.get("apiKey") || "").trim();
    setDiscovery({ kind: "testing" });
    setError("");
    try {
      const found = await onDiscover({
        ...(connection ? { connection: connection.name } : {}),
        protocol: connection?.protocol || String(values.get("protocol")),
        baseURL: oauth
          ? connection!.baseURL
          : String(values.get("baseURL") || "").trim(),
        ...(key ? { apiKey: key } : {}),
      });
      const known = new Set(catalog.map((m) => m.id));
      setSelected(
        (previous) =>
          new Set(
            [
              ...found.filter((m) => !known.has(m.id) || previous.has(m.id)),
              ...(connection?.models || []).filter((m) => previous.has(m.id)),
            ].map((m) => m.id),
          ),
      );
      setDirty(true);
      setDiscovery({ kind: "success", models: found });
    } catch (err) {
      setDiscovery({ kind: "idle" });
      setError(err instanceof Error ? err.message : "测试连接失败，请重试。");
      keepSavedSelections();
    }
  }
  async function close() {
    if (!busy && (!dirty || window.confirm("放弃尚未保存的连接更改？"))) {
      try {
        await onClose();
      } catch {
        setError("未能确认取消授权，请重试。");
      }
    }
  }
  async function login() {
    const form = formRef.current;
    if (!form || busy || !form.reportValidity()) return;
    setSaving(true);
    setError("");
    try {
      await onOAuthLogin(String(new FormData(form).get("name") || "").trim());
    } catch (err) {
      setError(err instanceof Error ? err.message : "登录失败，请重试。");
    } finally {
      setSaving(false);
    }
  }
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (busy) return;
    if (oauth && !connection && !draftReady) {
      setError("请先登录并获取模型。");
      return;
    }
    if (needsDiscovery && discovery.kind !== "success") {
      setError("请先测试连接并获取可用模型。");
      return;
    }
    const form = new FormData(event.currentTarget);
    const entries = selectedModels.map((m) => ({
      id: m.id,
      name: m.name || "",
    }));
    if (
      !entries.length ||
      entries.some((m) => !m.id) ||
      new Set(entries.map((m) => m.id)).size !== entries.length
    ) {
      setError(
        oauth
          ? "请填写至少一个模型，并确保模型 ID 非空且不重复。"
          : "请至少选择一个模型。",
      );
      return;
    }
    const apiKey = String(form.get("apiKey") || "").trim();
    const body = {
      models: entries,
      ...(!oauth
        ? {
            baseURL: String(form.get("baseURL") || "").trim(),
            ...(apiKey ? { apiKey } : {}),
          }
        : {}),
      ...(!connection
        ? {
            name: String(form.get("name") || "").trim(),
            protocol: form.get("protocol"),
          }
        : {}),
    };
    setSaving(true);
    setError("");
    try {
      if (!connection && oauth) {
        await onOAuthCreate(entries);
        return;
      } else
        await onSave(
          connection
            ? "connections/" + encodeURIComponent(connection.name)
            : "connections",
          connection ? "PATCH" : "POST",
          body,
        );
      if (keyInput.current) keyInput.current.value = "";
      await onClose();
    } catch (err) {
      setError(err instanceof Error ? err.message : "保存失败，请重试。");
    } finally {
      setSaving(false);
    }
  }
  return (
    <Sheet
      open
      onOpenChange={(open) => {
        if (!open) close();
      }}
    >
      <SheetContent
        className="connection-sheet"
        onEscapeKeyDown={(e) => {
          if (busy) e.preventDefault();
        }}
        onInteractOutside={(e) => e.preventDefault()}
      >
        <SheetHeader>
          <SheetTitle>
            {connection
              ? "编辑连接 · " + connection.name
              : oauth
                ? "OAuth 登录"
                : "添加 API_KEY 连接"}
          </SheetTitle>
          <SheetDescription>
            {oauth
              ? connection
                ? "获取并选择可用模型，登录与退出可在连接列表操作。"
                : "当前支持官方 Codex 设备码授权。先登录并自动获取账号可用模型，默认全选。确认所选模型后才创建 connection。"
              : "测试连接后选择要启用的模型，只有选中的模型会保存到连接。"}
          </SheetDescription>
        </SheetHeader>
        <form
          className="sheet-form"
          ref={formRef}
          onSubmit={submit}
          onChange={() => setDirty(true)}
        >
          <fieldset disabled={busy} className="form-fields">
            {!connection && oauthEnabled && (
              <div className="field">
                <Label htmlFor="connection-auth">认证方式</Label>
                <select
                  id="connection-auth"
                  className="native-select"
                  value={creationAuth}
                  disabled={!!oauthDraft}
                  onChange={(e) => {
                    setCreationAuth(e.target.value);
                    invalidateDiscovery();
                  }}
                >
                  <option value="api-key">API_KEY</option>
                  <option value="codex-oauth">OAuth</option>
                </select>
              </div>
            )}
            <div className="field">
              <Label htmlFor="connection-name">名称</Label>
              <Input
                id="connection-name"
                name="name"
                defaultValue={connection?.name || oauthDraft?.name}
                disabled={!!connection || !!oauthDraft}
                required
                pattern="[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}"
                maxLength={64}
                autoComplete="off"
                placeholder="例如 work-api"
              />
            </div>
            {!oauth && (
              <>
                <div className="field">
                  <Label htmlFor="connection-protocol">协议</Label>
                  <select
                    id="connection-protocol"
                    name="protocol"
                    className="native-select"
                    value={oauth ? "responses" : protocol}
                    disabled={!!connection || oauth}
                    onChange={(e) => {
                      const value = e.target.value;
                      if (
                        value === "responses" ||
                        value === "chat-completions" ||
                        value === "anthropic-messages"
                      )
                        setProtocol(value);
                      invalidateDiscovery();
                    }}
                  >
                    {Object.entries(protocols).map(([id, label]) => (
                      <option key={id} value={id}>
                        {label}
                      </option>
                    ))}
                  </select>
                  <p className="hint">连接创建后协议固定。</p>
                </div>
                <div className="field">
                  <Label htmlFor="connection-url">API 根地址</Label>
                  <Input
                    id="connection-url"
                    name="baseURL"
                    type="url"
                    key={oauth ? "oauth-url" : "api-url"}
                    defaultValue={
                      oauth
                        ? "https://chatgpt.com/backend-api/codex"
                        : connection?.baseURL
                    }
                    disabled={oauth}
                    onChange={invalidateDiscovery}
                    required
                    placeholder="https://api.example.com/v1"
                  />
                </div>
              </>
            )}
            {!oauth && (
              <div className="field">
                <Label htmlFor="connection-key">API key</Label>
                <Input
                  ref={keyInput}
                  id="connection-key"
                  name="apiKey"
                  type="password"
                  required={!connection}
                  autoComplete="new-password"
                  spellCheck={false}
                  onChange={invalidateDiscovery}
                  placeholder={
                    connection ? "留空保留原凭证" : "输入此连接的 API key"
                  }
                />
                <p className="hint">凭证不会在读取配置时返回。</p>
              </div>
            )}
            {(!oauth || connection) && (
              <div className="discovery-section">
                <Button
                  type="button"
                  variant="outline"
                  onClick={discover}
                  disabled={busy}
                >
                  {discovery.kind === "testing" ? (
                    <LoaderCircle className="spinning" />
                  ) : (
                    <Radio />
                  )}
                  {discovery.kind === "testing"
                    ? "正在获取模型…"
                    : discovery.kind === "success"
                      ? "重新测试连接"
                      : "测试连接并获取模型"}
                </Button>
                <p className="hint">
                  仅请求模型目录，不发送推理请求。
                  {connection &&
                    !oauth &&
                    " API key 留空时使用已保存凭证；测试新地址需填写对应的 key。"}
                </p>
                {discovery.kind === "success" && (
                  <p className="discovery-success" role="status">
                    <Check />
                    目录访问成功，已获取 {discovery.models.length} 个模型。
                  </p>
                )}
              </div>
            )}
            {(!oauth || connection || draftReady) && (
              <section className="discovered-models" aria-label="可用模型">
                <div className="section-heading">
                  <h2>可用模型</h2>
                  <span className="hint" role="status">
                    已选 {selectedModels.length} / {catalog.length}
                  </span>
                </div>
                {catalog.length ? (
                  <>
                    <p className="hint">
                      仅选中的模型可用于 profile 映射。请至少选择一个。
                      {connection &&
                        " 已被 profile 引用的模型需先调整映射后再取消。"}
                    </p>
                    <Input
                      className="model-search"
                      aria-label="搜索可用模型"
                      placeholder="搜索模型 ID 或展示名…"
                      value={search}
                      onChange={(e) => setSearch(e.target.value)}
                    />
                    <div className="model-selection-actions">
                      <Button
                        type="button"
                        variant="ghost"
                        size="sm"
                        disabled={!visibleModels.length}
                        onClick={() => {
                          setSelected(
                            (previous) =>
                              new Set([
                                ...previous,
                                ...visibleModels.map((m) => m.id),
                              ]),
                          );
                          setDirty(true);
                        }}
                      >
                        全选当前结果
                      </Button>
                      <Button
                        type="button"
                        variant="ghost"
                        size="sm"
                        disabled={!selectedModels.length}
                        onClick={() => {
                          setSelected(new Set());
                          setDirty(true);
                        }}
                      >
                        清空选择
                      </Button>
                    </div>
                    <ul>
                      {visibleModels.map((model) => (
                        <li key={model.id}>
                          <label className="model-choice">
                            <input
                              type="checkbox"
                              aria-label={"启用模型 " + model.id}
                              checked={selected.has(model.id)}
                              onChange={(e) =>
                                toggleModel(model.id, e.target.checked)
                              }
                            />
                            <span>
                              <code>{model.id}</code>
                              {model.name && (
                                <span className="hint">{model.name}</span>
                              )}
                              {discovery.kind === "success" &&
                                !discoveredIDs.has(model.id) && (
                                  <span className="hint">
                                    已保存，本次目录未返回；取消后将移出连接。
                                  </span>
                                )}
                            </span>
                          </label>
                        </li>
                      ))}
                    </ul>
                    {!visibleModels.length && (
                      <p className="hint">没有匹配的模型。</p>
                    )}
                  </>
                ) : (
                  <p className="hint">
                    {oauth
                      ? "登录后可重新获取账号的可用模型目录。"
                      : "测试连接后，自动展示此 API key 可访问的模型目录。"}
                  </p>
                )}
              </section>
            )}
          </fieldset>
          {error && (
            <p className="feedback error" role="alert">
              {error}
            </p>
          )}
          {!connection && oauth && (
            <p className="page-note">
              登录、获取模型和保存期间模型服务持续运行，已有请求不受影响。登录或获取模型失败、放弃创建时不会保存
              connection。
            </p>
          )}
          <div className="form-footer">
            {!connection && oauth && (
              <Button
                type="button"
                variant="outline"
                disabled={busy || draftReady}
                onClick={login}
              >
                登录并获取模型
              </Button>
            )}
            <Button
              type="submit"
              disabled={
                busy ||
                (needsDiscovery && discovery.kind !== "success") ||
                (!connection && oauth && !draftReady) ||
                !selectedModels.length
              }
            >
              {saving
                ? "正在保存…"
                : !connection && oauth
                  ? "保存 Connection"
                  : "保存连接"}
            </Button>
            <Button
              type="button"
              variant="outline"
              disabled={busy}
              onClick={close}
            >
              取消
            </Button>
          </div>
        </form>
      </SheetContent>
    </Sheet>
  );
}
