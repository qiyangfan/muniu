# pii-masker — muniu 样例 WASM 插件(ABI v0)

请求载荷 PII 脱敏:对 `messages[].content`(含 content parts 的 text 段)与
embeddings `input` 做正则替换,验证 muniu 插件运行时的「编写 → 构建 → 注册 →
生效 → 热替换 → 禁用」全流程。ABI 契约见 `docs/design/plugin-abi.md`,选型见
ADR-0011。

## 规则

- 宿主配置注入(`plugins[].config.rules` 或管理面注册参数),JSON 数组:
  ```json
  [{"pattern": "1[3-9]\\d{9}", "replacement": "[PHONE]"}]
  ```
- 未配置 `rules` 时使用内置缺省:身份证 18 位 → `[ID]`、手机号 → `[PHONE]`、邮箱 → `[EMAIL]`。
- 规则非法 → `plugin_start` 非 0,注册即拒载(坏规则不允许静默降级)。

## 构建

```bash
rustup target add wasm32-unknown-unknown   # 首次
./build.sh                                 # 产物 ./pii_masker.wasm
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
        [{"pattern":"\\b1[3-9]\\d{9}\\b","replacement":"[PHONE]"}]
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

见 `.github/workflows/plugins.yml`(plugins/** 变更触发:构建 wasm 并上传 artifact;
宿主侧测试与发布不依赖 Rust 工具链)。

## 内存语义(贡献者须知)

- 双 arena 代际制:A 区 = 宿主写入的请求体,B 区 = 钩子产物;宿主 `free` 为
  no-op,代际在下一请求 `malloc` 时统一重置。**插件不得持有跨调用的内存指针**
  (ABI 契约 §2),本插件的规则/正则驻留在 Rust 分配器(dlmalloc)区域,与 arena 正交。
- `memory_limit_mib` 须 ≥ 请求体 ×2 + dlmalloc 余量;默认 64MiB 覆盖 ≈30MiB 请求体。
