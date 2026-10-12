import path from "node:path";
import { defineConfig, lazyPlugins } from "vite-plus";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import wails from "@wailsio/runtime/plugins/vite";

const ignored = ["bindings/**", "dist/**"];

export default defineConfig({
  fmt: {
    ignorePatterns: ignored,
  },
  lint: {
    ignorePatterns: ignored,
    jsPlugins: [{ name: "vite-plus", specifier: "vite-plus/oxlint-plugin" }],
    rules: { "vite-plus/prefer-vite-plus-imports": "error" },
    options: { typeAware: true, typeCheck: true, denyWarnings: true },
  },
  test: {
    passWithNoTests: true,
  },
  server: {
    host: "127.0.0.1",
    port: Number(process.env.WAILS_VITE_PORT) || 9245,
    strictPort: true,
  },
  resolve: {
    alias: {
      "@": path.resolve(import.meta.dirname, "src"),
      "@bindings": path.resolve(import.meta.dirname, "bindings/github.com/edereagzi/kubereach"),
    },
  },
  plugins: lazyPlugins(() => [react(), tailwindcss(), wails("./bindings")]),
});
