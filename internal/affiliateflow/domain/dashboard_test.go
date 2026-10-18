package domain

import (
	"fmt"
	a "github.com/ex-business/ExCommission/internal/affiliate/domain"
	"testing"
	"time"
)

func TestDashboardNewMembersPeriodAndRawDeposits(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	start := now.Add(-12 * time.Hour)
	c := a.Config{Currency: "USD", CurrencyScale: 2, Timezone: "UTC", GradePeriod: "DAY", GradeCondition: a.Threshold{Deposit: "100", Turnover: "100"}}
	regs := []a.Registration{{UserID: "new", ParentID: "root", At: start.Add(time.Hour)}, {UserID: "old", ParentID: "root", At: start.Add(-48 * time.Hour)}, {UserID: "layer2", ParentID: "new", At: start.Add(2 * time.Hour)}, {UserID: "layer3", ParentID: "layer2", At: start.Add(3 * time.Hour)}, {UserID: "layer4", ParentID: "layer3", At: start.Add(4 * time.Hour)}}
	facts := []a.Fact{{UserID: "new", Kind: "DEPOSIT_SUCCEEDED", Currency: "USD", Amount: "100", At: start.Add(time.Hour)}, {UserID: "new", Kind: "BET_SETTLED", Currency: "USD", Amount: "100", ValidBet: "100", VenueID: "SPORTS", At: start.Add(2 * time.Hour)}, {UserID: "old", Kind: "DEPOSIT_SUCCEEDED", Currency: "USD", Amount: "1000", At: start.Add(-24 * time.Hour)}, {UserID: "old", Kind: "BET_SETTLED", Currency: "USD", Amount: "100", ValidBet: "100", VenueID: "SPORTS", At: start.Add(-23 * time.Hour)}}
	for i := range facts {
		facts[i].ID = fmt.Sprint("fact-", i)
		if facts[i].ValidBet == "" {
			facts[i].ValidBet = "0"
		}
	}
	c.VenueIDs = []string{"SPORTS"}
	raw := append(append([]a.Fact{}, facts...), a.Fact{UserID: "new", Kind: "DEPOSIT_SUCCEEDED", Amount: "50", At: start.Add(3 * time.Hour)}, a.Fact{UserID: "old", Kind: "DEPOSIT_SUCCEEDED", Amount: "10", At: start.Add(4 * time.Hour)}, a.Fact{UserID: "layer4", Kind: "DEPOSIT_SUCCEEDED", Amount: "999", At: start.Add(5 * time.Hour)})
	stats, err := TeamDashboardFor(c, "root", now, regs, raw, facts, []Reward{{UserID: "root", Status: "CLAIMED", Amount: "300"}, {UserID: "root", Status: "CLAIMABLE", Amount: "200"}})
	if err != nil {
		t.Fatal(err)
	}
	if stats.TotalReferrals != 4 || stats.TodayReferrals != 3 || stats.TotalQualified != 1 || stats.TodayQualified != 1 || stats.TotalDeposit != "1160" || stats.TodayDeposit != "150" || stats.Earned != "300" {
		t.Fatal(stats)
	}
}

func TestDashboardDoesNotMixProgramCurrencies(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	config := a.Config{Currency: "USD", Timezone: "UTC", GradePeriod: "DAY", GradeCondition: a.Threshold{Deposit: "100", Turnover: "100"}}
	regs := []a.Registration{{UserID: "child", ParentID: "root", At: now.Add(-time.Hour)}}
	raw := []a.Fact{{UserID: "child", Kind: "DEPOSIT_SUCCEEDED", Currency: "USD", Amount: "100", At: now}, {UserID: "child", Kind: "DEPOSIT_SUCCEEDED", Currency: "BTC", Amount: "100000000", At: now}}
	rewards := []Reward{{UserID: "root", Currency: "USD", Amount: "50", Status: "CLAIMED"}, {ProgramID: "new-program", UserID: "root", Currency: "BTC", Amount: "100000000", Status: "CLAIMED"}}
	result, e := TeamDashboardFor(config, "root", now, regs, raw, nil, rewards)
	if e != nil || result.TotalDeposit != "100" || result.Earned != "50" {
		t.Fatal("并行方案混入兼容方案统计", result, e)
	}
}
