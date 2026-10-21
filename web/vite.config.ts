import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

// 开发期代理到本地网关；构建产物经 go:embed 嵌入二进制，由 /console/* 提供。
// compiler 开启 React Compiler 自动 memo 化，对不合规组件自动跳过；
// 组件纪律由 eslint-plugin-react-hooks 的 react-compiler 规则在 lint 把守。
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
