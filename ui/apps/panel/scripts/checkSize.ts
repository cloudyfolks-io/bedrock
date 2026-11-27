import { readdirSync, readFileSync, statSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

export interface BundleFile {
  name: string;
  bytes: number;
}

export interface BundleInput {
  entry: string | readonly string[];
  startupLimit?: number;
  files: BundleFile[];
  lazy?: readonly string[];
}

const entryLimit = 600 * 1024;
const totalLimit = 8 * 1024 * 1024;
export const lazyChunks = ["novnc", "xterm", "uplot"] as const;

function entryViolations(input: BundleInput): string[] {
  const names = new Set(typeof input.entry === "string" ? [input.entry] : input.entry);
  const size = input.files.reduce((sum, file) => sum + (names.has(file.name) ? file.bytes : 0), 0);
  const limit = input.startupLimit ?? entryLimit;
  return size > limit ? [`entry scripts are ${size} bytes, limit ${limit}`] : [];
}

function totalViolations(input: BundleInput): string[] {
  const total = input.files.reduce((sum, file) => sum + file.bytes, 0);
  return total > totalLimit ? [`dist is ${total} bytes, limit ${totalLimit}`] : [];
}

function lazyViolations(input: BundleInput): string[] {
  const names = new Set(typeof input.entry === "string" ? [input.entry] : input.entry);
  return (input.lazy ?? lazyChunks)
    .filter((marker) => !input.files.some((file) => !names.has(file.name) && file.name.includes(marker)))
    .map((marker) => `no lazy chunk contains ${marker}`);
}

export function checkBundle(input: BundleInput): string[] {
  return [...entryViolations(input), ...totalViolations(input), ...lazyViolations(input)];
}

function listFiles(root: string, dir = ""): BundleFile[] {
  return readdirSync(join(root, dir)).flatMap((name) => {
    const relative = join(dir, name);
    const stat = statSync(join(root, relative));
    return stat.isDirectory() ? listFiles(root, relative) : [{ name: relative, bytes: stat.size }];
  });
}

function entryOf(root: string, manifestPath: string): string[] {
  const html = readFileSync(join(root, "index.html"), "utf8");
  const scripts = [...html.matchAll(/<script\b[^>]*src="\/([^"]+)"[^>]*>/g)]
    .filter(([tag]) => !/\snomodule(?:[=\s>])/i.test(tag))
    .flatMap((match) => match[1] ?? []);
  const manifest = JSON.parse(readFileSync(manifestPath, "utf8")) as Record<string, { files: string[] }>;
  const panel = manifest["app/panel.tsx -> ../App"]?.files;
  if (!scripts.length || !panel?.length) throw new Error("Missing Next.js startup assets");
  const names = [...new Set([...scripts, ...panel.filter((name) => name.endsWith(".js")).map((name) => `_next/${name}`)])];
  for (const name of names) {
    if (!statSync(join(root, name)).isFile()) throw new Error(`Missing entry script ${name}`);
  }
  return names;
}

function main(): void {
  const root = fileURLToPath(new URL("../../../../internal/proxy/ui/dist", import.meta.url));
  const files = listFiles(root);
  const manifest = fileURLToPath(new URL("../.next/react-loadable-manifest.json", import.meta.url));
  const entry = entryOf(root, manifest);
  const violations = checkBundle({ entry, files, lazy: lazyChunks, startupLimit: 1024 * 1024 });
  for (const violation of violations) {
    console.error(violation);
  }
  const total = files.reduce((sum, file) => sum + file.bytes, 0);
  const startup = files.reduce((sum, file) => sum + (entry.includes(file.name) ? file.bytes : 0), 0);
  console.log(`dist ${total} bytes in ${files.length} files; startup scripts ${startup} bytes`);
  process.exit(violations.length === 0 ? 0 : 1);
}

if (import.meta.main) {
  main();
}
