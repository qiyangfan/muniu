import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";

// globals 仅用于启用 Testing Library 的自动 cleanup；断言 API 从 vitest 显式导入。
export default defineConfig({
  plugins: [react()],
  test: {
    environment: "jsdom",
    globals: true,
    include: ["src/**/*.test.{ts,tsx}"],
  },
});
