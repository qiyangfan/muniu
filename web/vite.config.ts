import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

// 开发期代理到本地网关;构建产物经 go:embed 嵌入二进制,由 /console/* 提供
// compiler: React Compiler 自动 memo 化(plugin-react ≥6.1,oxc 引擎);
// 对不合规组件自动跳过,渐进安全——组件级纪律由 eslint-plugin-react-hooks 的
// react-compiler 规则在 lint 门禁把守
export default defineConfig({
  base: "/console/",
  plugins: [react({ compiler: true }), tailwindcss()],
  server: {
    proxy: {
      "/v1": "http://127.0.0.1:8787",
      "/admin": "http://127.0.0.1:8787",
    },
  },
});
