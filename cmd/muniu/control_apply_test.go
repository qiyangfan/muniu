// 快照图片价目投影：nil 保持、空数组清空、条目解析换算与合法性拒绝。
package main

import (
	"testing"
	"time"

	"github.com/solvela/muniu/internal/control"
)

func TestImagePricingRecords_nil与解析(t *testing.T) {
	applier := &snapshotApplier{wiring: controlWiring{settlement: "CNY", fxMilli: 7200}}
	if records, tables, replace, err := applier.imagePricingRecords(nil, time.Now()); err != nil ||
		replace || records != nil || tables != nil {
		t.Fatalf("nil 应保留现状: replace=%v err=%v", replace, err)
	}
	entries := []control.ImagePricingEntry{{
		Alias: " img-a ", Currency: "CNY", Default: "0.50",
		Tiers: map[string]string{"1024x1024|standard": "1.00"},
	}}
	records, tables, replace, err := applier.imagePricingRecords(entries, time.Now())
	if err != nil || !replace || len(records) != 1 || records[0].Alias != "img-a" {
		t.Fatalf("条目应解析并去空白: %+v replace=%v err=%v", records, replace, err)
	}
	if records[0].DefaultCredits <= 0 || records[0].Tiers["1024x1024|standard"] <= 0 {
		t.Fatalf("金额应换算为整数 credit: %+v", records[0])
	}
	if tables["img-a"].DefaultCredits != records[0].DefaultCredits {
		t.Fatalf("本位币目录应与记录一致: %+v", tables["img-a"])
	}
	empty := []control.ImagePricingEntry{}
	records, _, replace, err = applier.imagePricingRecords(empty, time.Now())
	if err != nil || !replace || len(records) != 0 {
		t.Fatalf("空数组应显式清空: %+v replace=%v err=%v", records, replace, err)
	}
	bad := []control.ImagePricingEntry{{Alias: "x", Currency: "CNY", Default: "0"}}
	if _, _, _, err := applier.imagePricingRecords(bad, time.Now()); err == nil {
		t.Fatal("非正价格必须拒绝")
	}
	dup := []control.ImagePricingEntry{
		{Alias: "x", Currency: "CNY", Default: "1"},
		{Alias: " x ", Currency: "CNY", Default: "1"},
	}
	env := control.SnapshotEnvelope{Payload: control.SnapshotPayload{
		Revocations: &control.Revocations{}, Tenants: []control.Tenant{},
		ImagePricing: dup,
	}}
	if err := control.ValidatePayload(env.Payload); err == nil {
		t.Fatal("重复图片别名必须由结构校验拒绝")
	}
}
