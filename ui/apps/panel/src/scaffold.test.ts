import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";
import { checkBundle } from "../scripts/checkSize";

function repoFile(path: string): string {
  return readFileSync(resolve(import.meta.dirname, "../../../..", path), "utf8");
}

const buildBoth = "pnpm --filter login build && pnpm --filter panel build";

describe("scaffold", () => {
  it("TestWorkspaceBuildsThePanel", () => {
    expect(repoFile("ui/pnpm-workspace.yaml")).toContain("apps/*");
    expect(JSON.parse(repoFile("ui/apps/panel/package.json")).name).toBe("panel");
    expect(JSON.parse(repoFile("ui/package.json")).scripts.build).toBe(buildBoth);
  });

  it("TestMakeUiTargetBuildsThePanel", () => {
    const makefile = repoFile("Makefile");
    expect(makefile).toContain(buildBoth);
    expect(makefile).toContain("touch internal/authn/server/ui/dist/.keep");
    expect(makefile).toContain("touch internal/proxy/ui/dist/.keep");
    expect(/^\.PHONY:.*\bui\b/m.test(makefile)).toBe(true);
    expect(/^build: ui$/m.test(makefile)).toBe(true);
  });

  it("TestContainerfileCopiesThePanel", () => {
    const containerfile = repoFile("Containerfile");
    expect(containerfile).toContain("pnpm --filter panel build");
    const copyAll = containerfile.indexOf("COPY . .");
    const copyPanel = containerfile.indexOf("COPY --from=ui /src/internal/proxy/ui/dist /src/internal/proxy/ui/dist");
    expect(copyAll).toBeGreaterThan(-1);
    expect(copyPanel).toBeGreaterThan(copyAll);
  });

  it("TestPanelBuildsIntoTheProxyEmbedDirectory", () => {
    const vite = repoFile("ui/apps/panel/vite.config.ts");
    expect(vite).toContain('outDir: "../../../internal/proxy/ui/dist"');
    expect(vite).toContain("emptyOutDir: true");
    const ignore = repoFile(".gitignore");
    expect(ignore).toContain("/internal/proxy/ui/dist/*");
    expect(ignore).toContain("!/internal/proxy/ui/dist/.keep");
  });
});

describe("checkBundle", () => {
  const lazyFiles = [
    { name: "assets/novnc-a.js", bytes: 10 },
    { name: "assets/xterm-b.js", bytes: 10 },
    { name: "assets/uplot-c.js", bytes: 10 },
  ];
  const entry = { name: "assets/index-d.js", bytes: 1000 };

  it("TestCheckBundleRules", () => {
    expect(checkBundle({ entry: entry.name, files: [entry, ...lazyFiles] })).toEqual([]);
    expect(checkBundle({ entry: entry.name, files: [{ ...entry, bytes: 600 * 1024 + 1 }, ...lazyFiles] })).toHaveLength(1);
    expect(checkBundle({ entry: entry.name, files: [entry, ...lazyFiles, { name: "assets/big.bin", bytes: 8 * 1024 * 1024 }] })).toHaveLength(1);
    expect(checkBundle({ entry: entry.name, files: [entry] })).toHaveLength(3);
  });
});
