import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import path from "node:path";
import { HttpProxyAgent } from "http-proxy-agent";

const egressProxy = process.env.http_proxy ?? process.env.HTTP_PROXY;
// const remote = 'http://127.0.0.1:8080';
const remote = 'http://172.28.44.16:32326';

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
    proxy: {
      "/api": {
        target: `${remote}`,
        changeOrigin: true,
        agent: egressProxy ? new HttpProxyAgent(egressProxy) : undefined,
      }
    },
  },
});
