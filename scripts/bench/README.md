# 压测方法(T5.4)

## 环境
- 同机:被测 muniu + 假上游(`fake_upstream.py`,固定延迟 20 chunks × 10ms)+ 负载机(oha)。
- 对照组:one-api 以 docker 同机同端口段部署,同一假上游剧本。

## 指标
- 首 token 增量延迟 p50/p99(假上游首帧延迟固定,网关增量开销 = 实测 − 上游基线);
- 每 chunk 转发开销(基准 `BenchmarkSSEForward` 交叉验证);
- 峰值内存(RSS,`ps -o rss`)与 goroutine 数(`GET /debug/pprof/goroutine?debug=0` 计数)。

## 档位
并发 100 / 500 / 1000 / 5000,每档 20×并发请求数。

## 执行
```bash
python3 scripts/bench/fake_upstream.py 59990 20 0.01 &
make build && ./bin/muniu -c scripts/bench/muniu-bench.yaml &
MUNIU_KEY=mk-… bash scripts/bench/run_oha.sh
```

## 局限(如实声明)
- 单机负载机与被测同机,CPU 争抢会使绝对数字偏高;只做相对对比(同机同剧本);
- 假上游为 ThreadingHTTPServer,5000 并发档上游自身可能先成为瓶颈,数字以「增量」口径解读。
