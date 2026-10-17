package domain

import (
	"strings"
	"testing"
	"time"
)

func TestConfigurableRiskPeriodsDimensionsAndScope(t *testing.T) {
	c := config()
	start := at("2026-10-01T00:00:00Z")
	hash := strings.Repeat("a", 64)
	makeFact := func(id, user string, when time.Time) Fact {
		return Fact{ID: id, UserID: user, Kind: "BET_SETTLED", Currency: "MXN", Amount: "10000", ValidBet: "10000", VenueID: "SPORTS", At: when, Identity: &RiskIdentity{IP: hash, Device: hash, Account: hash}}
	}
	facts := []Fact{makeFact("first", "a", start), makeFact("same-user", "a", start.Add(time.Hour)), makeFact("second", "b", start.Add(2*time.Hour)), makeFact("next-day", "b", start.AddDate(0, 0, 1))}
	relevant := map[string]bool{"first": true, "same-user": true, "second": true, "next-day": true}
	for _, dimension := range []string{"IP", "DEVICE", "ACCOUNT"} {
		for _, period := range []string{"DAY", "WEEK", "MONTH", "LIFETIME", "CUSTOM_DAYS"} {
			r := RiskRule{ID: "r1", Enabled: true, Dimension: dimension, Period: period, Scope: "SITE", Missing: "DENY", From: start}
			if period == "CUSTOM_DAYS" {
				r.Days = 2
			}
			c.RiskRules = []RiskRule{r}
			if e := c.Normalize(); e != nil {
				t.Fatal(e)
			}
			excluded, _ := CommissionRisk(c, facts, relevant)
			if excluded["first"] || excluded["same-user"] || !excluded["second"] || excluded["next-day"] != (period != "DAY") {
				t.Fatal(dimension, period, excluded)
			}
			// 来源乱序不能改变赢家；代理范围允许其他团队的会员不参与竞争。
			reversed := []Fact{facts[3], facts[2], facts[1], facts[0]}
			again, _ := CommissionRisk(c, reversed, relevant)
			if len(again) != len(excluded) {
				t.Fatal("乱序结果不一致")
			}
			c.RiskRules[0].Scope = "BENEFICIARY"
			_, decisions := CommissionRisk(c, facts, map[string]bool{"second": true})
			if len(decisions) != 0 {
				t.Fatal("范围外会员抢占资格")
			}
		}
	}
	c.RiskRules = []RiskRule{{ID: "missing", Enabled: true, Dimension: "DEVICE", Period: "DAY", Scope: "SITE", Missing: "DENY", From: start}}
	missing := makeFact("missing", "x", start)
	missing.Identity = nil
	excluded, d := CommissionRisk(c, []Fact{missing}, map[string]bool{"missing": true})
	if !excluded["missing"] || d[0].Reason != "IDENTITY_MISSING" {
		t.Fatal(excluded, d)
	}
	c.RiskRules[0].Missing = "ALLOW"
	excluded, _ = CommissionRisk(c, []Fact{missing}, nil)
	if len(excluded) != 0 {
		t.Fatal("缺失放行失效")
	}
	until := start.Add(time.Hour)
	c.RiskRules[0].Missing = "DENY"
	c.RiskRules[0].Until = &until
	missing.At = until
	excluded, _ = CommissionRisk(c, []Fact{missing}, nil)
	if len(excluded) != 0 {
		t.Fatal("结束边界应不生效")
	}
	c.RiskRules[0].Period = "CUSTOM_DAYS"
	if c.Normalize() == nil {
		t.Fatal("自定义天数为0未拒绝")
	}
}
func TestRiskCommissionUsesFilteredVolumeAndAudits(t *testing.T) {
	c := config()
	start := at("2026-10-01T00:00:00Z")
	c.RiskRules = []RiskRule{{ID: "ip-daily", Enabled: true, Dimension: "IP", Period: "DAY", Scope: "SITE", Missing: "DENY", From: start}}
	regs := []Registration{{"root", "", start.Add(-time.Hour)}, {"a", "root", start}, {"b", "root", start}}
	facts := []Fact{fact("a-bet", "a", "BET_SETTLED", "10000", "10000", "SPORTS", "2026-10-01T01:00:00Z"), fact("b-bet", "b", "BET_SETTLED", "10000", "10000", "SPORTS", "2026-10-01T02:00:00Z")}
	for i := range facts {
		facts[i].Identity = &RiskIdentity{IP: strings.Repeat("a", 64)}
	}
	result, e := CalculateDay(c, "root", start, regs, facts)
	if e != nil || result.Turnover != "10000" || result.Amount != "25" || len(result.RiskDecisions) != 1 {
		t.Fatal(result, e)
	}
	c.RiskRules = nil
	legacy, e := CalculateDay(c, "root", start, regs, facts)
	if e != nil || legacy.Turnover != "20000" || legacy.Amount != "50" {
		t.Fatal(legacy, e)
	}
}
func TestRiskDayWindowDSTAndSharedWinnersAcrossGates(t *testing.T) {
	c := config()
	c.Timezone = "America/New_York"
	start := at("2026-03-07T05:00:00Z")
	r := RiskRule{ID: "custom", Enabled: true, Dimension: "IP", Period: "CUSTOM_DAYS", Days: 2, Scope: "SITE", Missing: "DENY", From: start}
	loc := c.Location()
	if r.bucket(at("2026-03-08T18:00:00Z"), loc) != r.bucket(start, loc) || r.bucket(at("2026-03-09T04:00:00Z"), loc) == r.bucket(start, loc) {
		t.Fatal("夏令时窗口错位")
	}
	c.Timezone = "UTC"
	c.RiskRules = []RiskRule{{ID: "global", Enabled: true, Dimension: "IP", Period: "LIFETIME", Scope: "SITE", Missing: "DENY", From: start}}
	a := Fact{ID: "old", UserID: "a", Kind: "BET_SETTLED", Currency: "MXN", Amount: "10000", ValidBet: "10000", VenueID: "SPORTS", At: start, Identity: &RiskIdentity{IP: strings.Repeat("a", 64)}}
	b := a
	b.ID = "new"
	b.UserID = "b"
	b.At = start.AddDate(0, 0, 1)
	b.Identity = &RiskIdentity{IP: strings.Repeat("A", 64)}
	regs := []Registration{{"root", "", start}, {"a", "root", start}, {"b", "root", start}}
	out, e := CalculateRiskDay(c, "root", b.At, regs, []Fact{b}, nil, []Fact{a, b})
	if e != nil || out.Amount != "0" || len(out.RiskDecisions) != 1 {
		t.Fatal("重开后全站赢家不一致", out, e)
	}
}
func TestBeneficiaryLifetimeUsesHistoricalOwnership(t *testing.T) {
	c := config()
	start := at("2026-10-01T00:00:00Z")
	c.RiskRules = []RiskRule{{ID: "device-life", Enabled: true, Dimension: "DEVICE", Period: "LIFETIME", Scope: "BENEFICIARY", Missing: "DENY", From: start}}
	a := Fact{ID: "old", UserID: "a", Kind: "BET_SETTLED", Currency: "MXN", Amount: "10000", ValidBet: "10000", VenueID: "SPORTS", At: start, Identity: &RiskIdentity{Device: strings.Repeat("c", 64)}}
	b := a
	b.ID = "new"
	b.UserID = "b"
	b.At = start.AddDate(0, 0, 1)
	regs := []Registration{{"root", "", start}, {"a", "root", start}, {"b", "root", start}}
	ownership := []Attribution{{FactID: "old", Ancestors: [3]string{"root"}}, {FactID: "new", Ancestors: [3]string{"root"}}}
	out, e := CalculateRiskDay(c, "root", b.At, regs, []Fact{b}, ownership, []Fact{a, b})
	if e != nil || out.Amount != "0" || len(out.RiskDecisions) != 1 {
		t.Fatal(out, e)
	}
}
