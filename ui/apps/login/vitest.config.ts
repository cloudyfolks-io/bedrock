import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";
import babel from "@rolldown/plugin-babel";
import { lingui } from "@lingui/vite-plugin";

export default defineConfig({
  plugins: [
    babel({
      include: [/\/apps\/login\/src\/.*\.(?:[jt]sx?|[cm][jt]s)(?:$|\?)/],
      plugins: [["@lingui/babel-plugin-lingui-macro"]],
    }),
    react(),
    lingui(),
  ],
  test: {
    environment: "jsdom",
    include: ["src/**/*.test.{ts,tsx}"],
    setupFiles: ["./src/setupTests.ts"],
  },
});
