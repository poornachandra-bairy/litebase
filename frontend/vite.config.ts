import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// The build output goes straight into the Go binary's embed directory, so
// "npm run build" followed by "go build" produces a single self-contained
// binary with the dashboard inside it.
export default defineConfig({
  plugins: [react()],
  build: {
    outDir: "../backend/cmd/litebase/frontend",
    // The output directory holds a committed .gitkeep that go:embed depends on,
    // so Vite must not empty it. The Makefile removes stale assets instead.
    emptyOutDir: false,
    // Source maps would roughly double the embedded payload for little benefit
    // on a self-hosted admin tool.
    sourcemap: false,
  },
  server: {
    port: 5173,
    // In development Vite serves the UI and proxies API calls to the Go
    // server, so cookies and CSRF behave exactly as they do in production.
    proxy: {
      "/api": {
        target: "http://127.0.0.1:8090",
        changeOrigin: false,
      },
    },
  },
});
