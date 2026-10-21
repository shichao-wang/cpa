import { expect, test, type Page } from "@playwright/test";

function fixture() {
  const binding = (model: string) => ({
    public_model: "claude-" + model,
    connection: "work-api",
    target_model: "large",
    label: "推理模型",
    description: model[0].toUpperCase() + model.slice(1),
  });
  return {
    oauthAvailable: false,
    gatewayMode: "running" as const,
    revision: 12,
    activeRevision: 12,
    ready: true,
    listen: "127.0.0.1:18317",
    connections: {
      "work-api": {
        name: "work-api",
        protocol: "chat-completions",
        authType: "api-key",
        baseURL: "https://upstream.example/v1",
        credentialConfigured: true,
        models: [
          { id: "large", name: "推理模型" },
          { id: "fast", name: "快速模型" },
        ],
      },
      "team-messages": {
        name: "team-messages",
        protocol: "anthropic-messages",
        authType: "api-key",
        baseURL: "https://messages.example",
        credentialConfigured: true,
        models: [{ id: "coding", name: "编程模型" }],
      },
      "codex-personal": {
        name: "codex-personal",
        protocol: "responses",
        authType: "codex-oauth",
        baseURL: "https://chatgpt.com/backend-api/codex",
        credentialConfigured: true,
        models: [{ id: "coding-model" }],
      },
    },
    profiles: {
      daily: {
        name: "daily",
        agent: "claude-code",
        managed: false,
        models: {
          opus: binding("opus"),
          sonnet: binding("sonnet"),
          haiku: binding("haiku"),
          fable: binding("fable"),
        },
      },
    },
  };
}
async function connect(page: Page, workspace = true) {
  await page.goto("/ui/");
  await page.getByLabel("管理令牌", { exact: true }).fill("test-admin-token");
  await page.getByRole("button", { name: "连接网关", exact: true }).click();
  if (workspace)
    await expect(
      page.getByRole("heading", { name: "上游连接", exact: true }),
    ).toBeVisible();
}

test("连接搜索、协议筛选和 OAuth 编辑边界", async ({ page }) => {
  let patch: unknown;
  await page.route("**/admin/api/**", async (route) => {
    if (route.request().url().endsWith("/oauth/session")) {
      await route.fulfill({ json: null });
      return;
    }
    if (route.request().method() === "GET")
      await route.fulfill({ json: fixture() });
    else {
      patch = route.request().postDataJSON();
      await route.fulfill({
        json: { saved: true, active: true, revision: 12 },
      });
    }
  });
  await connect(page);
  await page.getByLabel("搜索连接").fill("work");
  await expect(
    page.getByRole("button", { name: "编辑连接 work-api" }),
  ).toBeVisible();
  await expect(
    page.getByRole("button", { name: "编辑连接 team-messages" }),
  ).toHaveCount(0);
  await page.getByLabel("搜索连接").fill("");
  await page.getByLabel("筛选协议").selectOption("responses");
  await expect(
    page.getByRole("button", { name: "删除连接 codex-personal" }),
  ).toHaveCount(0);
  await page.getByRole("button", { name: "编辑连接 codex-personal" }).click();
  await expect(page.getByLabel("API 根地址", { exact: true })).toHaveCount(0);
  await expect(page.getByLabel("协议", { exact: true })).toHaveCount(0);
  await expect(page.getByLabel("API key", { exact: true })).toHaveCount(0);
  await expect(
    page.getByRole("checkbox", { name: "启用模型 coding-model", exact: true }),
  ).toBeChecked();
  await page.getByRole("button", { name: "保存连接", exact: true }).click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  expect(patch).toEqual({ models: [{ id: "coding-model", name: "" }] });
});

test("四档映射联动、模型搜索和实际保存字段", async ({ page }) => {
  let patch: unknown;
  await page.route("**/admin/api/**", async (route) => {
    if (route.request().url().endsWith("/oauth/session")) {
      await route.fulfill({ json: null });
      return;
    }
    if (route.request().method() === "GET")
      await route.fulfill({ json: fixture() });
    else {
      patch = route.request().postDataJSON();
      await route.fulfill({
        json: { saved: true, active: true, revision: 12 },
      });
    }
  });
  await connect(page);
  await page.getByRole("button", { name: "Profiles", exact: false }).click();
  await page.getByRole("button", { name: "编辑映射 daily" }).click();
  await page.getByLabel("公开模型 ID", { exact: true }).first().fill("my-opus");
  await expect(page.getByText("展示名与说明", { exact: true })).toHaveCount(0);
  await expect(page.getByLabel("展示名（可选）")).toHaveCount(0);
  await expect(page.getByLabel("说明（可选）")).toHaveCount(0);
  const opus = page.getByRole("region", { name: "OPUS 映射" });
  await opus
    .getByLabel("上游连接", { exact: true })
    .selectOption("team-messages");
  await expect(
    opus.getByRole("combobox", { name: "上游模型", exact: true }),
  ).toHaveText("编程模型");
  const sonnet = page.getByRole("region", { name: "SONNET 映射" });
  await sonnet.getByRole("combobox", { name: "上游模型", exact: true }).click();
  await page.getByPlaceholder("搜索模型 ID 或展示名…").fill("快速");
  await page.getByRole("option", { name: "快速模型 fast" }).click();
  await expect(page.getByRole("region", { name: "映射预览" })).toContainText(
    "fast",
  );
  await page.getByRole("button", { name: "保存映射", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: "下游 Profiles" }),
  ).toBeVisible();
  const expected = Object.fromEntries(
    Object.entries(fixture().profiles.daily.models).map(
      ([slot, { public_model, connection, target_model }]) => [
        slot,
        { public_model, connection, target_model },
      ],
    ),
  );
  expect(patch).toEqual({
    agent: "claude-code",
    models: {
      ...expected,
      opus: {
        ...expected.opus,
        public_model: "my-opus",
        connection: "team-messages",
        target_model: "coding",
      },
      sonnet: { ...expected.sonnet, target_model: "fast" },
    },
  });
});

