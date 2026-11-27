/** @type {import('next').NextConfig} */
const config = {
  output: "export",
  pageExtensions: ["next.tsx", "next.ts"],
  devIndicators: false,
  reactStrictMode: true,
  transpilePackages: ["@bedrock/design"],
  webpack(config, { isServer }) {
    config.module.rules.push({
      test: /\.tsx?$/,
      include: fileURLToPath(new URL("./src", import.meta.url)),
      enforce: "pre",
      use: fileURLToPath(new URL("./scripts/lingui-macros.cjs", import.meta.url)),
    });
    config.module.rules.push({
      test: /\.po$/,
      use: [{ loader: "@lingui/loader", options: { failOnCompileError: true } }],
    });
    if (!isServer && config.optimization.splitChunks) {
      config.output.environment = { ...config.output.environment, asyncFunction: true };
      const groups = config.optimization.splitChunks.cacheGroups;
      for (const [name, test] of [
        ["novnc", /[\\/]node_modules[\\/]@novnc[\\/]/],
        ["xterm", /[\\/]node_modules[\\/]@xterm[\\/]/],
        ["uplot", /[\\/]node_modules[\\/]uplot[\\/]/],
      ]) {
        groups[name] = { test, name, chunks: "async", enforce: true, priority: 50 };
      }
    }
    return config;
  },
};

export default config;
import { fileURLToPath } from "node:url";
