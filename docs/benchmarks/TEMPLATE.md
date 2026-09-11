# 压测报告:<日期>(T5.4 模板,复制后填写)

## 方法
- 被测:muniu <版本/commit>,配置 <压测 yaml 要点>;
- 负载:oha,并发 100/500/1000/5000,每档 20×并发数;
- 假上游:scripts/bench/fake_upstream.py(20 chunks × 10ms 固定延迟);
- 对照组:one-api <版本>,docker 同机同剧本;
- 环境:机型/CPU/内存/OS/负载与被测是否同机。

## 原始数据
| 档位 | 指标 | muniu | one-api | 增量口径 |
|---|---|---|---|---|
| 100 | 首 token p50/p99 | | | |
| 500 | 首 token p50/p99 | | | |
| 1000 | 首 token p50/p99 | | | |
| 5000 | 首 token p50/p99 | | | |

每 chunk 转发开销(对照 BenchmarkSSEForward)、峰值 RSS、goroutine 数:<填>

## 结论
<关键数字与解读,关键数字回填 README>

## 局限
<同机争抢/上游瓶颈/样本量等,如实声明>