test("一次性 key 在刷新失败后仍保留，明确关闭后清除", async ({ page }) => {
  let created = false;
  await page.route("**/admin/api/**", async (route) => {
    if (route.request().url().endsWith("/oauth/session")) {
      await route.fulfill({ json: null });
      return;
    }
    if (route.request().method() === "GET") {
      if (created)
        await route.fulfill({ status: 503, json: { error: "暂不可用" } });
      else await route.fulfill({ json: fixture() });
    } else {
      created = true;
      await route.fulfill({
        json: {
          saved: true,
          active: false,
          revision: 13,
          profileKey: "cpa_profile_test-only",
        },
      });
    }
  });
  await connect(page);
  await page.getByRole("button", { name: "Profiles", exact: false }).click();
  await page.getByRole("button", { name: "创建 profile", exact: true }).click();
  await page.getByLabel("名称", { exact: true }).fill("new-agent");
  await page.getByRole("button", { name: "创建 profile", exact: true }).click();
  await expect(page.getByLabel("下游 key", { exact: true })).toHaveValue(
    "cpa_profile_test-only",
  );
  await page.keyboard.press("Escape");
  await expect(page.getByRole("dialog")).toBeVisible();
  expect(
    await page.evaluate(() => [localStorage.length, sessionStorage.length]),
  ).toEqual([0, 0]);
  await page.getByRole("button", { name: "我已保存，关闭提示" }).click();
  await expect(page.getByLabel("下游 key", { exact: true })).toHaveCount(0);
  await expect(page.getByRole("alert")).toContainText("配置已保存，但刷新失败");
});

test("创建 Desktop profile 发送 Agent 并显示对应本机接入指引", async ({
  page,
}) => {
  const state = fixture();
  let created: any;
  await page.route("**/admin/api/**", async (route) => {
    const request = route.request();
    if (request.url().endsWith("/oauth/session")) {
      await route.fulfill({ json: null });
    } else if (request.method() === "POST") {
      created = request.postDataJSON();
      Object.assign(state.profiles, {
        desktop: { ...created, managed: false },
      });
      await route.fulfill({
        json: { saved: true, active: true, revision: 12 },
      });
    } else await route.fulfill({ json: state });
  });
  await connect(page);
  await page.getByRole("button", { name: "Profiles", exact: false }).click();
  await page.getByRole("button", { name: "创建 profile", exact: true }).click();
  await expect(page.getByLabel("Agent", { exact: true })).toHaveValue(
    "claude-code",
  );
  await page.getByLabel("名称", { exact: true }).fill("desktop");
  await page
    .getByLabel("Agent", { exact: true })
    .selectOption("claude-desktop");
  const guide = page.getByRole("region", { name: "本机 CLI 接入指引" });
  await expect(guide).toContainText("完全退出 Claude Desktop");
  await expect(guide.locator(".cli-command").first()).toHaveText(
    "cpa profile apply desktop",
  );
  await expect(guide.locator(".cli-command").last()).toHaveText(
    "cpa profile restore --agent claude-desktop",
  );
  await expect(guide).toContainText(
    "--config-dir '/path/to/Application Support'",
  );
  await expect(guide).toContainText("Claude/");
  await expect(guide).toContainText("Claude-3p/");
  await expect(guide).toContainText("父目录");
  await expect(guide).not.toContainText("--settings");
  await page.getByRole("button", { name: "创建 profile", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: "下游 Profiles" }),
  ).toBeVisible();
  expect(created.agent).toBe("claude-desktop");
  expect(created.name).toBe("desktop");
  expect(Object.keys(created.models).sort()).toEqual([
    "fable",
    "haiku",
    "opus",
    "sonnet",
  ]);
  const row = page
    .getByRole("row")
    .filter({
      has: page.getByRole("button", { name: "编辑映射 desktop", exact: true }),
    });
  await expect(row).toContainText("Claude Desktop · 独立下游 key");
  await expect(row).toContainText("未应用");
});

test("编辑 Desktop 保留 Agent，未受管时可切换为 Code", async ({ page }) => {
  const state = fixture();
  state.profiles.daily.agent = "claude-desktop";
  const patches: any[] = [];
  await page.route("**/admin/api/**", async (route) => {
    const request = route.request();
    if (request.url().endsWith("/oauth/session")) {
      await route.fulfill({ json: null });
    } else if (request.method() === "PATCH") {
      const patch = request.postDataJSON();
      patches.push(patch);
      Object.assign(state.profiles.daily, patch);
      await route.fulfill({
        json: { saved: true, active: true, revision: 12 },
      });
    } else await route.fulfill({ json: state });
  });
  await connect(page);
  await page.getByRole("button", { name: "Profiles", exact: false }).click();
  await page.getByRole("button", { name: "编辑映射 daily" }).click();
  await expect(page.getByLabel("Agent", { exact: true })).toHaveValue(
    "claude-desktop",
  );
  await page
    .getByLabel("公开模型 ID", { exact: true })
    .first()
    .fill("desktop-opus");
  await page.getByRole("button", { name: "保存映射", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: "下游 Profiles" }),
  ).toBeVisible();
  expect(patches[0].agent).toBe("claude-desktop");
  await page.getByRole("button", { name: "编辑映射 daily" }).click();
  await expect(page.getByLabel("Agent", { exact: true })).toHaveValue(
    "claude-desktop",
  );
  await page.getByLabel("Agent", { exact: true }).selectOption("claude-code");
  const guide = page.getByRole("region", { name: "本机 CLI 接入指引" });
  await expect(guide.locator(".cli-command").last()).toHaveText(
    "cpa profile restore",
  );
  await expect(guide).toContainText("--settings '/path/to/settings.json'");
  await expect(guide).not.toContainText("--config-dir");
  await page.getByRole("button", { name: "保存映射", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: "下游 Profiles" }),
  ).toBeVisible();
  expect(patches[1].agent).toBe("claude-code");
});

for (const [agent, label] of [
  ["claude-code", "Claude Code"],
  ["claude-desktop", "Claude Desktop"],
]) {
  test(`${label} 受管时禁止切换 Agent 和删除，仍可保存模型映射`, async ({
    page,
  }) => {
    const state = fixture();
    state.profiles.daily.agent = agent;
    state.profiles.daily.managed = true;
    let patch: any;
    await page.route("**/admin/api/**", async (route) => {
      const request = route.request();
      if (request.url().endsWith("/oauth/session")) {
        await route.fulfill({ json: null });
      } else if (request.method() === "PATCH") {
        patch = request.postDataJSON();
        await route.fulfill({
          json: { saved: true, active: true, revision: 12 },
        });
      } else await route.fulfill({ json: state });
    });
    await connect(page);
    await page.getByRole("button", { name: "Profiles", exact: false }).click();
    await expect(
      page.getByText(`${label} 受管`, { exact: true }),
    ).toBeVisible();
    await expect(
      page.getByRole("button", { name: "删除 profile daily", exact: true }),
    ).toBeDisabled();
    await page.getByRole("button", { name: "编辑映射 daily" }).click();
    await expect(page.getByLabel("Agent", { exact: true })).toBeDisabled();
    await expect(page.getByLabel("Agent", { exact: true })).toHaveValue(agent);
    await expect(
      page.getByText(
        "此 profile 的服务器配置受管。先恢复对应配置，再更改 Agent。",
        { exact: true },
      ),
    ).toBeVisible();
    await page
      .getByLabel("公开模型 ID", { exact: true })
      .first()
      .fill("managed-opus");
    await page.getByRole("button", { name: "保存映射", exact: true }).click();
    await expect(
      page.getByRole("heading", { name: "下游 Profiles" }),
    ).toBeVisible();
    expect(patch.agent).toBe(agent);
    expect(patch.models.opus.public_model).toBe("managed-opus");
  });
}

