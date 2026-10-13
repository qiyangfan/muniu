package metering

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"sync/atomic"
)

// ImagePrice 与 token 价目平行，内部金额均为本位币 credit/张。
type ImagePrice struct {
	Currency       string
	DefaultCredits int64
	Tiers          map[string]int64
}

// ValidateImagePrice 在配置和数据库目录重建时使用同一校验。
func ValidateImagePrice(p ImagePrice) error {
	if !ValidSettlement(p.Currency) || p.DefaultCredits <= 0 {
		return errors.New("images: 币种须为 CNY/USD，兜底价须大于零")
	}
	for k, v := range p.Tiers {
		a, b, ok := strings.Cut(k, "|")
		if !ok || a == "" || b == "" || strings.Contains(b, "|") || v <= 0 {
			return fmt.Errorf("images: 档位 %q 非法或单价非正数", k)
		}
	}
	return nil
}

// Cost 按请求张数精确计价，乘法溢出拒绝而不饱和或回绕。
func (p ImagePrice) Cost(tier string, n int64) (int64, error) {
	price := p.DefaultCredits
	if v, ok := p.Tiers[tier]; ok {
		price = v
	}
	if n <= 0 || price <= 0 || n > math.MaxInt64/price {
		return 0, errors.New("images: 成本超出整数范围")
	}
	return price * n, nil
}

// SettleImagePrice 复用既有汇率与取整契约，在加载期换算全部档位。
func SettleImagePrice(p ImagePrice, settlement string, fx int64) (ImagePrice, error) {
	if err := ValidateImagePrice(p); err != nil {
		return ImagePrice{}, err
	}
	convert := func(v int64) (int64, error) {
		q, ok := SettlePrice(Pricing{Currency: p.Currency, InputCreditsPerM: v}, settlement, fx)
		if !ok {
			return 0, errors.New("images: 汇率缺失或换算溢出")
		}
		return q.InputCreditsPerM, nil
	}
	d, err := convert(p.DefaultCredits)
	if err != nil {
		return ImagePrice{}, err
	}
	out := ImagePrice{Currency: settlement, DefaultCredits: d, Tiers: map[string]int64{}}
	for k, v := range p.Tiers {
		c, err := convert(v)
		if err != nil {
			return ImagePrice{}, err
		}
		out.Tiers[k] = c
	}
	return out, nil
}

type imageSnapshot struct{ prices map[string]ImagePrice }

// ImagePriceDirectory 只发布不可变快照；预算与落账共享请求入口取得的同一份价格。
type ImagePriceDirectory struct{ state atomic.Pointer[imageSnapshot] }

// NewImagePriceDirectory 构建空目录亦合法。
func NewImagePriceDirectory(prices map[string]ImagePrice) *ImagePriceDirectory {
	d := &ImagePriceDirectory{}
	d.Replace(prices)
	return d
}

// Replace 深拷贝目录，避免调用方后续改 map 污染热路径。
func (d *ImagePriceDirectory) Replace(prices map[string]ImagePrice) {
	copyPrices := make(map[string]ImagePrice, len(prices))
	for a, p := range prices {
		tiers := make(map[string]int64, len(p.Tiers))
		for k, v := range p.Tiers {
			tiers[k] = v
		}
		p.Tiers = tiers
		copyPrices[a] = p
	}
	d.state.Store(&imageSnapshot{prices: copyPrices})
}

// Get 返回独立值，nil 目录与未配置别名都表示无价目。
func (d *ImagePriceDirectory) Get(alias string) *ImagePrice {
	if d == nil {
		return nil
	}
	s := d.state.Load()
	if s == nil {
		return nil
	}
	p, ok := s.prices[alias]
	if !ok {
		return nil
	}
	return &p
}

// ParseImagePrice 人类报价只解析一次，配置与管理 API 共用零浮点校验。
func ParseImagePrice(currency, def string, tiers map[string]string) (ImagePrice, error) {
	d, err := ParsePrice(def)
	if err != nil {
		return ImagePrice{}, err
	}
	p := ImagePrice{Currency: currency, DefaultCredits: d, Tiers: map[string]int64{}}
	for k, v := range tiers {
		n, err := ParsePrice(v)
		if err != nil {
			return ImagePrice{}, err
		}
		p.Tiers[k] = n
	}
	return p, ValidateImagePrice(p)
}
