package domain

import "testing"

func TestIdentityCannotBeBypassedByChangingEventNumber(t *testing.T) {
	a := Claim{Source: "s", EventID: "e1", BusinessID: "b", Kind: "BET_SETTLED", Engine: General}
	b := a
	b.EventID = "e2"
	b.Engine = Affiliate
	if a.Keys()[1] != b.Keys()[1] || a.Keys()[0] == b.Keys()[0] {
		t.Fatal("同业务换事件编号应保持业务唯一键")
	}
	b = a
	b.Kind = "DEPOSIT_SUCCEEDED"
	if a.Keys()[1] == b.Keys()[1] {
		t.Fatal("不同业务类型不可混淆")
	}
	b = a
	b.Source = "other"
	if a.Keys() == b.Keys() {
		t.Fatal("不同来源应隔离")
	}
}
func TestIdentityRejectsAmbiguousOrUnknownClaims(t *testing.T) {
	c := Claim{Source: "s", EventID: "e", BusinessID: "b", Kind: "BET_SETTLED", Engine: General}
	if c.Validate() != nil {
		t.Fatal("合法归属应通过")
	}
	for _, mutate := range []func(*Claim){func(v *Claim) { v.Engine = "unknown" }, func(v *Claim) { v.Kind = "" }, func(v *Claim) { v.Source = " s" }, func(v *Claim) { v.EventID = "" }} {
		v := c
		mutate(&v)
		if v.Validate() == nil {
			t.Fatal("错误归属未拒绝")
		}
	}
	a := Claim{Source: "s|x", EventID: "y", BusinessID: "b", Kind: "K", Engine: General}
	b := a
	b.Source = "s"
	b.EventID = "x|y"
	if a.Keys()[0] == b.Keys()[0] {
		t.Fatal("字段分隔符不得碰撞")
	}
}