test("认证失效会清空工作区并返回登录页", async ({ page }) => {
  let denied = false;
  await page.route("**/admin/api/**", (route) =>
    route.fulfill(
      denied
        ? { status: 401, json: { error: "令牌已失效" } }
        : { json: fixture() },
    ),
  );
  await connect(page);
  denied = true;
  await page.getByRole("button", { name: "刷新状态" }).click();
  await expect(page.getByLabel("管理令牌", { exact: true })).toHaveValue("");
  await expect(
    page.getByRole("heading", { name: "上游连接", exact: true }),
  ).toHaveCount(0);
  await expect(page.getByRole("alert")).toContainText("管理令牌无效或已失效");
});

test("桌面与窄屏布局、抽屉和弹层遵守实际 CSP", async ({ page }) => {
  const errors: string[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  page.on("console", (message) => {
    if (message.type() === "error") errors.push(message.text());
  });
  await page.route("**/admin/api/**", (route) =>
    route.fulfill({
      json: route.request().url().endsWith("/oauth/session") ? null : fixture(),
    }),
  );
  await connect(page);
  await page.screenshot({
    path: "test-results/connections-desktop.png",
    fullPage: true,
  });
  await page.getByRole("button", { name: "添加连接", exact: true }).click();
  await expect(page.getByRole("dialog")).toBeVisible();
  await page.screenshot({
    path: "test-results/connection-sheet.png",
    fullPage: true,
  });
  await page.getByRole("button", { name: "取消", exact: true }).click();
  await page.getByRole("button", { name: "Profiles", exact: false }).click();
  await page.getByRole("button", { name: "编辑映射 daily" }).click();
  await page.screenshot({
    path: "test-results/profile-desktop.png",
    fullPage: true,
  });
  for (const width of [768, 390, 320]) {
    await page.setViewportSize({ width, height: 900 });
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= window.innerWidth,
      ),
    ).toBe(true);
    await expect(
      page.getByRole("button", { name: "保存映射", exact: true }),
    ).toBeVisible();
  }
  await page.screenshot({
    path: "test-results/profile-mobile.png",
    fullPage: true,
  });
  expect(errors).toEqual([]);
});

test("API key 连接仅保存勾选模型、保留凭证及删除失败反馈", async ({ page }) => {
  const writes: { method: string; body: unknown }[] = [];
  await page.route("**/admin/api/**", async (route) => {
    if (route.request().url().endsWith("/oauth/session")) {
      await route.fulfill({ json: null });
      return;
    }
    const req = route.request();
    if (req.method() === "GET") {
      await route.fulfill({ json: fixture() });
    } else if (req.method() === "DELETE") {
      writes.push({ method: req.method(), body: null });
      await route.fulfill({
        status: 409,
        json: { error: "连接仍被 profile 引用" },
      });
    } else {
      writes.push({ method: req.method(), body: req.postDataJSON() });
      await route.fulfill({
        json: { saved: true, active: true, revision: 12 },
      });
    }
  });
  await connect(page);
  await page.getByRole("button", { name: "编辑连接 work-api" }).click();
  await expect(
    page.getByRole("checkbox", { name: "启用模型 large", exact: true }),
  ).toBeChecked();
  await page
    .getByRole("checkbox", { name: "启用模型 fast", exact: true })
    .uncheck();
  await page.getByRole("button", { name: "保存连接", exact: true }).click();
  expect(writes).toEqual([
    {
      method: "PATCH",
      body: {
        baseURL: "https://upstream.example/v1",
        models: [{ id: "large", name: "推理模型" }],
      },
    },
  ]);
  await page.getByRole("button", { name: "删除连接 work-api" }).click();
  await page.getByRole("button", { name: "确认删除", exact: true }).click();
  await expect(page.getByRole("alertdialog")).toBeVisible();
  await expect(page.getByRole("alertdialog").getByRole("alert")).toContainText(
    "连接仍被 profile 引用",
  );
  await page.getByRole("button", { name: "取消", exact: true }).click();
  await expect(
    page.getByRole("button", { name: "编辑连接 work-api" }),
  ).toBeVisible();
  expect(writes[1]).toEqual({ method: "DELETE", body: null });
});

test("放弃编辑需确认，取消放弃后保留映射草稿", async ({ page }) => {
  await page.route("**/admin/api/**", (route) =>
    route.fulfill({
      json: route.request().url().endsWith("/oauth/session") ? null : fixture(),
    }),
  );
  await connect(page);
  await page.getByRole("button", { name: "Profiles", exact: false }).click();
  await page.getByRole("button", { name: "编辑映射 daily" }).click();
  const opus = page.getByRole("region", { name: "OPUS 映射" });
  await opus.getByLabel("公开模型 ID").fill("draft-opus");
  page.once("dialog", (dialog) => dialog.dismiss());
  await page.getByRole("button", { name: "返回 Profiles" }).click();
  await expect(opus.getByLabel("公开模型 ID")).toHaveValue("draft-opus");
  page.once("dialog", (dialog) => dialog.accept());
  await page.getByRole("button", { name: "返回 Profiles" }).click();
  await expect(
    page.getByRole("heading", { name: "下游 Profiles" }),
  ).toBeVisible();
});

