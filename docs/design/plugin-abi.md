# Muniu 插件 ABI v0(冻结)

- 状态:**v0 已冻结(2026-09-12)**;任何变更走 ADR(ABI v1 或废弃)。
- 宿主实现:`internal/plugin`;参考实现:`plugins/pii-masker` 与 `plugins/json-redactor`(Rust wasm32-unknown-unknown,经 `muniu-plugin-sdk` 承接全部 ABI 机制——插件作者零 unsafe,见 `plugins/README.md`)。
- 本文与 ADR-0011、openspec change `wasm-plugin-runtime` 的 design Decisions 一一对应。

## 0. 模块形态

- 插件为 wasm 线性内存模块:必须导出名为 `memory` 的线性内存。
- **目标建议 `wasm32-unknown-unknown`**(样例插件 pii-masker 即此目标):零 WASI 导入,不持文件句柄/时钟/随机源,沙箱面最小。
- 宿主亦兼容导入 `wasi_snapshot_preview1` 的模块(wazero WASI 实例化,**零文件系统挂载、零 socket**——插件不持凭证、不触网是 spec 硬性要求)。
- 宿主自定义导入模块名为 `muniu`(见 §3)。
- 宿主与插件的一切数据交换只经线性内存 offset/len,**单次拷贝**:宿主写入 → 插件读;插件写 → 宿主拷出。

## 1. 插件导出(v0 共 6 个,全部必选)

| 导出签名(wasm) | 语义 |
|---|---|
| `muniu_abi_version() -> i32` | ABI 版本探测,必须返回 `0`(v0)。宿主注册时调用一次,不匹配即拒载 |
| `plugin_start() -> i32` | 实例初始化(读配置、编译正则等)。`0`=成功;非 `0`=实例不可用,宿主丢弃该实例 |
| `malloc(len: i32) -> i32` | 分配 `len` 字节,返回起始 offset;`0`=分配失败(0 号字节永不分配,保留为失败哨兵) |
| `free(offset: i32)` | 释放 `malloc` 得到的 offset;重复释放/非法 offset 由插件方容错(宿主不依赖其行为) |
| `hook_request_body(offset: i32, len: i32) -> i32` | 请求体钩子。输入:宿主经 `malloc` 写入插件内存的原始请求体。返回 verdict:`0`=放行(未改写)、`1`=改写、`2`=拒绝 |
| `plugin_result() -> i64` | 结果通道,紧跟 verdict≠0 的 `hook_request_body` 调用后由宿主调用一次:高 32 位 = 结果 offset,低 32 位 = 结果 len。verdict=`1` → 新请求体字节(宿主拷出后替换请求体);verdict=`2` → 拒绝理由 UTF-8 文本(宿主截断至 512 字节);verdict=`0` → 未定义,宿主不读 |

**所有权规则**:`hook_request_body` 的入参 buffer 由宿主 `malloc`、宿主写、宿主在钩子返回后 `free`;结果 buffer 由插件 `malloc`、宿主拷出后宿主 `free`(同一分配器;若结果 offset 与入参 offset 相同只 free 一次)。

**错误码表**:`muniu_abi_version` 返回非 0 = ABI 不匹配(注册拒绝);`plugin_start` 非 0 = 实例初始化失败(注册拒绝或实例丢弃);`malloc` 返回 0 = 内存不足(该次调用失败,计 containment);钩子 trap/超时/超内存 = 调用失败(计 containment,按 fail 策略处置)。

**结果通道上限**:宿主对 `plugin_result` 声明的 len 施加 **16 MiB** 单次上限(超限 = `bad_result` containment)——防恶意插件拖入巨型拷贝;正常改写产物(≤入站体上限)远低于此。

**所有权补则**:结果 buffer 复用入参 buffer(原地改写,offset 相同)时宿主只 free 一次。

## 2. 请求体钩子语义

- 输入请求体 = 入站 JSON 原始字节(已过 32MB 上限与 OpenAI 形态校验)。
- 插件必须 (MUST) 只读入参 buffer、只在自身内存构造结果;不得假设两次调用间入参 buffer 内容留存。
- verdict=`1` 时宿主以新请求体**重新执行入站解析**,解析失败按请求级失败处置(fail_closed 默认拒绝)——插件产出必须是合法 JSON。
- 改写留痕:宿主在响应头 `X-Muniu-Plugin` 回带触发改写的插件名列表(逗号分隔)。

## 3. 宿主导入(模块 `muniu`,v0 共 2 个)

| 导入签名(wasm) | 语义 |
|---|---|
| `log(level: i32, offset: i32, len: i32)` | 插件写结构化日志;`level`:`0`=debug `1`=info `2`=warn `3`=error;字节位于插件线性内存,宿主读取后原样透传(带插件名标签) |
| `get_config(key_off: i32, key_len: i32, val_buf_off: i32, val_buf_len: i32) -> i32` | 读 per-plugin 配置(字符串直通,v0 不做结构校验)。返回:写入 `val_buf` 的字节数;`0`=key 不存在;`-N`=缓冲不足,需要 N 字节。值字节 UTF-8 |

配置来源:muniu.yaml `plugins[].config`(map[string]string)与管理面注册参数,宿主以只读 map 注入,键序无关。

## 4. 版本与注册校验

注册探测(实例化/版本探测/plugin_start)全程受**探测超时**约束(缺省 max(2s, 4×调用超时),可按插件配置,硬顶 60s)——插件在任一环节死循环即探测超时拒载,注册链不可能无界挂死。

注册即校验,顺序:
1. **二进制合法性**:wasm magic(`\0asm`)+ 版本字段(LE `0x6d736100`),wazero 编译兜底;
2. **编译**:wazero 编译一次(compilation cache 常驻进程),失败即拒载;
3. **探测实例**:以正式资源限制实例化 → `muniu_abi_version()` 必须 = 0 → `plugin_start()` 必须 = 0;缺任一导出、版本不符、start 失败均拒载并留审计;
4. 校验通过的探测实例直接入池服务(零浪费)。

## 5. 资源限制默认值(全部可按插件配置)

| 项 | 默认 | 上限语义 |
|---|---|---|
| 实例内存 | 64 MiB | wazero memory limit(超限 memory.grow trap = 调用失败) |
| 单次调用超时 | 50 ms | wazero epoch/上下文中断(超时 trap = 调用失败) |
| 实例池 | 8 / 插件 | 池满并发等待;空闲实例超时回收重建 |
| free 清理 | 独立 ≤调用超时 | free 与钩子同样运行不可信代码:宿主给独立短超时(请求取消后清理仍执行,但有上界);free 被中断即模块已关闭,实例丢弃 |

## 6. 失败语义

- 默认 **fail_closed**:插件调用失败(超时/超内存/trap/解析失败)→ 请求以 `internal`/`plugin_failed` 信封拒绝;
- **fail_open**(per-plugin 显式配置):调用失败 → 原请求体放行 + containment 指标 + 审计日志;
- 插件 verdict=`2` 拒绝 → `bad_request`/`plugin_rejected` 信封,message 为插件理由。

## 7. 演进纪律

- v0 只服务自家样例插件,**不对第三方承诺兼容**;字段只增不改语义,增字段即 v1 并走 ADR。
- 不承诺 proxy-wasm 0.2.1 兼容(ADR-0011);Component Model/WIT 待 wazero 支持后评估。
