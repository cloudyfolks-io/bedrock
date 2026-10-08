import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import babel from "@rolldown/plugin-babel";
import { lingui } from "@lingui/vite-plugin";

export default defineConfig({
  base: "/",
  plugins: [
    babel({
      include: [/\/panel\/src\/.*\.(?:[jt]sx?|[cm][jt]s)(?:$|\?)/],
      plugins: [["@lingui/babel-plugin-lingui-macro"]],
    }),
    react(),
    lingui(),
  ],
  build: {
    outDir: "../../../internal/proxy/ui/dist",
    emptyOutDir: true,
    target: "esnext",
    rolldownOptions: {
      output: {
        codeSplitting: {
          groups: [
            { name: "novnc", test: /node_modules[\\/]@novnc/ },
            { name: "xterm", test: /node_modules[\\/]@xterm/ },
          ],
        },
      },
    },
  },
});
