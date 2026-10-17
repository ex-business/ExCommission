package domain

import (
	"fmt"
	"math/big"
	"testing"
)

func TestExtendedCommissionActualMatchesPreview(t *testing.T) {
	c := config()
	c.Levels = []Level{{Number: 0, Turnover: "0"}}
	c.Commission = &CommissionPolicy{Depth: 4, Grades: []CommissionGradePolicy{{Level: 0, Layers: []CommissionLayerRule{
		{Algorithm: "PROPORTIONAL", Rate: 100000},
		{Algorithm: "FIXED", Fixed: "77"},
		{Algorithm: "TIERED", Tiers: []CommissionTier{{From: "0", To: "100", Rate: 100000}, {From: "100", Rate: 200000}}},
		{Algorithm: "FORMULA", Cap: "99", Formula: &Formula{Op: "DIV", Args: []Formula{{Op: "VOLUME"}, {Op: "CONST", Value: "2"}}}},
	}}}}
	day := at("2026-10-17T00:00:00Z")
	regs := []Registration{{"root", "", day}, {"a", "root", day}, {"b", "a", day}, {"c", "b", day}, {"d", "c", day}}
	facts := []Fact{}
	for _, id := range []string{"a", "b", "c", "d"} {
		facts = append(facts, fact(id, id, "BET_SETTLED", "200", "200", "SPORTS", "2026-10-17T01:00:00Z"))
	}
	actual, e := CalculateDay(c, "root", day, regs, facts)
	if e != nil {
		t.Fatal(e)
	}
	preview, e := EstimateCommissionLayers(c, 0, []string{"200", "200", "200", "200"})
	if e != nil {
		t.Fatal(e)
	}
	if actual.Amount != "226" || actual.Amount != preview.Total || len(actual.Layers) != 4 {
		t.Fatalf("日结与预估不一致 %+v %+v", actual, preview)
	}
	sum := big.NewInt(0)
	for _, p := range actual.Contributions {
		n, _ := Money(p.Amount, false)
		sum.Add(sum, n)
	}
	if sum.String() != actual.Amount {
		t.Fatal("贡献分配不守恒")
	}
	ownership := []Attribution{}
	for i, f := range facts {
		chain := []string{}
		for n := i; n >= 0; n-- {
			chain = append(chain, regs[n].UserID)
		}
		ownership = append(ownership, Attribution{FactID: f.ID, ExtendedAncestors: chain})
	}
	attributed, e := CalculateAttributedDay(c, "root", day, regs, facts, ownership)
	if e != nil || attributed.Amount != actual.Amount {
		t.Fatal(attributed, e)
	}
	if _, e := EstimateCommission(c, 0, [3]string{"200", "200", "200"}); e == nil {
		t.Fatal("扩展方案允许缺层计算")
	}
}
func TestCommissionFormulaAndRulesBoundaries(t *testing.T) {
	v := big.NewInt(9007199254740993)
	rules := []CommissionLayerRule{
		{Algorithm: "FORMULA", Formula: &Formula{Op: "DIV", Args: []Formula{{Op: "VOLUME"}, {Op: "CONST", Value: "0"}}}},
		{Algorithm: "FORMULA", Formula: &Formula{Op: "SUB", Args: []Formula{{Op: "CONST", Value: "0"}, {Op: "VOLUME"}}}},
		{Algorithm: "FORMULA", Formula: &Formula{Op: "EXEC", Value: "script"}},
		{Algorithm: "TIERED", Tiers: []CommissionTier{{From: "1", Rate: 100}}},
		{Algorithm: "PROPORTIONAL", Rate: 1000001},
	}
	for _, r := range rules {
		if _, e := r.Calculate(v, 1, 1); e == nil {
			t.Fatalf("无效公式未拒绝 %+v", r)
		}
	}
	r := CommissionLayerRule{Algorithm: "FORMULA", Formula: &Formula{Op: "VOLUME"}}
	n, e := r.Calculate(v, 0, 1)
	if e != nil || n.String() != v.String() {
		t.Fatal("公式损失大整数精度", n, e)
	}
	fixed := CommissionLayerRule{Algorithm: "FIXED", Fixed: "100"}
	n, e = fixed.Calculate(big.NewInt(0), 0, 1)
	if e != nil || n.Sign() != 0 {
		t.Fatal("空业务量产生固定奖励")
	}
}

