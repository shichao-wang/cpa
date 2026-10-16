// 仅提供构建后的页面；管理接口由浏览器测试模拟，不读取任何用户状态。
import http from "node:http";
import { readFile } from "node:fs/promises";
import { randomBytes } from "node:crypto";
const resources = new Map([
  ["/ui/", ["index.html", "text/html"]],
  ["/ui/app.js", ["app.js", "text/javascript"]],
  ["/ui/style.css", ["style.css", "text/css"]],
]);
http
  .createServer(async (req, res) => {
    const resource = resources.get(req.url);
    if (!resource) {
      res.writeHead(404).end();
      return;
    }
    try {
      let data = await readFile(
        new URL("../../internal/webui/assets/" + resource[0], import.meta.url),
      );
      const nonce = randomBytes(16).toString("base64");
      const styles =
        resource[0] === "index.html"
          ? "style-src 'self' 'nonce-" + nonce + "'"
          : "style-src 'self'";
      if (resource[0] === "index.html")
        data = Buffer.from(
          data
            .toString()
            .replace(
              "</head>",
              `<meta name="cpa-style-nonce" content="${nonce}"></head>`,
            ),
        );
      res.setHeader(
        "Content-Security-Policy",
        "default-src 'none'; script-src 'self'; " +
          styles +
          "; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'",
      );
      res.setHeader("Content-Type", resource[1]);
      res.writeHead(200).end(data);
    } catch {
      res.writeHead(500).end();
    }
  })
  .listen(19327, "127.0.0.1");
