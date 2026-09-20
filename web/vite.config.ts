import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import path from "node:path";

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: { alias: { "@": path.resolve(import.meta.dirname, "./src") } },
  build: {
    // Every megabyte here is a megabyte of the swissd binary, shipped to every
    // cluster and pulled on every rollout. Keep the warning limit low enough
    // that growth is noticed rather than discovered.
    chunkSizeWarningLimit: 400,
  },
  server: {
    // Dev runs against a local swissd; the embedded build serves both from one
    // origin, so there is no CORS either way.
    proxy: { "/api": "http://127.0.0.1:8080" },
  },
});
