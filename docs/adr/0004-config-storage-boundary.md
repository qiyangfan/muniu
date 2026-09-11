# ADR-0004:静态拓扑走 YAML,运行态走 SQLite,秘密只在环境变量

- 状态:已接受(2026-09-08)

## 决策

三分离:providers/model_routes/pricing/超时等**部署期拓扑**进 `muniu.yaml`(git 版本化);tenants/api_keys/usage_logs 等**运行期状态**进内嵌 SQLite;provider key 明文只允许存在于环境变量,YAML 与 DB 一律存环境变量引用名(`env: DEEPSEEK_API_KEY_1`)。

## 理由

- 拓扑进 git = 变更可审计、可回滚、环境间可 diff——one-api 把拓扑全塞数据库,换来的是「改个路由要登 UI 手点」,部署不可重现;
- 运行态进库 = apikey 签发/吊销/配额不必改配置重启进程;两类数据变更频率差两个数量级,混放必然两头别扭;
- 秘密纪律:DB 泄露/YAML 误提交都拿不到明文 key;引用名机制让「换 key」= 换环境变量 + 重启,操作可预期。

## 备选与否决

- 全 YAML(apikey 也在文件):运行态变更需要改文件+重载,与「网关是长期运行服务」矛盾;
- 全 DB(one-api 形态):如上,拓扑不可审计;
- 内置 secrets 加密:单二进制 sidecar 无 KMS 可依赖,密钥管理诚实外移给环境(容器 secret/注 入脚本),不假装解决。

## 后果

改拓扑需重启(热加载在 backlog,SIGHUP 触发);SQLite 单写者 → 计量异步批量;YAML 校验必须在启动期 fail-fast 且一次性报全(config 契约)。
