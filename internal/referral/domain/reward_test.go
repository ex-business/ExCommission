package domain

import (
	q "github.com/ex-business/ExCommission/internal/qualification/domain"
	"testing"
	"time"
)

func TestUnlockIsPermanentAcrossPeriodsAndClaim(t *testing.T) {
	at := time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC)
	r := Rule{Definition: Definition{ID: "r", Tiers: []Tier{{ID: "one", Required: 1, Amount: "500"}, {ID: "two", Required: 2, Amount: "800"}}}}
	p := Advance(r, Empty(r, "A"), q.StatisticsResult{Count: 2, AgentIDs: []string{"B", "C"}}, at)
	p.Tiers[0].Status = "CLAIMED"
	p.Tiers[0].ClaimedAt = &at
	p.Tiers[0].ClaimID = ClaimID("r", "A", "one")
	later := at.AddDate(0, 0, 1)
	next := Advance(r, p, q.StatisticsResult{Count: 0}, later)
	if next.DirectCount != 0 || next.Tiers[0].Status != "CLAIMED" || next.Tiers[1].Status != "CLAIMABLE" || next.Tiers[1].Evidence.Count != 2 {
		t.Fatal(next)
	}
	backdated := Advance(r, next, q.StatisticsResult{Count: 2}, at)
	if !backdated.RefreshedAt.Equal(later) || backdated.DirectCount != 0 || backdated.Tiers[0].ClaimID != p.Tiers[0].ClaimID {
		t.Fatal("旧事件不应回退当前进度", backdated)
	}
}
func TestTierDefinitionRejectsInvalidAmountsAndDuplicates(t *testing.T) {
	base := Definition{ID: "r", Name: "推荐奖励", PlanID: "p", Source: "sandbox", PolicyID: "q", Currency: "USD", Tiers: []Tier{{ID: "a", Required: 1, Amount: "1"}}}
	for _, amount := range []string{"0", "-1", "1.5", "01", "100000000000000000000000000000000000000"} {
		d := base
		d.Tiers = []Tier{{ID: "a", Required: 1, Amount: amount}}
		if err := Normalize(&d); err == nil {
			t.Fatal(amount)
		}
	}
	for _, tiers := range [][]Tier{{{ID: "a", Required: 1, Amount: "1"}, {ID: "a", Required: 2, Amount: "2"}}, {{ID: "a", Required: 1, Amount: "1"}, {ID: "b", Required: 1, Amount: "2"}}, {{ID: "a", Required: 0, Amount: "1"}}} {
		d := base
		d.Tiers = tiers
		if err := Normalize(&d); err == nil {
			t.Fatal(tiers)
		}
	}
}
