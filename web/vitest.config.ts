import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";

// 组件测试配置(openspec fix-api-key-binding-identifiers 引入 vitest,devDependency 不进产物)。
// globals: true 仅为启用 @testing-library/react 的自动 cleanup;断言 API 一律从 "vitest" 显式导入。
export default defineConfig({
  plugins: [react()],
  test: {
    environment: "jsdom",
    globals: true,
    include: ["src/**/*.test.{ts,tsx}"],
  },
});
