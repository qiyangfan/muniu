# Claude Code 接入木牛(类 cc-switch 反代)

> muniu 的 `/v1/messages` 入站(ADR-0013)使 Claude Code 等 Anthropic 协议客户端可直连,
> 由网关完成换家/failover/key 池/计量。与 cc-switch 的本质差异:cc-switch 改写客户端配置
> 切家(切一次改一次);muniu 对客户端是唯一端点,换家在网关侧完成,客户端零改动。

## 三步接入

**1. muniu.yaml 定义别名路由**——把 Claude 的档位别名指到任意上游(含国内 Anthropic 分支):

```yaml
providers:
  - name: zhipu-anthropic
    adapter: anthropic
    auth: bearer                       # 国内分支声明口径,预设目录可直接复制
    base_url: "https://open.bigmodel.cn/api/anthropic"
    keys: [{ env: ZHIPUAI_API_KEY }]
    weight: 70
  - name: moonshot
    adapter: moonshot
    base_url: "https://api.moonshot.cn/v1"
    keys: [{ env: MOONSHOT_API_KEY }]
    weight: 30

model_routes:
  claude-sonnet-4-5:                   # 别名即路由键:ANTHROPIC_MODEL 填什么就定义什么
    - { provider: zhipu-anthropic, model: glm-5.1, priority: 0 }
    - { provider: moonshot, model: kimi-k2.7-code, priority: 1 }   # failover 候选
  claude-haiku-4-5:                    # 后台小模型单独一路(或指同一档)
    - { provider: moonshot, model: kimi-k2.7-code, priority: 0 }
```

**2. Claude Code 指向木牛**(密钥管理页签发 mk- key):

```bash
export ANTHROPIC_BASE_URL=http://127.0.0.1:8787
export ANTHROPIC_AUTH_TOKEN=mk-xxxx                     # 木牛的 key,不是厂商 key
export ANTHROPIC_MODEL=claude-sonnet-4-5
export ANTHROPIC_DEFAULT_SONNET_MODEL=claude-sonnet-4-5
export ANTHROPIC_DEFAULT_HAIKU_MODEL=claude-haiku-4-5
export ANTHROPIC_DEFAULT_OPUS_MODEL=claude-sonnet-4-5
export CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1       # 可选:关非必要遥测
```

**3. 启动 muniu,直接 `claude`。**

## key 的流向(两跳两把钥匙)

- **客户端 → muniu**:Bearer(`ANTHROPIC_AUTH_TOKEN`)或 x-api-key(`ANTHROPIC_API_KEY`,
  网关自动适配为 Bearer)携带的都是**木牛的 mk- key**——鉴权/配额/限流/计量/审计都在这一跳。
- **muniu → 上游**:各家真实 key 在 `providers[].keys` 的环境变量里,按声明的鉴权形态注入
  (`auth: bearer` 发 Authorization Bearer,缺省发 x-api-key,协议契约见 upstream-adapters)。
- 客户端永远只见 mk- key:上游 key 轮换/池化/冷却/failover 对 Claude Code 完全透明。

## 边界与提示

- `count_tokens` 未实现:Claude Code 自动回落本地估算;`DISABLE_NONESSENTIAL_TRAFFIC=1` 可一并关掉。
- 请求侧 thinking 块被丢弃(签名回传语义不成立):工具多轮功能不受影响,思考回放不可见。
- 流式(SSE)与非流式均支持;工具多轮(tool_use/tool_result)翻译完整,agentic 循环可达任意
  OpenAI 兼容上游(经 anthropic 分支出站亦然)。
- 出站为国内 Anthropic 分支时,鉴权形态已按官方文档口径声明 `auth: bearer`;实际接受头以
  真上游冒烟为准(docs/provider-presets.md「鉴权形态」)。
