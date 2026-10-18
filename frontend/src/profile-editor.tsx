import { useEffect, useState, type FormEvent } from "react";
import { ArrowDown, ArrowLeft, ArrowRight } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { ModelPicker } from "./model-picker";
import { CompatibilityNotice } from "./compatibility-notice";
import {
  initialBindings,
  slots,
  sorted,
  type Binding,
  type GatewayState,
  type Profile,
  type Slot,
} from "./api";

export function ProfileEditor({
  state,
  profile,
  onClose,
  onSave,
}: {
  state: GatewayState;
  profile: Profile | null;
  onClose: () => void;
  onSave: (path: string, method: string, body: unknown) => Promise<void>;
}) {
  const connections = sorted(state.connections);
  const [name, setName] = useState(profile?.name || "");
  const [bindings, setBindings] = useState(() =>
    initialBindings(connections, profile || undefined),
  );
  const [dirty, setDirty] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => {
    if (!dirty) return;
    const prevent = (e: BeforeUnloadEvent) => e.preventDefault();
    window.addEventListener("beforeunload", prevent);
    return () => window.removeEventListener("beforeunload", prevent);
  }, [dirty]);
  function change(slot: Slot, patch: Partial<Binding>) {
    setBindings((previous) => ({
      ...previous,
      [slot]: { ...previous[slot], ...patch },
    }));
    setDirty(true);
  }
  function close() {
    if (!busy && (!dirty || window.confirm("放弃尚未保存的模型映射？")))
      onClose();
  }
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const models = Object.fromEntries(
      slots.map((slot) => [
        slot,
        Object.fromEntries(
          Object.entries(bindings[slot]).map(([key, value]) => [
            key,
            value.trim(),
          ]),
        ),
      ]),
    );
    if (
      slots.some(
        (slot) =>
          !state.connections[bindings[slot].connection]?.models.some(
            (m) => m.id === bindings[slot].target_model,
          ),
      )
    ) {
      setError("请为每个档位选择有效的连接与上游模型。");
      return;
    }
    if (
      new Set(slots.map((slot) => bindings[slot].public_model.trim())).size !==
      slots.length
    ) {
      setError("各档位的公开模型 ID 不能重复。");
      return;
    }
    setBusy(true);
    setError("");
    try {
      await onSave(
        profile ? "profiles/" + encodeURIComponent(profile.name) : "profiles",
        profile ? "PATCH" : "POST",
        { models, ...(!profile ? { name: name.trim() } : {}) },
      );
      setDirty(false);
      onClose();
    } catch (err) {
      setError(err instanceof Error ? err.message : "保存失败，请重试。");
    } finally {
      setBusy(false);
    }
  }
  return (
    <form onSubmit={submit} className="profile-editor">
      <Button
        type="button"
        variant="ghost"
        className="back-button"
        onClick={close}
        disabled={busy}
      >
        <ArrowLeft />
        返回 Profiles
      </Button>
      <div className="page-heading">
        <div>
          <p className="eyebrow">PROFILE / 模型映射</p>
          <h1>{profile ? profile.name : "创建 profile"}</h1>
          <p className="muted">
            为三个模型档位分别设置公开 ID、连接与上游模型。
          </p>
        </div>
        <Badge variant="outline">独立下游 key</Badge>
      </div>
      <fieldset disabled={busy} className="form-fields">
        <div className="field profile-name">
          <Label htmlFor="profile-name">名称</Label>
          <Input
            id="profile-name"
            value={name}
            onChange={(e) => {
              setName(e.target.value);
              setDirty(true);
            }}
            disabled={!!profile}
            required
            pattern="[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}"
            maxLength={64}
            placeholder="例如 daily-coding"
            autoComplete="off"
          />
        </div>
        <div className="mapping-grid">
          {slots.map((slot, i) => {
            const b = bindings[slot];
            const connection = state.connections[b.connection];
            return (
              <section
                className="mapping-card"
                key={slot}
                aria-label={slot.toUpperCase() + " 映射"}
              >
                <header>
                  <h2>{slot.toUpperCase()}</h2>
                  <p className="hint">
                    {["深度推理", "日常编程", "快速任务"][i]}
                  </p>
                </header>
                <div className="field">
                  <Label htmlFor={slot + "-public"}>公开模型 ID</Label>
                  <Input
                    id={slot + "-public"}
                    value={b.public_model}
                    onChange={(e) =>
                      change(slot, { public_model: e.target.value })
                    }
                    required
                    spellCheck={false}
                  />
                </div>
                <div className="mapping-arrow">
                  <ArrowDown aria-hidden="true" />
                </div>
                <div className="field">
                  <Label htmlFor={slot + "-connection"}>上游连接</Label>
                  <select
                    id={slot + "-connection"}
                    className="native-select"
                    value={b.connection}
                    required
                    onChange={(e) =>
                      change(slot, {
                        connection: e.target.value,
                        target_model:
                          state.connections[e.target.value]?.models[0]?.id ||
                          "",
                      })
                    }
                  >
                    {!connection && <option value="">选择连接</option>}
                    {connections.map((c) => (
                      <option key={c.name} value={c.name}>
                        {c.name}
                      </option>
                    ))}
                  </select>
                </div>
                <div className="field">
                  <Label htmlFor={slot + "-model"}>上游模型</Label>
                  <ModelPicker
                    id={slot + "-model"}
                    models={connection?.models || []}
                    value={b.target_model}
                    onChange={(id) => change(slot, { target_model: id })}
                  />
                  {connection && (
                    <p className="hint model-id">
                      {b.target_model || "尚未选择"}
                    </p>
                  )}
                </div>
                {connection && (
                  <CompatibilityNotice
                    connection={connection}
                    model={b.target_model}
                  />
                )}
              </section>
            );
          })}
        </div>
      </fieldset>
      <section className="mapping-preview" aria-label="映射预览">
        <div className="section-heading">
          <h2>映射预览</h2>
          <span className="hint">公开 ID → 连接 / 上游模型</span>
        </div>
        {slots.map((slot) => (
          <div className="mapping-preview-row" key={slot}>
            <code>{bindings[slot].public_model || "公开 ID"}</code>
            <ArrowRight aria-hidden="true" />
            <span>
              <strong>{bindings[slot].connection || "未选择连接"}</strong>
              <span className="muted"> / </span>
              <code>{bindings[slot].target_model || "未选择模型"}</code>
            </span>
          </div>
        ))}
      </section>
      {error && (
        <p className="feedback error" role="alert">
          {error}
        </p>
      )}
      <div className="profile-save">
        <span className="muted">
          {busy
            ? "正在保存并等待网关加载…"
            : dirty
              ? "有未保存的更改"
              : profile
                ? "所有更改已保存"
                : "创建后请保存一次性下游 key"}
        </span>
        <div className="actions">
          <Button
            type="button"
            variant="outline"
            disabled={busy}
            onClick={close}
          >
            取消
          </Button>
          <Button disabled={busy} type="submit">
            {busy ? "正在保存…" : profile ? "保存映射" : "创建 profile"}
          </Button>
        </div>
      </div>
    </form>
  );
}