test("新模型默认勾选，取消后只保存所选模型", async ({ page }) => {
  const discoveries: unknown[] = [];
  const creates: unknown[] = [];
  await page.route("**/admin/api/**", async (route) => {
    if (route.request().url().endsWith("/oauth/session")) {
      await route.fulfill({ json: null });
      return;
    }
    const req = route.request();
    if (req.method() === "GET") await route.fulfill({ json: fixture() });
    else if (req.url().endsWith("/connections/test")) {
      discoveries.push(req.postDataJSON());
      await route.fulfill({
        json: {
          models: [
            { id: "auto-large", name: "自动发现模型" },
            { id: "auto-fast" },
          ],
        },
      });
    } else {
      creates.push(req.postDataJSON());
      await route.fulfill({
        json: { saved: true, active: true, revision: 12 },
      });
    }
  });
  await connect(page);
  await page.getByRole("button", { name: "添加连接", exact: true }).click();
  await expect(page.getByLabel("模型 ID 1", { exact: true })).toHaveCount(0);
  await expect(
    page.getByRole("button", { name: "保存连接", exact: true }),
  ).toBeDisabled();
  await page
    .getByLabel("API 根地址", { exact: true })
    .fill("https://discover.example/v1");
  await page.getByLabel("API key", { exact: true }).fill("mock-directory-key");
  await page
    .getByRole("button", { name: "测试连接并获取模型", exact: true })
    .click();
  await expect(page.getByRole("region", { name: "可用模型" })).toContainText(
    "auto-large",
  );
  await expect(page.getByRole("region", { name: "可用模型" })).toContainText(
    "自动发现模型",
  );
  await page.getByLabel("名称", { exact: true }).fill("auto-connection");
  await expect(
    page.getByRole("button", { name: "保存连接", exact: true }),
  ).toBeEnabled();
  await page.getByLabel("协议", { exact: true }).selectOption("responses");
  await expect(
    page.getByRole("button", { name: "保存连接", exact: true }),
  ).toBeDisabled();
  await page
    .getByRole("button", { name: "测试连接并获取模型", exact: true })
    .click();
  await expect(
    page.getByRole("button", { name: "保存连接", exact: true }),
  ).toBeEnabled();
  await page
    .getByLabel("API 根地址", { exact: true })
    .fill("https://new-discover.example/v1");
  await expect(
    page.getByRole("button", { name: "保存连接", exact: true }),
  ).toBeDisabled();
  await page
    .getByRole("button", { name: "测试连接并获取模型", exact: true })
    .click();
  await expect(
    page.getByRole("button", { name: "保存连接", exact: true }),
  ).toBeEnabled();
  await page
    .getByLabel("API key", { exact: true })
    .fill("new-mock-directory-key");
  await expect(
    page.getByRole("button", { name: "保存连接", exact: true }),
  ).toBeDisabled();
  await page
    .getByRole("button", { name: "测试连接并获取模型", exact: true })
    .click();
  await expect(
    page.getByRole("button", { name: "保存连接", exact: true }),
  ).toBeEnabled();
  expect(creates).toEqual([]);
  expect(discoveries).toHaveLength(4);
  expect(discoveries[3]).toEqual({
    protocol: "responses",
    baseURL: "https://new-discover.example/v1",
    apiKey: "new-mock-directory-key",
  });
  await expect(
    page.getByRole("checkbox", { name: "启用模型 auto-fast", exact: true }),
  ).toBeChecked();
  await page
    .getByRole("checkbox", { name: "启用模型 auto-fast", exact: true })
    .uncheck();
  await expect(
    page.getByRole("checkbox", { name: "启用模型 auto-large", exact: true }),
  ).toBeChecked();
  await page.screenshot({
    path: "test-results/connection-model-discovery.png",
    fullPage: true,
  });
  await page.getByRole("button", { name: "保存连接", exact: true }).click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  expect(creates).toEqual([
    {
      name: "auto-connection",
      protocol: "responses",
      baseURL: "https://new-discover.example/v1",
      apiKey: "new-mock-directory-key",
      models: [{ id: "auto-large", name: "自动发现模型" }],
    },
  ]);
});

test("重新测试失败后不保留旧模型，也不会创建连接或退出管理", async ({
  page,
}) => {
  let failed = false;
  let creates = 0;
  await page.route("**/admin/api/**", async (route) => {
    if (route.request().url().endsWith("/oauth/session")) {
      await route.fulfill({ json: null });
      return;
    }
    if (route.request().method() === "GET")
      await route.fulfill({ json: fixture() });
    else if (route.request().url().endsWith("/connections/test")) {
      await route.fulfill(
        failed
          ? {
              status: 400,
              json: { error: "模型目录返回 HTTP 401，请检查 API key" },
            }
          : { json: { models: [{ id: "available" }] } },
      );
    } else {
      creates++;
      await route.fulfill({ status: 500 });
    }
  });
  await connect(page);
  await page.getByRole("button", { name: "添加连接", exact: true }).click();
  await page
    .getByLabel("API 根地址", { exact: true })
    .fill("https://discover.example/v1");
  await page.getByLabel("API key", { exact: true }).fill("mock-directory-key");
  await page
    .getByRole("button", { name: "测试连接并获取模型", exact: true })
    .click();
  await expect(page.getByRole("region", { name: "可用模型" })).toContainText(
    "available",
  );
  failed = true;
  await page.getByRole("button", { name: "重新测试连接", exact: true }).click();
  await expect(page.getByRole("alert")).toContainText("模型目录返回 HTTP 401");
  await expect(
    page.getByRole("region", { name: "可用模型" }),
  ).not.toContainText("available");
  await expect(
    page.getByRole("button", { name: "保存连接", exact: true }),
  ).toBeDisabled();
  await expect(
    page.getByRole("heading", { name: "上游连接", exact: true }),
  ).toBeHidden();
  await expect(page.getByLabel("管理令牌", { exact: true })).toHaveCount(0);
  expect(creates).toBe(0);
});

test("已保存连接使用各自凭证获取目录，OAuth 不接收 API_KEY", async ({
  page,
}) => {
  let discovery: unknown;
  await page.route("**/admin/api/**", async (route) => {
    if (route.request().url().endsWith("/oauth/session")) {
      await route.fulfill({ json: null });
      return;
    }
    if (route.request().method() === "GET")
      await route.fulfill({ json: fixture() });
    else {
      discovery = route.request().postDataJSON();
      await route.fulfill({ json: { models: [{ id: "refreshed-model" }] } });
    }
  });
  await connect(page);
  await page.getByRole("button", { name: "编辑连接 work-api" }).click();
  await page
    .getByRole("button", { name: "测试连接并获取模型", exact: true })
    .click();
  await expect(
    page.getByRole("checkbox", {
      name: "启用模型 refreshed-model",
      exact: true,
    }),
  ).toBeChecked();
  await expect(
    page.getByRole("checkbox", { name: "启用模型 large", exact: true }),
  ).toBeChecked();
  await expect(page.getByRole("region", { name: "可用模型" })).toContainText(
    "本次目录未返回",
  );
  expect(discovery).toEqual({
    connection: "work-api",
    protocol: "chat-completions",
    baseURL: "https://upstream.example/v1",
  });
  page.once("dialog", (dialog) => dialog.accept());
  await page.getByRole("button", { name: "取消", exact: true }).click();
  await page.getByRole("button", { name: "编辑连接 codex-personal" }).click();
  await expect(
    page.getByRole("button", { name: "测试连接并获取模型", exact: true }),
  ).toBeVisible();
  await page
    .getByRole("button", { name: "测试连接并获取模型", exact: true })
    .click();
  await expect(
    page.getByRole("checkbox", {
      name: "启用模型 refreshed-model",
      exact: true,
    }),
  ).toBeChecked();
  expect(discovery).toEqual({
    connection: "codex-personal",
    protocol: "responses",
    baseURL: "https://chatgpt.com/backend-api/codex",
  });
});

