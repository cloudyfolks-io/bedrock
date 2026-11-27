import { createHash } from "node:crypto";
import { cpSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

// Next's build-time bootstrap must work under Bedrock's script-src 'self'.
// Keep execution order and publish each inline script as a hashed asset.
export function externalizeBootstrap(html: string, write: (name: string, body: string) => void): string {
  return html.replace(/<script([^>]*)>([\s\S]*?)<\/script>/gi, (original, attributes: string, body: string) => {
    if (!body.trim() || /(?:^|\s)src\s*=/i.test(attributes)) return original;
    if (attributes.trim()) throw new Error("Unexpected attributes on a Next.js inline script");
    const hash = createHash("sha256").update(body).digest("hex").slice(0, 24);
    const name = `_next/static/bedrock-bootstrap-${hash}.js`;
    write(name, body);
    return `<script src="/${name}"></script>`;
  });
}

export function publishPanel(source: string, target: string): void {
  const html = readFileSync(join(source, "index.html"), "utf8");
  rmSync(join(target, "_next"), { recursive: true, force: true });
  rmSync(join(target, "assets"), { recursive: true, force: true });
  rmSync(join(target, "brand"), { recursive: true, force: true });
  mkdirSync(target, { recursive: true });
  cpSync(join(source, "_next/static"), join(target, "_next/static"), { recursive: true });
  cpSync(join(source, "brand"), join(target, "brand"), { recursive: true });
  writeFileSync(join(target, "index.html"), externalizeBootstrap(html,
    (name, body) => writeFileSync(join(target, name), body)));
}

if (import.meta.main) {
  publishPanel(
    fileURLToPath(new URL("../out", import.meta.url)),
    fileURLToPath(new URL("../../../../internal/proxy/ui/dist", import.meta.url)),
  );
}
