# Proposal: sighup-config-reload

## Why

配置变更(换上游/改路由/换模型能力目录)当前需要重启进程;控制面拉取源与价目管理面都有运行态变更通道,唯独文件配置这一运维主路径没有热加载手势。原子热替换机器(3.1 快照+池运行态保留)已经建好,缺的只是一个运维入口把它打通到端到端。

## What Changes

- `SIGHUP` → 重读配置文件(locate 同一解析链):校验失败保持旧态并告警;成功 → 拓扑/池/适配器/能力目录经既有 `ReplaceTopology` 热替换(provider 配置未变时池运行态无损)。
- 范围登记:`auth`/`metrics`/管理面/静态价目兜底不热更(价目运营态本就走管理面;静态兜底重启生效)。

## Capabilities

### Modified Capabilities

- `platform-data-plane`:新增「SIGHUP 配置热加载」Requirement(校验失败保持旧态/成功热替换/范围边界)。
