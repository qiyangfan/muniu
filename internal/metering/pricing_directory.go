// 运行态价目目录与人类可读格式化(动态改价契约,metering.md「动态价目」):
// 价目是运营态数据(厂商改价是运维动作)——目录由装配期构建、管理面改价后整体原子替换,
// 转发路径只读,零锁竞争;YAML 静态兜底 + DB 运营覆盖的合并语义在装配层(cmd/muniu/pricing.go)。
package metering

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
)

// PricingDirectory 价目目录(进程内单例):Get 转发热路径消费(RLock),Replace 管理面改价后整体换图。
type PricingDirectory struct {
	mu     sync.RWMutex
	tables map[string]*PricingTable
}

// NewPricingDirectory 构建目录(nil map 合法 = 无任何价目,成本记 0)。
func NewPricingDirectory(tables map[string]*PricingTable) *PricingDirectory {
	if tables == nil {
		tables = map[string]*PricingTable{}
	}
	return &PricingDirectory{tables: tables}
}

// Get 取别名气目表;未配置返回 nil(调用方按成本记 0 处理)。nil 目录安全(纯转发场景不装配价目)。
func (d *PricingDirectory) Get(alias string) *PricingTable {
	if d == nil {
		return nil
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.tables[alias]
}

// Replace 整体原子替换(管理面改价/删除后调用;快照语义,在途请求用旧表完成,下一请求生效)。
func (d *PricingDirectory) Replace(tables map[string]*PricingTable) {
	if d == nil {
		return
	}
	if tables == nil {
		tables = map[string]*PricingTable{}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.tables = tables
}

// Aliases 当前目录内全部别名(管理面/诊断用,升序)。
func (d *PricingDirectory) Aliases() []string {
	if d == nil {
		return nil
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	out := make([]string, 0, len(d.tables))
	for a := range d.tables {
		out = append(out, a)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// FormatPrice credit 价目 → 人类可读十进制主单位(≤9 位小数,去尾零);
// FormatPrice(ParsePrice(x)) 恒等往返。表示层只出现主单位,credit 不外露。
func FormatPrice(credits int64) string {
	neg := credits < 0
	if neg {
		credits = -credits
	}
	intPart, frac := credits/creditPerUnit, credits%creditPerUnit
	s := strconv.FormatInt(intPart, 10)
	if frac > 0 {
		s += "." + strings.TrimRight(fmt.Sprintf("%09d", frac), "0")
	}
	if neg {
		s = "-" + s
	}
	return s
}

// FormatMultiplier 基点 → 人类可读倍率(≤4 位小数,去尾零);FormatMultiplier(ParseMultiplier(x)) 恒等往返。
func FormatMultiplier(bp int64) string {
	intPart, frac := bp/scaleDenom, bp%scaleDenom
	s := strconv.FormatInt(intPart, 10)
	if frac > 0 {
		s += "." + strings.TrimRight(fmt.Sprintf("%04d", frac), "0")
	}
	return s
}
