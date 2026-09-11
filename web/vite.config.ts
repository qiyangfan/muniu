import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

// 开发期代理到本地网关;构建产物经 go:embed 嵌入二进制,由 /console/* 提供
export default defineConfig({
  base: "/console/",
  plugins: [react(), tailwindcss()],
  server: {
    proxy: {
      "/v1": "http://127.0.0.1:8787",
      "/admin": "http://127.0.0.1:8787",
    },
  },
});
