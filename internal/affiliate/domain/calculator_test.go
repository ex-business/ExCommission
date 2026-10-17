package domain

import "testing"

func TestCalculatorMatchesDailyRoundingAndGrade(t *testing.T) {
	c := config()
	day := at("2026-10-17T00:00:00Z")
	regs := []Registration{{"root", "", day}, {"a", "root", day}, {"b", "a", day}, {"c", "b", day}}
	facts := []Fact{fact("one", "a", "BET_SETTLED", "12345", "12345", "SPORTS", "2026-10-17T01:00:00Z"), fact("two", "b", "BET_SETTLED", "23456", "23456", "SPORTS", "2026-10-17T01:00:00Z"), fact("three", "c", "BET_SETTLED", "34567", "34567", "SPORTS", "2026-10-17T01:00:00Z")}
	actual, e := CalculateDay(c, "root", day, regs, facts)
	if e != nil {
		t.Fatal(e)
	}
	preview, e := EstimateCommission(c, actual.QualifiedMembers, [3]string{"12345", "23456", "34567"})
	if e != nil || preview.Total != actual.Amount || preview.Amounts != actual.LayerAmounts || preview.Level != actual.Level {
		t.Fatal(preview, actual, e)
	}
	for _, values := range [][3]string{{"-1", "0", "0"}, {"1.5", "0", "0"}, {"99999999999999999999999999999999999999", "1", "0"}} {
		if _, e = EstimateCommission(c, 0, values); e == nil {
			t.Fatal("越界金额未拒绝")
		}
	}
}
