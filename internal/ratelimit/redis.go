// RedisBackend = SharedBackend 的 redis 实现(cluster 形态可选;ADR-0008 修订):
// RPM = GCRA 令牌桶(Lua 原子,桶容=rpm、速率=rpm/分,与 inmem x/time/rate 同算法族);
// TPM = 60×1s 槽 INCR+EXPIRE(Lua 单往返求和,窗口语义与 inmem 环形窗一致)。
// 降级纪律(spec「redis 后端降级纪律」):任一操作失败即置 degraded,回退内嵌
// inmem(RPM 令牌桶 + TPM 环窗)判定并告警一次;恢复探测 = 降级态下每
// probeInterval 试一次 PING,成功即回共享视图。降级窗口内短时计数误差可接受,
// 日配额由控制面 ingest 聚合兜底。
package ratelimit

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

// rpmGCRA GCRA 令牌桶:KEYS[1] 存 TAT(theoretical arrival time,秒浮点);
// ARGV: now(秒浮点)、rpm。返回 {allow(0/1), retry_after(秒)}。
// 与 x/time/rate 的 rate.Every(60s/rpm)+burst=rpm 语义对齐:1/rpm*60 秒一令牌。
var rpmGCRA = redis.NewScript(`
local key    = KEYS[1]
local now    = tonumber(ARGV[1])
local rpm    = tonumber(ARGV[2])
local interval = 60 / rpm
local burst    = rpm * interval
local tat = tonumber(redis.call('GET', key))
if tat == nil or tat < now - burst then tat = now - burst end
local new_tat = tat + interval
if new_tat <= now then
  redis.call('SET', key, now + interval, 'PX', math.ceil(burst * 2 * 1000))
  return {1, 0}
end
if new_tat - burst > now then
  redis.call('SET', key, tat, 'PX', math.ceil(burst * 2 * 1000))
  local retry = new_tat - burst - now
  return {0, retry}
end
redis.call('SET', key, new_tat, 'PX', math.ceil(burst * 2 * 1000))
return {1, 0}
`)

// tpmAllowLua 60 槽窗口求和:60 个槽键全部经 KEYS 显式声明(redis cluster 兼容,
// 同 hash tag 前缀保证同槽),ARGV[1]=当前 unix 秒。返回窗口总量。
var tpmAllowLua = redis.NewScript(`
local total = 0
for i = 1, 60 do
  local v = redis.call('GET', KEYS[i])
  if v then total = total + tonumber(v) end
end
return total
`)

const (
	tpmKeyFmt      = "muniu:tpm:{%s}:%d" // 每秒槽(hash tag 保证 cluster 同槽)
	rpmKeyFmt      = "muniu:rpm:%s"
	tpmKeyTTL      = 61 * time.Second
	tpmSlotsWindow = 60              // 窗口槽数(与 inmem tpmSlots 同义)
	probeEvery     = 5 * time.Second // 降级态下 redis 探测间隔
	probeTimeout   = 500 * time.Millisecond
)

// RedisBackend redis 共享计数后端。
type RedisBackend struct {
	rdb *redis.Client

	fallbackRPM *Limiter // 降级兜底(进程内,恢复后不回迁——短窗口误差可接受)
	fallbackTPM *TPM

	degraded     atomic.Bool
	lastProbe    atomic.Int64 // unix nano
	logger       *slog.Logger
	probeTimeout time.Duration
}

// NewRedisBackend 构造(addr 形态 host:port);连通性惰性验证(首操作失败即降级)。
func NewRedisBackend(addr string, logger *slog.Logger) *RedisBackend {
	return &RedisBackend{
		rdb:          redis.NewClient(&redis.Options{Addr: addr, DialTimeout: probeTimeout, ReadTimeout: probeTimeout, WriteTimeout: probeTimeout}),
		fallbackRPM:  New(),
		fallbackTPM:  NewTPM(),
		logger:       logger,
		probeTimeout: probeTimeout,
	}
}

// Degraded 降级状态。
func (b *RedisBackend) Degraded() bool { return b.degraded.Load() }

