# pii-masker — muniu 样例 WASM 插件(ABI v0)

请求载荷 PII 脱敏:对 `messages[].content`(含 content parts 的 text 段)与
embeddings `input` 做正则替换,验证 muniu 插件运行时的「编写 → 构建 → 注册 →
生效 → 热替换 → 禁用」全流程。ABI 契约见 `docs/design/plugin-abi.md`,选型见
ADR-0011。基于 `muniu-plugin-sdk` 实现(业务代码 ~90 行,零 unsafe;ABI 机制见
`plugins/sdk`)。

## 规则

- 宿主配置注入(`plugins[].config.rules` 或管理面注册参数),JSON 数组:
  ```json
  [{"pattern": "1[3-9]\\d{9}", "replacement": "[PHONE]"}]
  ```
- 未配置 `rules` 时使用内置缺省:身份证 18 位 → `[ID]`、手机号 → `[PHONE]`、邮箱 → `[EMAIL]`。
- 规则非法 → init Err → 注册即拒载(坏规则不允许静默降级)。

### 词边界语义(2026-09-13 缺陷修复)

替换引擎施加**引擎层 ASCII 词边界**:命中两侧紧邻 ASCII 字母数字时拒判(防手机号
规则咬进身份证长串等截断误伤),CJK/标点/空白均视为边界——**号码紧贴中文可命中**
(`电话13912345678` → `电话[PHONE]`)。

- 旧版(含 `\b` 锚点)在 regex crate 的 Unicode `\w` 语义下 CJK 与数字同为词字符,
  紧贴中文整体失明——已由 `muniu-plugin-sdk` 重写批次修复(用户裁决),模式不再带 `\b`。
- 自定义规则同样受该边界约束:需匹配「更长 ASCII token 内部子串」的极少数场景,
  当前无配置开关(无消费者;需要时经 config 显式加 boundary 开关)。

## 构建

```bash
rustup target add wasm32-unknown-unknown   # 首次
../build.sh                                # workspace 全量构建,产物 ./pii_masker.wasm
# 或仓库根:make plugin-build
```

> 目标为 wasm32-unknown-unknown(非 wasip1):无 self-contained libc,规避 C dlmalloc
> 与 ABI malloc/free 导出的同名符号冲突;插件零 WASI 导入,宿主侧无文件/时钟/随机面。

## 使用(muniu 侧)

静态注册(muniu.yaml):

```yaml
plugins:
  - name: pii-masker
    path: ./plugins/pii-masker/pii_masker.wasm
    config:
      rules: |
        [{"pattern":"1[3-9]\\d{9}","replacement":"[PHONE]"}]
```

运行态(管理 API,重启后回落 YAML 声明):

```bash
curl -X PUT http://127.0.0.1:8787/admin/v1/plugins/pii-masker \
  -H "Authorization: Bearer $MUNIU_ADMIN_TOKEN" -H "Content-Type: application/json" \
  -d "{\"path\":\"$PWD/plugins/pii-masker/pii_masker.wasm\"}"

curl -X PATCH http://127.0.0.1:8787/admin/v1/plugins/pii-masker \
  -H "Authorization: Bearer $MUNIU_ADMIN_TOKEN" -d '{"enabled":false}'
```

## CI 模板

见 `.github/workflows/plugins.yml`(plugins/** 变更触发:workspace 构建双插件 + Rust
宿主单测 + artifact 上传;宿主侧测试与发布不依赖 Rust 工具链)。

## 内存语义(贡献者须知)

- arena 代际制、两段式配置读取、scratch 错位等 ABI 机制已全部由 SDK 承接
  (不变量文档见 `plugins/sdk/src/__abi.rs` 模块头);本插件只写 `init` + 钩子逻辑,
  规则/正则经 `state` 驻留 dlmalloc 区,与 arena 正交。
- `memory_limit_mib` 须 ≥ 请求体 ×2 + dlmalloc 余量;默认 64MiB 覆盖 ≈25MiB 请求体。