test("模型搜索与批量选择，重新发现只默认启用新模型", async ({ page }) => {
  let discoveries = 0;
  await page.route("**/admin/api/**", async (route) => {
    if (route.request().url().endsWith("/oauth/session")) {
      await route.fulfill({ json: null });
      return;
    }
    if (route.request().method() === "GET")
      await route.fulfill({ json: fixture() });
    else {
      discoveries++;
      await route.fulfill({
        json: {
          models: [
            { id: "large" },
            { id: "fast" },
            { id: "new", name: "新增模型" },
            ...(discoveries > 1 ? [{ id: "next", name: "Next model" }] : []),
          ],
        },
      });
    }
  });
  await connect(page);
  await page.getByRole("button", { name: "编辑连接 work-api" }).click();
  await page
    .getByRole("button", { name: "测试连接并获取模型", exact: true })
    .click();
  await expect(
    page.getByRole("checkbox", { name: "启用模型 new", exact: true }),
  ).toBeChecked();
  await page
    .getByRole("checkbox", { name: "启用模型 fast", exact: true })
    .uncheck();
  await page
    .getByRole("checkbox", { name: "启用模型 new", exact: true })
    .uncheck();
  await page.getByRole("button", { name: "重新测试连接", exact: true }).click();
  await expect(
    page.getByRole("checkbox", { name: "启用模型 next", exact: true }),
  ).toBeChecked();
  await expect(
    page.getByRole("checkbox", { name: "启用模型 fast", exact: true }),
  ).not.toBeChecked();
  await expect(
    page.getByRole("checkbox", { name: "启用模型 new", exact: true }),
  ).not.toBeChecked();
  await page.getByRole("button", { name: "清空选择", exact: true }).click();
  await expect(
    page.getByRole("button", { name: "保存连接", exact: true }),
  ).toBeDisabled();
  await page.getByLabel("搜索可用模型").fill("Next");
  await expect(page.getByRole("checkbox")).toHaveCount(1);
  await page.getByRole("button", { name: "全选当前结果", exact: true }).click();
  await expect(
    page.getByRole("button", { name: "保存连接", exact: true }),
  ).toBeEnabled();
  await page.getByLabel("搜索可用模型").fill("");
  await expect(
    page.getByRole("checkbox", { name: "启用模型 large", exact: true }),
  ).not.toBeChecked();
  await expect(
    page.getByRole("checkbox", { name: "启用模型 next", exact: true }),
  ).toBeChecked();
  await expect(page.getByRole("region", { name: "可用模型" })).toContainText(
    "已选 1 / 4",
  );
});

test("保存选择后 profile 只能选择连接启用的模型", async ({ page }) => {
  const state = fixture();
  let saved = false;
  await page.route("**/admin/api/**", async (route) => {
    if (route.request().url().endsWith("/oauth/session")) {
      await route.fulfill({ json: null });
      return;
    }
    if (route.request().method() === "GET")
      await route.fulfill({ json: state });
    else {
      expect(route.request().postDataJSON().models).toEqual([
        { id: "large", name: "推理模型" },
      ]);
      state.connections["work-api"].models = [
        { id: "large", name: "推理模型" },
      ];
      saved = true;
      await route.fulfill({
        json: { saved: true, active: true, revision: 12 },
      });
    }
  });
  await connect(page);
  await page.getByRole("button", { name: "编辑连接 work-api" }).click();
  await page
    .getByRole("checkbox", { name: "启用模型 fast", exact: true })
    .uncheck();
  await page.getByRole("button", { name: "保存连接", exact: true }).click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  expect(saved).toBe(true);
  await page.getByRole("button", { name: "Profiles", exact: false }).click();
  await page.getByRole("button", { name: "编辑映射 daily" }).click();
  await page
    .getByRole("region", { name: "OPUS 映射" })
    .getByRole("combobox", { name: "上游模型", exact: true })
    .click();
  await expect(
    page.getByRole("option", { name: "推理模型 large" }),
  ).toBeVisible();
  await expect(page.getByRole("option", { name: "快速模型 fast" })).toHaveCount(
    0,
  );
});