// Close 释放 redis 连接。
func (b *RedisBackend) Close() error { return b.rdb.Close() }

// AllowRPM GCRA 判定;失败降级内嵌令牌桶。
func (b *RedisBackend) AllowRPM(keyID string, rpm int64, now time.Time) (bool, time.Duration) {
	if b.degraded.Load() {
		b.maybeProbe()
		if b.degraded.Load() {
			return b.fallbackRPM.Allow(keyID, rpm)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	res, err := rpmGCRA.Run(ctx, b.rdb, []string{fmt.Sprintf(rpmKeyFmt, keyID)},
		float64(now.UnixMilli())/1000.0, float64(rpm)).Result()
	if err != nil {
		b.enterDegraded(err)
		return b.fallbackRPM.Allow(keyID, rpm)
	}
	vals, ok := res.([]interface{})
	if !ok || len(vals) < 2 {
		b.enterDegraded(fmt.Errorf("unexpected GCRA reply"))
		return b.fallbackRPM.Allow(keyID, rpm)
	}
	allow, _ := vals[0].(int64)
	retrySec, _ := vals[1].(int64)
	if f, isF := vals[1].(float64); isF {
		retrySec = int64(f)
	}
	return allow == 1, time.Duration(retrySec) * time.Second
}

// AllowTPM 60 槽窗口求和判定;失败降级内嵌环窗。
func (b *RedisBackend) AllowTPM(keyID string, limit int64, now time.Time) bool {
	if limit <= 0 {
		return true // 不限(与 inmem 同契约)
	}
	if b.degraded.Load() {
		b.maybeProbe()
		if b.degraded.Load() {
			return b.fallbackTPM.Allow(keyID, limit, now)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	base := now.Unix()
	keys := make([]string, tpmSlotsWindow)
	for i := range keys {
		keys[i] = fmt.Sprintf(tpmKeyFmt, keyID, base-int64(i))
	}
	total, err := tpmAllowLua.Run(ctx, b.rdb, keys, base).Int64()
	if err != nil {
		b.enterDegraded(err)
		return b.fallbackTPM.Allow(keyID, limit, now)
	}
	return total < limit
}

// RecordTPM 完成记账;失败静默降级(记账丢失窗口 ≤ 降级期,控制面聚合兜底)。
func (b *RedisBackend) RecordTPM(keyID string, tokens int64, now time.Time) {
	if tokens <= 0 {
		return
	}
	if b.degraded.Load() {
		b.fallbackTPM.Record(keyID, tokens, now)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	key := fmt.Sprintf(tpmKeyFmt, keyID, now.Unix())
	pipe := b.rdb.TxPipeline()
	pipe.IncrBy(ctx, key, tokens)
	pipe.Expire(ctx, key, tpmKeyTTL)
	if _, err := pipe.Exec(ctx); err != nil {
		b.enterDegraded(err)
		b.fallbackTPM.Record(keyID, tokens, now)
	}
}

// enterDegraded 进入降级(一次性告警),回退判定已在调用方完成。
func (b *RedisBackend) enterDegraded(err error) {
	if b.degraded.CompareAndSwap(false, true) {
		if b.logger != nil {
			b.logger.Warn("ratelimit redis 后端不可达,降级实例自治(恢复将自动探测)", "err", err)
		}
		b.lastProbe.Store(time.Now().UnixNano())
	}
}

// maybeProbe 降级态下按间隔试 PING,成功即回共享视图。
func (b *RedisBackend) maybeProbe() {
	now := time.Now().UnixNano()
	last := b.lastProbe.Load()
	if now-last < int64(probeEvery) {
		return
	}
	if !b.lastProbe.CompareAndSwap(last, now) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	if err := b.rdb.Ping(ctx).Err(); err == nil {
		b.degraded.Store(false)
		if b.logger != nil {
			b.logger.Info("ratelimit redis 后端恢复,回到共享视图")
		}
	}
}

// Record 满足 TPMRecorder(proxy 记账回填形态);与 RecordTPM 等价。
func (b *RedisBackend) Record(keyID string, tokens int64, now time.Time) {
	b.RecordTPM(keyID, tokens, now)
}
