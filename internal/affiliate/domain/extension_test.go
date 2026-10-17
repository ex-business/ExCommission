package domain

import "testing"

func TestConfigExtensionPreservesLegacyAndAllowsVersionedChanges(t *testing.T) {
	old := config()
	next := config()
	next.Steps[0].Amount = "6000"
	next.Steps[0].Members = 2
	if next.ValidateChange(old) == nil {
		t.Fatal("旧模式不得改写阶梯")
	}
	next.Extension = &Extension{DisabledEntry: "HIDE", EditableSteps: true, MaxLevels: 300, MaxSteps: 300}
	if e := next.Normalize(); e != nil {
		t.Fatal(e)
	}
	if e := next.ValidateChange(old); e != nil {
		t.Fatal("配置启用新版本编辑应允许", e)
	}
	next.GradePeriod = "MONTH"
	if next.ValidateChange(old) == nil {
		t.Fatal("新模式也不能修改统计周期")
	}
}
func TestConfigurableLevelLimitAndInvalidExtension(t *testing.T) {
	c := config()
	c.Levels = nil
	for i := 0; i < 101; i++ {
		c.Levels = append(c.Levels, Level{Number: i, Members: i, Turnover: "0"})
	}
	if c.Normalize() == nil {
		t.Fatal("旧配置仍须保持原100级限制")
	}
	c.Extension = &Extension{DisabledEntry: "PAUSE", MaxLevels: 200, MaxSteps: 200}
	if e := c.Normalize(); e != nil {
		t.Fatal("扩展应支持超过100级", e)
	}
	c.Extension.MaxLevels = 0
	if e := c.Normalize(); e != nil {
		t.Fatal("不限数量模式失败", e)
	}
	for _, x := range []Extension{{DisabledEntry: "UNKNOWN", MaxLevels: 200, MaxSteps: 200}, {DisabledEntry: "HIDE", MaxLevels: -1, MaxSteps: 200}, {DisabledEntry: "HIDE", MaxLevels: 200, MaxSteps: 10001}} {
		c.Extension = &x
		if c.Normalize() == nil {
			t.Fatal("无效扩展未拒绝", x)
		}
	}
}
