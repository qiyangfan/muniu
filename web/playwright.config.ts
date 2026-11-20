import { defineConfig, devices } from "@playwright/test";

// 本文件与测试用例由 tsc 主工程检查,后者不含 Node 全局:CI 只在 reporter 一处分流
const isCI = Boolean(
  (globalThis as { process?: { env?: Record<string, string> } }).process?.env?.CI,
);

// 浏览器冒烟 + 无障碍:对生产构建做真浏览器加载,补上 jsdom 单测看不到的
// 布局/焦点/ARIA 计算问题。CI ubuntu-latest 预装 Chrome,与本地同一 channel,
// 不依赖 npx playwright install 下载浏览器(仓库不缓存二进制)。
export default defineConfig({
  testDir: "e2e",
  // 不重试:冒烟用例若有失败,重试只会拉长反馈并掩盖稳定性问题
  retries: 0,
  workers: 1,
  reporter: isCI ? [["list"], ["github"]] : [["list"]],
  use: {
    baseURL: "http://127.0.0.1:4173",
    channel: "chrome",
    trace: "retain-on-failure",
  },
  projects: [{ name: "chrome", use: { ...devices["Desktop Chrome"] } }],
  // preview 服务消费 vite build 产物;CI 先跑 build,再经 webServer 起静态服务
  webServer: {
    command: "pnpm exec vite preview --host 127.0.0.1 --port 4173 --strictPort",
    url: "http://127.0.0.1:4173/console/",
    // 本地保留复用:手动起过 preview 时不必再拉一份;CI 上端口只能是本作业自己起的
    reuseExistingServer: !isCI,
    timeout: 60_000,
  },
});