func TestCommissionDepthBoundaryAndNonlinearShares(t *testing.T) {
	c := config()
	c.Levels = []Level{{Number: 0, Turnover: "0"}}
	layers := make([]CommissionLayerRule, 64)
	for i := range layers {
		layers[i] = CommissionLayerRule{Algorithm: "PROPORTIONAL", Rate: 10000}
	}
	c.Commission = &CommissionPolicy{Depth: 64, Grades: []CommissionGradePolicy{{Level: 0, Layers: layers}}}
	day := at("2026-10-17T00:00:00Z")
	regs := []Registration{{"root", "", day}}
	parent := "root"
	for i := 1; i <= 65; i++ {
		id := fmt.Sprintf("member-%02d", i)
		regs = append(regs, Registration{id, parent, day})
		parent = id
	}
	facts := []Fact{fact("at-limit", "member-64", "BET_SETTLED", "200", "200", "SPORTS", "2026-10-17T01:00:00Z"), fact("past-limit", "member-65", "BET_SETTLED", "999", "999", "SPORTS", "2026-10-17T01:00:00Z")}
	actual, e := CalculateDay(c, "root", day, regs, facts)
	if e != nil || actual.Amount != "2" || len(actual.Layers) != 64 || actual.Layers[63].Volume != "200" {
		t.Fatal("64层边界错误", actual, e)
	}
	c.Commission.Depth = 65
	if c.Normalize() == nil {
		t.Fatal("未拒绝65层")
	}
	rules := []CommissionLayerRule{{Algorithm: "FIXED", Fixed: "2"}, {Algorithm: "TIERED", Tiers: []CommissionTier{{From: "0", Rate: 750000}}}, {Algorithm: "FORMULA", Formula: &Formula{Op: "CONST", Value: "2"}}, {Algorithm: "PROPORTIONAL", Rate: 1000000, Cap: "2"}}
	for _, rule := range rules {
		c.Commission = &CommissionPolicy{Depth: 1, Grades: []CommissionGradePolicy{{Level: 0, Layers: []CommissionLayerRule{rule}}}}
		regs = []Registration{{"root", "", day}, {"a", "root", day}, {"b", "root", day}, {"c", "root", day}}
		facts = []Fact{}
		for _, id := range []string{"a", "b", "c"} {
			facts = append(facts, fact(id, id, "BET_SETTLED", "1", "1", "SPORTS", "2026-10-17T01:00:00Z"))
		}
		result, e := CalculateDay(c, "root", day, regs, facts)
		if e != nil || result.Amount != "2" {
			t.Fatal(rule, result, e)
		}
		amounts := map[string]string{}
		total := big.NewInt(0)
		for _, p := range result.Contributions {
			amounts[p.UserID] = p.Amount
			n, _ := Money(p.Amount, false)
			total.Add(total, n)
		}
		if total.String() != result.Amount || amounts["a"] != "1" || amounts["b"] != "1" || amounts["c"] != "0" {
			t.Fatal("非线性分摊不守恒或余数排序不稳定", rule, amounts)
		}
	}
}

func TestFormulaPublicationProvesSafeInputRange(t *testing.T) {
	unsafe := []Formula{
		{Op: "DIV", Args: []Formula{{Op: "VOLUME"}, {Op: "MEMBERS"}}},
		{Op: "SUB", Args: []Formula{{Op: "VOLUME"}, {Op: "CONST", Value: "100"}}},
		{Op: "MUL", Args: []Formula{{Op: "VOLUME"}, {Op: "CONST", Value: "2"}}},
	}
	for _, f := range unsafe {
		if (CommissionLayerRule{Algorithm: "FORMULA", Formula: &f}).Validate() == nil {
			t.Fatal("未拒绝可能除零、负值或溢出的公式", f)
		}
	}
	f := Formula{Op: "MAX", Args: []Formula{{Op: "CONST", Value: "0"}, {Op: "SUB", Args: []Formula{{Op: "VOLUME"}, {Op: "CONST", Value: "100"}}}}}
	r := CommissionLayerRule{Algorithm: "FORMULA", Formula: &f}
	if e := r.Validate(); e != nil {
		t.Fatal("安全差值封底公式被拒绝", e)
	}
	f = Formula{Op: "MUL", Args: []Formula{{Op: "VOLUME"}, {Op: "CONST", Value: "2"}}}
	r = CommissionLayerRule{Algorithm: "FORMULA", Formula: &f, Cap: "999"}
	if e := r.Validate(); e != nil {
		t.Fatal("已封顶公式被拒绝", e)
	}
}
