import { useRef, useState } from "react";
import { Copy, ExternalLink, LoaderCircle, ShieldCheck } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { oauthPending, type OAuthStatus } from "./api";

export function OAuthDialog({
  status,
  onCancel,
  onClose,
}: {
  status: OAuthStatus;
  onCancel: () => Promise<void>;
  onClose: () => void;
}) {
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const codeField = useRef<HTMLInputElement>(null);
  const pending = oauthPending(status);
  const selecting = status.phase === "selecting";
  async function cancel() {
    setBusy(true);
    setError("");
    try {
      await onCancel();
    } catch {
      setError("取消请求未能确认，请稍后重试。");
    } finally {
      setBusy(false);
    }
  }
  async function copy() {
    try {
      await navigator.clipboard.writeText(status.code || "");
    } catch {
      codeField.current?.focus();
      codeField.current?.select();
    }
  }
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !pending && !selecting) onClose();
      }}
    >
      <DialogContent
        className="oauth-dialog"
        showCloseButton={!pending && !selecting}
        onEscapeKeyDown={(e) => {
          if (pending || selecting) e.preventDefault();
        }}
        onInteractOutside={(e) => {
          if (pending || selecting) e.preventDefault();
        }}
      >
        <DialogHeader>
          <span className="key-symbol">
            <ShieldCheck />
          </span>
          <DialogTitle>
            {selecting ? "创建 connection" : "OAuth"} · {status.name}
          </DialogTitle>
          <DialogDescription>
            {status.draft
              ? "先登录并获取模型，选择确认后再保存 connection。登录和保存期间模型服务持续运行。"
              : status?.pausesModels
                ? "操作期间模型服务暂停，结束后自动恢复。"
                : "登录期间模型服务持续运行，已有请求不受影响。"}
          </DialogDescription>
        </DialogHeader>
        {pending && (
          <p className="oauth-progress" role="status">
            <LoaderCircle className="spinning" />
            {status.phase === "starting"
              ? "正在准备授权…"
              : status.phase === "creating"
                ? "正在创建 connection…"
                : status.phase === "discovering"
                  ? "授权成功，正在获取可用模型…"
                  : status.phase === "resuming"
                    ? "正在恢复模型服务…"
                    : "等待你在官方页面完成授权…"}
          </p>
        )}
        {status.phase === "waiting" &&
          status.code &&
          status.verificationURL && (
            <>
              <div className="field">
                <Label htmlFor="oauth-device-code">设备授权码</Label>
                <Input
                  ref={codeField}
                  id="oauth-device-code"
                  readOnly
                  value={status.code}
                  autoComplete="off"
                  spellCheck={false}
                />
                <Button type="button" variant="outline" onClick={copy}>
                  <Copy />
                  复制授权码
                </Button>
              </div>
              <a
                className="oauth-link"
                href={status.verificationURL}
                target="_blank"
                rel="noopener noreferrer"
              >
                <ExternalLink />
                打开 OpenAI 官方授权页面
              </a>
              <p className="hint">
                在官方页面输入上方设备码并完成账号授权。此操作最多等待 15
                分钟；请勿把设备码分享给他人。
              </p>
            </>
          )}
        {!pending && (
          <p
            className={"feedback" + (status.phase === "failed" ? " error" : "")}
            role={status.phase === "failed" ? "alert" : "status"}
          >
            {status.message}
          </p>
        )}
        {status.modelError && (
          <p className="feedback error" role="alert">
            {status.modelError}
          </p>
        )}
        {!!status.models?.length && (
          <p role="status">
            已自动获取 {status.models.length}{" "}
            个模型，默认全选。可继续查看并取消不需要的模型。
          </p>
        )}
        {error && (
          <p className="feedback error" role="alert">
            {error}
          </p>
        )}
        <div className="actions">
          {pending ? (
            <Button
              variant="outline"
              disabled={busy || status.phase === "resuming"}
              onClick={cancel}
            >
              {busy
                ? "正在取消…"
                : status?.pausesModels
                  ? "取消操作并恢复模型服务"
                  : "取消登录"}
            </Button>
          ) : (
            <Button onClick={onClose}>
              {status.models?.length || status.modelError
                ? "查看并选择模型"
                : "完成"}
            </Button>
          )}
        </div>
      </DialogContent>
    </Dialog>
  );
}
