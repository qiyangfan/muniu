import AxeBuilder from "@axe-core/playwright";
import { expect, test, type Page } from "@playwright/test";

// 登录页壳层冒烟:未登录时控制台只渲染 Login 卡片,不发业务数据请求;
// /api/** 与 /admin/** 一律 page.route 拦截,用例不依赖网关在场。
// 这里验证「壳层能起来且无实测无障碍缺陷」,不覆盖登录成功后的业务页。

// mockApi 拦截全部后端请求:whoami 回 401 让 App 落到登录态,联邦信息关闭。
async function mockApi(page: Page): Promise<void> {
  await page.route("**/api/**", (route) =>
    route.fulfill({ status: 404, contentType: "application/json", body: "{}" }),
  );
  await page.route("**/admin/**", (route) => {
    if (route.request().url().endsWith("/auth/federation")) {
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ enabled: false }),
      });
    }
    // 其余管理面请求按未登录处理
    return route.fulfill({
      status: 401,
      contentType: "application/json",
      body: JSON.stringify({ error: { message: "未登录" } }),
    });
  });
}

test.beforeEach(async ({ page }) => {
  await mockApi(page);
});

// 木牛去账号化后登录页只保留「统一身份登录」整页跳转与管理令牌两条,不再有本地账密表单。
test("登录页壳层可渲染并完成登录态探测", async ({ page }) => {
  const resp = await page.goto("/console/");
  expect(resp?.status()).toBe(200);
  await expect(page.getByRole("heading", { name: "木牛控制台" })).toBeVisible();
  await expect(page.getByRole("button", { name: "统一身份登录" })).toBeVisible();
  await expect(page.getByLabel("用户名")).toHaveCount(0);
  await expect(page.getByLabel("密码")).toHaveCount(0);
  // 探测失败(401)不得把登录页替换成错误页
  await expect(page.getByRole("alert")).toHaveCount(0);
});

test("登录页统一身份入口与令牌模式切换", async ({ page }) => {
  await page.goto("/console/");
  await page.getByRole("button", { name: "使用管理令牌登录" }).click();
  await expect(page.getByLabel("管理员令牌")).toBeVisible();
  await page.getByRole("button", { name: "返回统一身份登录" }).click();
  await expect(page.getByRole("button", { name: "统一身份登录" })).toBeVisible();
});

// 只拦 serious/critical:minor/moderate 多为对比度边角,留给设计走查,避免噪声淹没真缺陷。
test("登录页无障碍:serious/critical 级缺陷为零", async ({ page }) => {
  await page.goto("/console/");
  await expect(page.getByRole("heading", { name: "木牛控制台" })).toBeVisible();
  const results = await new AxeBuilder({ page })
    .withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"])
    .analyze();
  const severe = results.violations.filter(
    (v) => v.impact === "serious" || v.impact === "critical",
  );
  // 失败时把规则 id 与命中节点一并打出来,便于直接定位
  const summary = severe.map(
    (v) => `${v.id}: ${v.nodes.map((n) => n.target.join(" ")).join(", ")}`,
  );
  expect(summary).toEqual([]);
});