test("网页创建 OAuth、展示设备码并可取消，模型服务持续运行", async ({
  page,
}) => {
  const state = { ...fixture(), oauthAvailable: true };
  type Job = {
    id: string;
    name: string;
    phase: string;
    expiresAt: string;
    code?: string;
    verificationURL?: string;
    message?: string;
  };
  let job: Job | null = null;
  let login: unknown;
  let cancelled = false;
  await page.route("**/admin/api/**", async (route) => {
    const req = route.request();
    if (req.url().endsWith("/oauth/authorize")) {
      login = req.postDataJSON();
      job = {
        id: "mock-oauth",
        name: "new-codex",
        phase: "waiting",
        code: "ABCD-1234",
        verificationURL: "https://auth.openai.com/codex/device",
        expiresAt: new Date(Date.now() + 900000).toISOString(),
      };
      await route.fulfill({ status: 202, json: job });
    } else if (req.method() === "DELETE") {
      cancelled = true;
      job = {
        ...job!,
        phase: "cancelled",
        code: undefined,
        verificationURL: undefined,
        message: "授权已取消或超时，原有模型服务持续运行。",
      };
      await route.fulfill({ json: { cancelled: true } });
    } else if (req.url().endsWith("/oauth/session"))
      await route.fulfill({ json: job });
    else await route.fulfill({ json: state });
  });
  await connect(page);
  await page.getByRole("button", { name: "添加连接", exact: true }).click();
  await page
    .getByLabel("认证方式", { exact: true })
    .selectOption("codex-oauth");
  await expect(page.getByLabel("API key", { exact: true })).toHaveCount(0);
  await expect(page.getByLabel("协议", { exact: true })).toHaveCount(0);
  await expect(page.getByLabel("API 根地址", { exact: true })).toHaveCount(0);
  await expect(
    page.getByRole("button", { name: "保存 Connection", exact: true }),
  ).toBeDisabled();
  await page.getByLabel("名称", { exact: true }).fill("new-codex");
  await expect(page.getByLabel("模型 ID 1", { exact: true })).toHaveCount(0);
  await expect(
    page.getByLabel("认证方式", { exact: true }).locator("option"),
  ).toHaveText(["API_KEY", "OAuth"]);
  await page.screenshot({
    path: "test-results/oauth-create.png",
    fullPage: true,
  });
  await page
    .getByRole("button", { name: "登录并获取模型", exact: true })
    .click();
  await expect(page.getByLabel("设备授权码", { exact: true })).toHaveValue(
    "ABCD-1234",
  );
  expect(login).toEqual({ name: "new-codex" });
  await expect(
    page.getByRole("link", { name: "打开 OpenAI 官方授权页面" }),
  ).toHaveAttribute("href", "https://auth.openai.com/codex/device");
  await page.keyboard.press("Escape");
  await expect(page.getByLabel("设备授权码", { exact: true })).toBeVisible();
  await page.screenshot({
    path: "test-results/oauth-device-code.png",
    fullPage: true,
  });
  await page.getByRole("button", { name: "取消登录", exact: true }).click();
  await expect(page.getByRole("dialog")).toContainText("模型服务持续运行");
  await expect(page.getByLabel("设备授权码", { exact: true })).toHaveCount(0);
  expect(cancelled).toBe(true);
  await page.getByRole("button", { name: "完成", exact: true }).click();
  await expect(
    page.getByRole("button", { name: "保存 Connection", exact: true }),
  ).toBeDisabled();
  page.once("dialog", (dialog) => dialog.accept());
  await page.getByRole("button", { name: "取消", exact: true }).click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
});

test("重新进入页面恢复 OAuth 进度，已有连接重登录先确认", async ({ page }) => {
  const state = { ...fixture(), oauthAvailable: true };
  let pending = true;
  let calls = 0;
  await page.route("**/admin/api/**", async (route) => {
    const req = route.request();
    if (req.url().endsWith("/oauth/session"))
      await route.fulfill({
        json: pending
          ? {
              id: "existing-job",
              name: "codex-personal",
              phase: "waiting",
              code: "WXYZ-5678",
              verificationURL: "https://auth.openai.com/codex/device",
              expiresAt: new Date(Date.now() + 900000).toISOString(),
            }
          : null,
      });
    else if (req.url().endsWith("/oauth/login")) {
      calls++;
      expect(req.postDataJSON()).toEqual({ name: "codex-personal" });
      await route.fulfill({
        status: 202,
        json: {
          id: "next-job",
          name: "codex-personal",
          phase: "succeeded",
          expiresAt: new Date().toISOString(),
          message: "OAuth 登录已完成，模型服务持续运行。",
        },
      });
    } else await route.fulfill({ json: state });
  });
  await connect(page, false);
  await expect(page.getByLabel("设备授权码", { exact: true })).toHaveValue(
    "WXYZ-5678",
  );
  pending = false;
  await page.reload();
  await page.getByLabel("管理令牌", { exact: true }).fill("test-admin-token");
  await page.getByRole("button", { name: "连接网关", exact: true }).click();
  await page
    .getByRole("button", { name: "重新登录 codex-personal", exact: true })
    .click();
  await expect(page.getByRole("alertdialog")).toContainText("已有请求继续完成");
  expect(calls).toBe(0);
  await page.getByRole("button", { name: "确认登录", exact: true }).click();
  expect(calls).toBe(1);
});

test("OAuth 登录成功自动展示默认全选模型，保存后只保留所选", async ({
  page,
}) => {
  const state = { ...fixture(), oauthAvailable: true };
  const models = [
    { id: "oauth-coding", name: "账号编程模型" },
    { id: "oauth-fast", name: "快速模型" },
  ];
  let job: unknown = null;
  let saved: unknown;
  await page.route("**/admin/api/**", async (route) => {
    const req = route.request();
    if (req.url().endsWith("/oauth/login")) {
      expect(req.postDataJSON()).toEqual({ name: "codex-personal" });
      state.connections["codex-personal"].models = models;
      job = {
        id: "success-job",
        name: "codex-personal",
        phase: "succeeded",
        models,
        expiresAt: new Date().toISOString(),
        message: "OAuth 登录已完成，模型服务持续运行。",
      };
      await route.fulfill({ status: 202, json: job });
    } else if (req.url().endsWith("/oauth/session"))
      await route.fulfill({ json: job });
    else if (req.method() === "PATCH") {
      saved = req.postDataJSON();
      await route.fulfill({
        json: { saved: true, active: true, revision: 13 },
      });
    } else await route.fulfill({ json: state });
  });
  await connect(page);
  await page
    .getByRole("button", { name: "重新登录 codex-personal", exact: true })
    .click();
  await page.getByRole("button", { name: "确认登录", exact: true }).click();
  await expect(page.getByRole("dialog")).toContainText("默认全选");
  await page
    .getByRole("button", { name: "查看并选择模型", exact: true })
    .click();
  await expect(page.getByRole("region", { name: "可用模型" })).toContainText(
    "已选 2 / 2",
  );
  await expect(
    page.getByRole("checkbox", { name: "启用模型 oauth-coding", exact: true }),
  ).toBeChecked();
  await expect(
    page.getByRole("checkbox", { name: "启用模型 oauth-fast", exact: true }),
  ).toBeChecked();
  await expect(page.getByLabel("模型 ID 1", { exact: true })).toHaveCount(0);
  await page
    .getByRole("checkbox", { name: "启用模型 oauth-fast", exact: true })
    .uncheck();
  await page.getByRole("button", { name: "保存连接", exact: true }).click();
  expect(saved).toEqual({
    models: [{ id: "oauth-coding", name: "账号编程模型" }],
  });
});

