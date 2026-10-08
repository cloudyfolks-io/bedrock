import { readdirSync, readFileSync, statSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

export interface BundleFile {
  name: string;
  bytes: number;
}

export interface BundleInput {
  entry: string;
  files: BundleFile[];
  lazy?: readonly string[];
}

const entryLimit = 600 * 1024;
const totalLimit = 8 * 1024 * 1024;
export const lazyChunks = ["novnc", "xterm", "uplot"] as const;

function entryViolations(input: BundleInput): string[] {
  const entry = input.files.find((file) => file.name === input.entry);
  return entry && entry.bytes > entryLimit ? [`entry ${entry.name} is ${entry.bytes} bytes, limit ${entryLimit}`] : [];
}

function totalViolations(input: BundleInput): string[] {
  const total = input.files.reduce((sum, file) => sum + file.bytes, 0);
  return total > totalLimit ? [`dist is ${total} bytes, limit ${totalLimit}`] : [];
}

function lazyViolations(input: BundleInput): string[] {
  return (input.lazy ?? lazyChunks)
    .filter((marker) => !input.files.some((file) => file.name !== input.entry && file.name.includes(marker)))
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

function entryOf(root: string): string {
  const html = readFileSync(join(root, "index.html"), "utf8");
  const match = /<script[^>]*type="module"[^>]*src="\/([^"]+)"/.exec(html);
  if (!match?.[1]) {
    throw new Error("index.html has no module script");
  }
  return match[1];
}

function main(): void {
  const root = fileURLToPath(new URL("../../../../internal/proxy/ui/dist", import.meta.url));
  const files = listFiles(root);
  const violations = checkBundle({ entry: entryOf(root), files, lazy: ["novnc", "xterm"] });
  for (const violation of violations) {
    console.error(violation);
  }
  const total = files.reduce((sum, file) => sum + file.bytes, 0);
  console.log(`dist ${total} bytes in ${files.length} files`);
  process.exit(violations.length === 0 ? 0 : 1);
}

if (import.meta.main) {
  main();
}
