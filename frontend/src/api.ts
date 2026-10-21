import { z } from "zod";

export const slots = ["opus", "sonnet", "haiku", "fable"] as const;
export type Slot = (typeof slots)[number];
export const agents = {
  "claude-code": "Claude Code",
  "claude-desktop": "Claude Desktop",
};
export type Agent = keyof typeof agents;
export const protocols = {
  "chat-completions": "Chat Completions",
  "anthropic-messages": "Anthropic Messages",
  responses: "Responses",
};
const modelSchema = z.object({ id: z.string(), name: z.string().optional() });
const compatibilitySchema = z.object({
  outputLimit: z.literal("not-enforced"),
  reasoning: z.enum(["effort-mapping", "unavailable"]),
  efforts: z.array(z.string()),
  thinkingOff: z.enum(["none", "lowest-effort", "unverified"]),
  contextWindow: z.literal("unverified"),
  warnings: z.array(z.string()),
});
export const discoveredSchema = z.object({
  models: z.array(modelSchema).min(1),
});
export type DiscoveryInput = {
  connection?: string;
  protocol: string;
  baseURL: string;
  apiKey?: string;
};
const connectionSchema = z.object({
  name: z.string(),
  authType: z.enum(["api-key", "codex-oauth"]),
  protocol: z.enum(["chat-completions", "anthropic-messages", "responses"]),
  baseURL: z.string(),
  models: z.array(modelSchema),
  credentialConfigured: z.boolean(),
  compatibility: z.record(z.string(), compatibilitySchema).optional(),
});
const bindingSchema = z.object({
  public_model: z.string(),
  connection: z.string(),
  target_model: z.string(),
});
const profileSchema = z.object({
  name: z.string(),
  agent: z.enum(["claude-code", "claude-desktop"]),
  managed: z.boolean(),
  models: z.object({
    opus: bindingSchema,
    sonnet: bindingSchema,
    haiku: bindingSchema,
    fable: bindingSchema,
  }),
});
export const oauthStatusSchema = z.object({
  id: z.string(),
  name: z.string(),
  phase: z.enum([
    "starting",
    "waiting",
    "discovering",
    "selecting",
    "creating",
    "resuming",
    "succeeded",
    "failed",
    "cancelled",
  ]),
  code: z.string().optional(),
  verificationURL: z.literal("https://auth.openai.com/codex/device").optional(),
  expiresAt: z.string(),
  message: z.string().optional(),
  models: z.array(modelSchema).optional(),
  modelError: z.string().optional(),
  draft: z.boolean().optional(),
  pausesModels: z.boolean().optional(),
});
export type OAuthStatus = z.infer<typeof oauthStatusSchema>;
export function oauthPending(status: OAuthStatus | null) {
  return (
    !!status &&
    ["starting", "waiting", "discovering", "creating", "resuming"].includes(
      status.phase,
    )
  );
}
export const stateSchema = z.object({
  oauthAvailable: z.boolean(),
  gatewayMode: z.enum(["running", "starting", "pausing", "paused", "failed"]),
  revision: z.number().int().nonnegative(),
  activeRevision: z.number().int().nonnegative(),
  ready: z.boolean(),
  listen: z.string(),
  connections: z.record(z.string(), connectionSchema),
  profiles: z.record(z.string(), profileSchema),
});
export const savedSchema = z.object({
  saved: z.literal(true),
  active: z.boolean(),
  revision: z.number().int().nonnegative(),
  profileKey: z.string().optional(),
});
export type GatewayState = z.infer<typeof stateSchema>;
export type Connection = z.infer<typeof connectionSchema>;
export type Model = z.infer<typeof modelSchema>;
export type Binding = z.infer<typeof bindingSchema>;
export type Profile = z.infer<typeof profileSchema>;
export type Bindings = Profile["models"];

export class Unauthorized extends Error {}
export async function request<T>(
  token: string,
  signal: AbortSignal,
  path: string,
  schema: z.ZodType<T>,
  method = "GET",
  body?: unknown,
): Promise<T> {
  let response: Response;
  try {
    response = await fetch("/admin/api/" + path, {
      method,
      signal,
      headers: {
        ...(token ? { Authorization: "Bearer " + token } : {}),
        ...(body === undefined ? {} : { "Content-Type": "application/json" }),
      },
      body: body === undefined ? undefined : JSON.stringify(body),
      credentials: "omit",
      cache: "no-store",
      redirect: "error",
    });
  } catch {
    throw new Error(
      method === "GET" || path === "connections/test"
        ? "无法读取网关，请检查连接后重试。"
        : "未能确认保存结果，请先刷新状态核对，避免重复创建。",
    );
  }
  if (response.status === 401)
    throw new Unauthorized("管理令牌无效或已失效，请重新连接。");
  let data: unknown;
  try {
    data = await response.json();
  } catch {
    throw new Error("网关响应无效，请刷新状态核对操作结果。");
  }
  if (!response.ok) {
    const error = z.object({ error: z.string() }).safeParse(data);
    throw new Error(
      error.success ? error.data.error : "操作失败，请刷新后重试。",
    );
  }
  const parsed = schema.safeParse(data);
  if (!parsed.success)
    throw new Error("网关响应格式不受支持，请刷新状态核对操作结果。");
  return parsed.data;
}
export function sorted<T extends { name: string }>(items: Record<string, T>) {
  return Object.values(items).sort((a, b) => a.name.localeCompare(b.name));
}
export function initialBindings(
  connections: Connection[],
  profile?: Profile,
): Bindings {
  const make = (slot: Slot): Binding =>
    profile
      ? { ...profile.models[slot] }
      : {
          public_model: "claude-" + slot,
          connection: connections[0]?.name || "",
          target_model: connections[0]?.models[0]?.id || "",
        };
  return {
    opus: make("opus"),
    sonnet: make("sonnet"),
    haiku: make("haiku"),
    fable: make("fable"),
  };
}