test("OAuth 先登录选择模型，确认后才创建 connection，刷新可继续草稿", async ({
  page,
}) => {
  const state = { ...fixture(), oauthAvailable: true };
  const models = [
    { id: "draft-coding", name: "编程模型" },
    { id: "draft-fast", name: "快速模型" },
  ];
  let job: unknown = null;
  let creates = 0;
  let chosen: unknown;
  await page.route("**/admin/api/**", async (route) => {
    const req = route.request();
    if (req.url().endsWith("/oauth/authorize")) {
      expect(req.postDataJSON()).toEqual({ name: "draft-codex" });
      job = {
        id: "draft-job",
        name: "draft-codex",
        draft: true,
        phase: "selecting",
        models,
        expiresAt: new Date(Date.now() + 900000).toISOString(),
        message: "登录成功，请选择模型后创建 connection。",
      };
      await route.fulfill({ status: 202, json: job });
    } else if (req.url().endsWith("/oauth/session/draft-job/connection")) {
      creates++;
      chosen = req.postDataJSON();
      Object.assign(state.connections, {
        "draft-codex": {
          ...state.connections["codex-personal"],
          name: "draft-codex",
          models: [models[0]],
        },
      });
      job = {
        id: "draft-job",
        name: "draft-codex",
        phase: "succeeded",
        draft: false,
        expiresAt: new Date().toISOString(),
        message: "connection 已创建，所选模型已启用。",
      };
      await route.fulfill({ status: 202, json: job });
    } else if (req.url().endsWith("/oauth/session"))
      await route.fulfill({ json: job });
    else await route.fulfill({ json: state });
  });
  await connect(page);
  await page.getByRole("button", { name: "添加连接", exact: true }).click();
  await page
    .getByLabel("认证方式", { exact: true })
    .selectOption("codex-oauth");
  await page.getByLabel("名称", { exact: true }).fill("draft-codex");
  await expect(
    page.getByRole("button", { name: "保存 Connection", exact: true }),
  ).toBeDisabled();
  await page
    .getByRole("button", { name: "登录并获取模型", exact: true })
    .click();
  await expect(page.getByRole("region", { name: "可用模型" })).toContainText(
    "已选 2 / 2",
  );
  await expect(
    page.getByRole("button", { name: "登录并获取模型", exact: true }),
  ).toBeDisabled();
  await expect(
    page.getByRole("button", { name: "保存 Connection", exact: true }),
  ).toBeEnabled();
  await expect(page.getByLabel("协议", { exact: true })).toHaveCount(0);
  await expect(page.getByLabel("API 根地址", { exact: true })).toHaveCount(0);
  expect(creates).toBe(0);
  expect(Object.keys(state.connections)).not.toContain("draft-codex");
  await page.reload();
  await page.getByLabel("管理令牌", { exact: true }).fill("test-admin-token");
  await page.getByRole("button", { name: "连接网关", exact: true }).click();
  await expect(
    page.getByRole("checkbox", { name: "启用模型 draft-fast", exact: true }),
  ).toBeChecked();
  await page
    .getByRole("checkbox", { name: "启用模型 draft-fast", exact: true })
    .uncheck();
  await page.screenshot({
    path: "test-results/oauth-before-create.png",
    fullPage: true,
  });
  expect(creates).toBe(0);
  await page
    .getByRole("button", { name: "保存 Connection", exact: true })
    .click();
  await expect(page.getByRole("dialog")).toContainText("connection 已创建");
  expect(creates).toBe(1);
  expect(chosen).toEqual({ models: [models[0]] });
  await page.getByRole("button", { name: "完成", exact: true }).click();
  await expect(
    page.getByRole("button", { name: "编辑连接 draft-codex", exact: true }),
  ).toBeVisible();
});

test("OAuth 模型在表单内选择，取消会放弃草稿而不保存 Connection", async ({
  page,
}) => {
  const state = { ...fixture(), oauthAvailable: true };
  let discarded = false;
  let creates = 0;
  let reads = 0;
  const job = {
    id: "discard-draft",
    name: "discard-codex",
    draft: true,
    phase: "selecting",
    models: [{ id: "coding-model" }],
    expiresAt: new Date(Date.now() + 900000).toISOString(),
  };
  await page.route("**/admin/api/**", async (route) => {
    const req = route.request();
    if (req.method() === "DELETE") {
      discarded = true;
      await route.fulfill({ json: { cancelled: true } });
    } else if (req.url().endsWith("/oauth/session")) {
      reads++;
      await route.fulfill({
        json: discarded
          ? {
              ...job,
              phase: "cancelled",
              models: undefined,
              message: "已放弃创建。",
            }
          : job,
      });
    } else if (req.method() === "POST") {
      creates++;
      await route.fulfill({ status: 500 });
    } else await route.fulfill({ json: state });
  });
  await connect(page, false);
  await expect(
    page.getByRole("button", { name: "保存 Connection", exact: true }),
  ).toBeEnabled();
  await expect(
    page.getByRole("button", { name: "登录并获取模型", exact: true }),
  ).toBeVisible();
  await page.getByRole("button", { name: "取消", exact: true }).click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await expect.poll(() => reads).toBeGreaterThan(1);
  await expect(page.getByRole("dialog")).toHaveCount(0);
  expect(discarded).toBe(true);
  expect(creates).toBe(0);
});

test("本地免令牌自动连接并可编辑连接", async ({ page }) => {
  await page.route("**/ui/", async (route) => {
    const response = await route.fetch();
    await route.fulfill({
      response,
      body: (await response.text()).replace(
        "</head>",
        '<meta name="cpa-local-admin" content="true"></head>',
      ),
    });
  });
  const writes: unknown[] = [];
  await page.route("**/admin/api/**", async (route) => {
    expect(route.request().headers().authorization).toBeUndefined();
    if (route.request().url().endsWith("/oauth/session")) {
      await route.fulfill({ json: null });
    } else if (route.request().method() === "GET") {
      await route.fulfill({ json: fixture() });
    } else {
      writes.push(route.request().postDataJSON());
      await route.fulfill({
        json: { saved: true, active: true, revision: 12 },
      });
    }
  });
  await page.goto("/ui/");
  await expect(
    page.getByRole("heading", { name: "上游连接", exact: true }),
  ).toBeVisible();
  await expect(page.getByLabel("管理令牌", { exact: true })).toHaveCount(0);
  await expect(
    page.getByRole("button", { name: "退出管理", exact: true }),
  ).toHaveCount(0);
  await page.getByRole("button", { name: "编辑连接 work-api" }).click();
  await page.getByRole("button", { name: "保存连接", exact: true }).click();
  await expect.poll(() => writes.length).toBe(1);
  await page.reload();
  await expect(
    page.getByRole("heading", { name: "上游连接", exact: true }),
  ).toBeVisible();
});

