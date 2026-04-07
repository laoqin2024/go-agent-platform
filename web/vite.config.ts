import { defineConfig } from "vite";
import vue from "@vitejs/plugin-vue";

export default defineConfig({
  plugins: [vue()],
  server: {
    port: 5173,
    proxy: {
      // Global status WebSocket must be first to avoid being shadowed
      "/global_status": {
        target: "http://127.0.0.1:8080",
        ws: true,
        changeOrigin: true,
        rewrite: (path) => path,
      },
      // Avoid CORS during development by proxying API requests to Go backend.
      "/api": {
        target: "http://127.0.0.1:8080",
        changeOrigin: true,
      },
      // WebSocket: frontend connects to `/ws/:device_id` on same-origin;
      // we proxy it to the Go backend so LAN clients work without env vars.
      "/ws": {
        target: "http://127.0.0.1:8080",
        changeOrigin: true,
        ws: true,
      },
      // Global status WebSocket channel
      "/ws-global": {
        target: "http://127.0.0.1:8080",
        changeOrigin: true,
        ws: true,
      },
    },
  },
});

