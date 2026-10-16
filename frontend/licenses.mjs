// 根据锁文件收集前端分发依赖许可，重跑时替换同一区段。
import { readFile, readdir, writeFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";
const dir = path.dirname(fileURLToPath(import.meta.url));
const lock = JSON.parse(
  await readFile(path.join(dir, "package-lock.json"), "utf8"),
);
const notices = [];
for (const [location, entry] of Object.entries(lock.packages)) {
  if (!location || (entry.dev && location !== "node_modules/tailwindcss"))
    continue;
  const packageDir = path.join(dir, location);
  const pkg = JSON.parse(
    await readFile(path.join(packageDir, "package.json"), "utf8"),
  );
  const files = (await readdir(packageDir))
    .filter((name) => /^(licen[cs]e|copying)(\.|$)/i.test(name))
    .sort();
  const texts = await Promise.all(
    files.map((file) => readFile(path.join(packageDir, file), "utf8")),
  );
  if (!files.length) {
    texts.push(
      await readFile(path.join(dir, "licenses", pkg.name + ".txt"), "utf8"),
    );
  }
  notices.push(
    "--- " +
      pkg.name +
      " " +
      pkg.version +
      " ---\n\n" +
      texts.join("\n").replace(/\r\n/g, "\n"),
  );
}
const marker =
  "\n--- cpa WebUI 前端依赖许可（由 frontend/licenses.mjs 生成）---\n";
const target = path.join(dir, "../THIRD_PARTY_NOTICES");
const original = (await readFile(target, "utf8")).split(marker)[0].trimEnd();
const shadcn = await readFile(path.join(dir, "licenses/shadcn.txt"), "utf8");
await writeFile(
  target,
  original +
    "\n" +
    marker +
    "\n--- shadcn/ui ---\n\n" +
    shadcn +
    "\n" +
    notices.join("\n\n"),
);
