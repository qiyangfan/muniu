# Design: 用量与健康上报（data-plane-metering-sink）

## Context

现有 metering sink 写本地/锦官 ingest；需要统一 usage/health envelope、服务身份与 LLM 单一来源。

## Goals / Non-Goals

**Goals:**

- LLM 用量可聚合。
- 上报不阻断热路径。
- 服务身份认证。

**Non-Goals:**

- 不做计费。
- 不上报内容。
- 不让其他服务重复上报 LLM 用量。

## Decisions

1. usage/health 事件独立 envelope；source=muniu。
2. 本地缓冲/重试/幂等；失败不阻断。
3. 只传计量/健康摘要；LLM 为唯一来源。
4. 服务身份认证；匿名拒绝。

## Risks / Trade-offs

- 缓冲膨胀 → 上限/告警。
- 重复 → 幂等键。
- 双计 → 其他服务 LLM 事件拒绝/丢弃。

## Migration Plan

greenfield：接入锦官上报；本地计量保留。
