import { defineConfig } from "vite";
export default defineConfig({
  server: {
    proxy: {
      "/admin/api": "http://127.0.0.1:8080",
      "/healthz": "http://127.0.0.1:8080",
    },
  },
});