test("大量模型保持列表紧凑，完整目录可按名称与 ID 搜索", async ({ page }) => {
  const state = fixture();
  state.connections["work-api"].models = Array.from({ length: 92 }, (_, i) => ({
    id: "model-" + i,
    name: "测试模型 " + i,
  }));
  await page.route("**/admin/api/**", async (route) => {
    await route.fulfill({
      json: route.request().url().endsWith("/oauth/session") ? null : state,
    });
  });
  await connect(page);
  const row = page.getByRole("row").filter({
    has: page.getByRole("button", { name: "编辑连接 work-api", exact: true }),
  });
  await expect(row).toContainText("92 个模型");
  await expect(row).not.toContainText("测试模型 91");
  await expect(row.locator(".model-summary")).toHaveCSS(
    "-webkit-line-clamp",
    "2",
  );
  await page
    .getByRole("button", { name: "查看 work-api 的全部模型", exact: true })
    .click();
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByRole("listitem")).toHaveCount(92);
  await page.getByLabel("搜索可用模型").fill("MODEL-91");
  await expect(dialog.getByRole("listitem")).toHaveCount(1);
  await expect(dialog).toContainText("测试模型 91");
  await page.getByLabel("搜索可用模型").fill("测试模型 87");
  await expect(dialog.getByRole("listitem")).toHaveCount(1);
  await page.getByLabel("搜索可用模型").fill("无此模型");
  await expect(dialog).toContainText("没有匹配的模型");
  await page.keyboard.press("Escape");
  await page
    .getByRole("button", { name: "查看 work-api 的全部模型", exact: true })
    .click();
  await expect(page.getByRole("dialog").getByRole("listitem")).toHaveCount(92);
});

test("Responses 连接及 profile 显示模型能力，不承诺输出预算和 1M 容量", async ({
  page,
}) => {
  const base = fixture();
  const common = {
    outputLimit: "not-enforced",
    contextWindow: "unverified",
    warnings: [
      "输出预算不执行，不能用 max_tokens 控制消耗。",
      "上下文容量未经上游验证，[1m] 不会扩容。",
      "托管工具仅部分映射，不代表原生服务端执行。",
    ],
  };
  const workspace = {
    ...base,
    connections: {
      ...base.connections,
      "codex-personal": {
        ...base.connections["codex-personal"],
        models: [{ id: "gpt-6.1-sol" }, { id: "unknown-model" }],
        compatibility: {
          "gpt-6.1-sol": {
            ...common,
            reasoning: "effort-mapping",
            efforts: ["low", "high"],
            thinkingOff: "lowest-effort",
            warnings: [
              ...common.warnings,
              "关闭 thinking 会降到最低支持档，不能保证完全关闭内部推理。",
            ],
          },
          "unknown-model": {
            ...common,
            reasoning: "unavailable",
            efforts: [],
            thinkingOff: "unverified",
            warnings: [
              ...common.warnings,
              "SDK 未识别推理能力，显式 thinking 或 effort 会被拒绝。",
            ],
          },
        },
      },
    },
  };
  let writes = 0;
  await page.route("**/admin/api/**", async (route) => {
    if (route.request().url().endsWith("/oauth/session")) {
      await route.fulfill({ json: null });
      return;
    }
    if (route.request().method() !== "GET") writes++;
    await route.fulfill({ json: workspace });
  });
  await connect(page);
  await page
    .getByRole("button", {
      name: "查看 codex-personal 的全部模型",
      exact: true,
    })
    .click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toContainText("输出上限不执行，上下文容量未验证");
  const unknown = dialog
    .locator(".connection-model-list > li")
    .filter({ hasText: "unknown-model" });
  await unknown.locator("summary").click();
  await expect(unknown).toContainText(
    "SDK 未识别推理能力，显式 thinking 或 effort 会被拒绝。",
  );
  await page.keyboard.press("Escape");
  await page.getByRole("button", { name: "Profiles", exact: false }).click();
  await page.getByRole("button", { name: "编辑映射 daily" }).click();
  const opus = page.getByRole("region", { name: "OPUS 映射" });
  await opus
    .getByLabel("上游连接", { exact: true })
    .selectOption("codex-personal");
  await opus.locator(".compatibility-notice summary").click();
  await expect(opus).toContainText("关闭 thinking 会降到最低支持档");
  await opus.getByRole("combobox", { name: "上游模型", exact: true }).click();
  await page
    .getByRole("option", { name: "unknown-model", exact: true })
    .click();
  await expect(opus).toContainText(
    "SDK 未识别推理能力，显式 thinking 或 effort 会被拒绝。",
  );
  expect(writes).toBe(0);
});

test("独立编辑必填 Fable 映射并保留已有四档", async ({ page }) => {
  const state = fixture();
  let patch: any;
  await page.route("**/admin/api/**", async (route) => {
    if (route.request().url().endsWith("/oauth/session")) {
      await route.fulfill({ json: null });
    } else if (route.request().method() === "GET") {
      await route.fulfill({ json: state });
    } else {
      patch = route.request().postDataJSON();
      Object.assign(state.profiles.daily.models, patch.models);
      await route.fulfill({
        json: { saved: true, active: true, revision: 12 },
      });
    }
  });
  await connect(page);
  await page.getByRole("button", { name: "Profiles", exact: false }).click();
  await page.getByRole("button", { name: "编辑映射 daily" }).click();
  const fable = page.getByRole("region", { name: "FABLE 映射" });
  await fable
    .getByLabel("公开模型 ID", { exact: true })
    .fill("claude-fable-test");
  await fable
    .getByLabel("上游连接", { exact: true })
    .selectOption("team-messages");
  await page.getByRole("button", { name: "保存映射", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: "下游 Profiles" }),
  ).toBeVisible();
  expect(patch.models.fable).toEqual({
    public_model: "claude-fable-test",
    connection: "team-messages",
    target_model: "coding",
  });
  expect(Object.keys(patch.models).sort()).toEqual([
    "fable",
    "haiku",
    "opus",
    "sonnet",
  ]);
  await expect(page.locator(".profile-summary")).toContainText(
    "claude-fable-test",
  );
  await page.getByRole("button", { name: "编辑映射 daily" }).click();
  await expect(fable.getByLabel("公开模型 ID", { exact: true })).toHaveValue(
    "claude-fable-test",
  );
  await expect(
    fable.getByRole("combobox", { name: "上游模型", exact: true }),
  ).toHaveText("编程模型");
});
