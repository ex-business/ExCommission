package domain

import (
	"testing"
	"time"
)

func TestRoutingUsesBusinessTimeAndImmutableSchedules(t *testing.T) {
	now := time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)
	first := Rule{ID: "one", Source: "s", Kind: "BET_SETTLED", Engine: General, EffectiveFrom: now.Add(time.Hour)}
	second := first
	second.ID = "two"
	second.Engine = Affiliate
	second.EffectiveFrom = now.Add(2 * time.Hour)
	config := Config{Rules: []Rule{second, first}}
	if err := ValidateChange(Config{}, config, now); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		at     time.Time
		engine string
		found  bool
	}{{now, "", false}, {first.EffectiveFrom, General, true}, {second.EffectiveFrom.Add(-time.Nanosecond), General, true}, {second.EffectiveFrom, Affiliate, true}} {
		got, ok := config.Select("s", "BET_SETTLED", tt.at)
		if ok != tt.found || got.Engine != tt.engine {
			t.Fatalf("时间边界错误: %+v %v", got, ok)
		}
	}
	if _, ok := config.Select("other", "BET_SETTLED", second.EffectiveFrom); ok {
		t.Fatal("来源不匹配不得使用规则")
	}
	duplicate := second
	duplicate.ID = "three"
	if (Config{Rules: []Rule{second, duplicate}}).Validate() == nil {
		t.Fatal("同生效点冲突未拒绝")
	}
	old := Config{Rules: []Rule{first}}
	if ValidateChange(old, Config{}, now) == nil {
		t.Fatal("不可删除旧规则")
	}
	changed := first
	changed.Engine = Affiliate
	if ValidateChange(old, Config{Rules: []Rule{changed, second}}, now) == nil {
		t.Fatal("不可覆盖旧规则")
	}
	if ValidateChange(Config{}, old, first.EffectiveFrom) == nil {
		t.Fatal("审批已错过生效时间应拒绝")
	}
}
