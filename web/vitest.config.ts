import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";

// globals 仅用于启用 Testing Library 的自动 cleanup；断言 API 从 vitest 显式导入。
export default defineConfig({
  plugins: [react()],
  test: {
    environment: "jsdom",
    globals: true,
    include: ["src/**/*.test.{ts,tsx}"],
    coverage: {
      provider: "v8",
      include: ["src/**/*.{ts,tsx}"],
      // openapi-typescript 生成物不进统计,与 eslint 的 ignore 同口径
      exclude: ["src/lib/api-types.d.ts"],
      // 阈值取 2026-11-19 实测基线向下取整(66.83/54.5/59.85/68.83),只防回退不拔高目标
      thresholds: { statements: 66, branches: 54, functions: 59, lines: 68 },
      // json-summary 供 scripts/coverage-gate.mjs 做逐文件下限校验
      reporter: ["text-summary", "json-summary"],
      reportsDirectory: "coverage",
    },
  },
});
