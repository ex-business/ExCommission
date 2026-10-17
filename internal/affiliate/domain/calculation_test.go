package domain

import (
	"encoding/json"
	"math/big"
	"strings"
	"testing"
	"time"
)

func config() Config {
	return Config{Enabled: true, Currency: "MXN", CurrencyScale: 2, Timezone: "UTC", GradePeriod: "DAY", GradeCondition: Threshold{"5000", "10000"}, ReferralCondition: Threshold{"5000", "30000"}, InviteeCondition: Threshold{"10000", "50000"}, InviteeReward: "5000", Levels: []Level{{0, 1, "5000", [3]int{2500, 1500, 500}}, {1, 2, "50000", [3]int{4000, 2500, 1500}}, {2, 5, "500000", [3]int{5000, 3000, 2000}}, {3, 10, "5000000", [3]int{10000, 5000, 2500}}}, Steps: []Step{{1, 1, "5000"}, {2, 3, "8000"}, {3, 8, "18000"}}, VenueIDs: []string{"SPORTS", "LIVE"}}
}
func at(v string) time.Time {
	t, e := time.Parse(time.RFC3339, v)
	if e != nil {
		panic(e)
	}
	return t
}
func fact(id, user, kind, amount, valid, venue, when string) Fact {
	return Fact{ID: id, UserID: user, Kind: kind, Currency: "MXN", Amount: amount, ValidBet: valid, VenueID: venue, At: at(when)}
}
func TestConfigurationRejectsInvalidBusinessBounds(t *testing.T) {
	cases := []struct {
		name   string
		change func(*Config)
	}{
		{"zero deposit", func(c *Config) { c.GradeCondition.Deposit = "0" }},
		{"zero invitee bet", func(c *Config) { c.InviteeCondition.Turnover = "0" }},
		{"duplicate step requirements", func(c *Config) { c.Steps[1].Members = 1 }},
		{"missing level", func(c *Config) { c.Levels[2].Number = 3 }},
		{"negative rate", func(c *Config) { c.Levels[0].Rates[0] = -1 }},
		{"over 100 percent", func(c *Config) { c.Levels[0].Rates[0] = 1000001 }},
		{"decreasing qualification", func(c *Config) { c.Levels[2].Members = 1 }},
		{"same grade boundaries", func(c *Config) { c.Levels[1].Members = 1; c.Levels[1].Turnover = "5000" }},
		{"leading zero money", func(c *Config) { c.InviteeReward = "050" }},
		{"decimal money", func(c *Config) { c.InviteeReward = "1.01" }},
		{"overflow", func(c *Config) { c.InviteeReward = strings.Repeat("9", 39) }},
		{"duplicate venue", func(c *Config) { c.VenueIDs = []string{"SPORTS", "SPORTS"} }},
		{"unknown timezone", func(c *Config) { c.Timezone = "invalid-zone" }},
		{"implicit host timezone", func(c *Config) { c.Timezone = "Local" }},
		{"invalid period", func(c *Config) { c.GradePeriod = "ROLLING" }},
	}
	for _, v := range cases {
		t.Run(v.name, func(t *testing.T) {
			c := config()
			v.change(&c)
			if c.Normalize() == nil {
				t.Fatal("accepted invalid config")
			}
		})
	}
	c := config()
	if e := c.Normalize(); e != nil {
		t.Fatal(e)
	}
	for _, field := range []string{"period", "zone", "currency", "scale", "step"} {
		old := config()
		c := config()
		switch field {
		case "period":
			c.GradePeriod = "MONTH"
		case "zone":
			c.Timezone = "Asia/Shanghai"
		case "currency":
			c.Currency = "USD"
		case "scale":
			c.CurrencyScale = 8
		case "step":
			c.Steps[0].Amount = "9999"
		}
		if c.ValidateChange(old) == nil {
			t.Fatal(field)
		}
	}
}
func TestProgressConsumesDistinctMembersPerStep(t *testing.T) {
	c := config()
	users := []QualifiedUser{}
	base := at("2026-10-01T00:00:00Z")
	for i := 0; i < 12; i++ {
		users = append(users, QualifiedUser{string(rune('a' + i)), base.Add(time.Duration(i) * time.Hour)})
	}
	for _, n := range []int{0, 1, 2, 3, 4, 11, 12} {
		out, e := c.StepProgress(users[:n])
		if e != nil {
			t.Fatal(e)
		}
		sum := 0
		for i, v := range out {
			sum += v.Completed
			want := []int{1, 4, 12}[i]
			if v.CumulativeRequired != want || v.Unlocked != (n >= want) {
				t.Fatalf("n=%d %+v", n, v)
			}
			if v.Unlocked && !v.QualifiedAt.Equal(users[want-1].At) {
				t.Fatal("wrong first qualification")
			}
		}
		if sum != n {
			t.Fatalf("double consumed %d %d", sum, n)
		}
	}
	if _, e := c.StepProgress([]QualifiedUser{users[0], users[0]}); e == nil {
		t.Fatal("duplicate qualified member")
	}
}
func TestLifetimeQualificationBothConditionsAndOrder(t *testing.T) {
	c := config()
	cut := at("2026-10-03T00:00:00Z")
	fs := []Fact{fact("b2", "u", "BET_SETTLED", "20000", "20000", "SPORTS", "2026-10-02T01:00:00Z"), fact("d", "u", "DEPOSIT_SUCCEEDED", "5000", "0", "", "2026-10-01T01:00:00Z"), fact("b1", "u", "BET_SETTLED", "10000", "10000", "LIVE", "2026-10-01T02:00:00Z"), fact("excluded", "u", "BET_SETTLED", "500000", "500000", "POKER", "2026-10-01T03:00:00Z")}
	q, e := Qualify(c.ReferralCondition, fs, c, cut)
	if e != nil {
		t.Fatal(e)
	}
	if q.At == nil || !q.At.Equal(fs[0].At) || q.Deposit != "5000" || q.Turnover != "30000" {
		t.Fatalf("%+v", q)
	}
	q, e = Qualify(c.InviteeCondition, fs, c, cut)
	if e != nil || q.At != nil {
		t.Fatal("invitee shares different thresholds", q, e)
	}
	fs = append(fs, fact("d2", "u", "DEPOSIT_SUCCEEDED", "5000", "0", "", "2026-10-02T02:00:00Z"), fact("b3", "u", "BET_SETTLED", "50000", "50000", "LIVE", "2026-10-02T03:00:00Z"))
	q, e = Qualify(c.ReferralCondition, fs, c, cut)
	if e != nil || !q.At.Equal(fs[0].At) {
		t.Fatal("later fact changed first qualification", q, e)
	}
	q, e = Qualify(c.InviteeCondition, fs, c, cut)
	if e != nil || q.At == nil || !q.At.Equal(fs[len(fs)-1].At) {
		t.Fatal(q, e)
	}
	fs = append(fs, fs[0])
	if _, e = Qualify(c.ReferralCondition, fs, c, cut); e == nil {
		t.Fatal("duplicate event silently inflates progress")
	}
}
func TestNaturalPeriodsAndDST(t *testing.T) {
	c := config()
	c.Timezone = "Australia/Sydney"
	c.GradePeriod = "DAY"
	s, e := c.Day(at("2026-10-04T12:00:00Z"))
	if e.Sub(s) != 23*time.Hour {
		t.Fatal("DST day", s, e)
	}
	c.GradePeriod = "WEEK"
	s, e = c.Period(at("2026-10-04T12:00:00Z"))
	if s.In(c.Location()).Weekday() != time.Monday || e.Sub(s) != 167*time.Hour {
		t.Fatal("natural week", s, e)
	}
	c.GradePeriod = "MONTH"
	s, e = c.Period(at("2026-10-04T12:00:00Z"))
	if s.In(c.Location()).Day() != 1 || e.In(c.Location()).Month() != time.November {
		t.Fatal("natural month", s, e)
	}
	c.GradePeriod = "LIFETIME"
	s, _ = c.Period(at("2026-10-04T12:00:00Z"))
	if !s.IsZero() {
		t.Fatal(s)
	}
	c.Timezone = "UTC"
	if got := c.DueDate(at("2026-10-05T00:59:59Z")); !got.Equal(at("2026-10-03T00:00:00Z")) {
		t.Fatal(got)
	}
	if got := c.DueDate(at("2026-10-05T01:00:00Z")); !got.Equal(at("2026-10-04T00:00:00Z")) {
		t.Fatal(got)
	}
}
func fixture() ([]Registration, []Fact) {
	start := at("2026-09-01T00:00:00Z")
	regs := []Registration{{"A", "", start}, {"B1", "A", start}, {"B2", "A", start}, {"B3", "A", start}, {"C1", "B1", start}, {"C2", "B1", start}, {"C3", "B2", start}, {"D1", "C1", start}, {"D2", "C1", start}, {"D3", "C2", start}, {"E", "D1", start}}
	fs := []Fact{}
	for _, r := range regs {
		fs = append(fs, fact("bet-"+r.UserID, r.UserID, "BET_SETTLED", "100000", "100000", "SPORTS", "2026-10-01T12:00:00Z"), fact("dep-"+r.UserID, r.UserID, "DEPOSIT_SUCCEEDED", "5000", "0", "", "2026-10-01T23:00:00Z"))
	}
	return regs, fs
}
func TestPrototypeThreePhysicalLayersAndFinalGrade(t *testing.T) {
	c := config()
	c.Levels = []Level{{0, 0, "0", [3]int{2500, 1500, 500}}, {1, 1, "0", [3]int{4000, 2500, 1500}}, {2, 2, "0", [3]int{5000, 3000, 2000}}, {3, 3, "0", [3]int{10000, 5000, 2500}}}
	regs, fs := fixture()
	day := at("2026-10-01T00:00:00Z")
	// All deposits arrive at 23:00, after all bets. Every bet nevertheless receives the final grade.
	for _, tc := range []struct {
		id, amount, turnover string
		level                int
	}{{"A", "5250", "900000", 3}, {"B1", "2100", "600000", 2}, {"C1", "1300", "300000", 2}, {"D1", "400", "100000", 1}, {"E", "0", "0", 0}} {
		v, e := CalculateDay(c, tc.id, day, regs, fs)
		if e != nil {
			t.Fatal(e)
		}
		if v.Level != tc.level || v.Amount != tc.amount || v.Turnover != tc.turnover {
			t.Fatalf("%s %+v", tc.id, v)
		}
		sum := big.NewInt(0)
		for _, p := range v.Contributions {
			n, _ := Money(p.Amount, false)
			sum.Add(sum, n)
			if p.UserID == tc.id {
				t.Fatal("self commission")
			}
			if tc.id == "A" && p.UserID == "E" {
				t.Fatal("fourth level included")
			}
		}
		if sum.String() != v.Amount {
			t.Fatal("non-conserving contribution allocation")
		}
	}
}
func TestDailyGradeOnlyDirectQualifiedAndPeriodReset(t *testing.T) {
	c := config()
	regs, fs := fixture()
	day := at("2026-10-01T00:00:00Z")
	v, e := CalculateDay(c, "A", day, regs, fs)
	if e != nil {
		t.Fatal(e)
	}
	if v.QualifiedMembers != 3 || v.Level != 1 {
		t.Fatal(v)
	}
	next := at("2026-10-02T00:00:00Z")
	fs = append(fs, fact("bet-next", "B1", "BET_SETTLED", "100000", "100000", "SPORTS", "2026-10-02T12:00:00Z"))
	v, e = CalculateDay(c, "A", next, regs, fs)
	if e != nil || v.QualifiedMembers != 0 || v.Level != 0 {
		t.Fatal(v, e)
	}
	c.GradePeriod = "WEEK"
	v, e = CalculateDay(c, "A", next, regs, fs)
	if e != nil || v.QualifiedMembers != 3 || v.Level != 1 {
		t.Fatal(v, e)
	}
}
func TestAggregateRoundingAndDeterministicContributionConservation(t *testing.T) {
	c := config()
	c.Levels = []Level{{0, 0, "0", [3]int{500000, 0, 0}}}
	s := at("2026-09-01T00:00:00Z")
	regs := []Registration{{"a", "", s}, {"b", "a", s}, {"c", "a", s}}
	fs := []Fact{fact("b", "b", "BET_SETTLED", "1", "1", "SPORTS", "2026-10-01T01:00:00Z"), fact("c", "c", "BET_SETTLED", "1", "1", "SPORTS", "2026-10-01T01:00:00Z")}
	v, e := CalculateDay(c, "a", at("2026-10-01T00:00:00Z"), regs, fs)
	if e != nil || v.Amount != "1" || v.Contributions[0].UserID != "b" || v.Contributions[0].Amount != "1" {
		t.Fatal(v, e)
	}
	regs[1], regs[2] = regs[2], regs[1]
	fs[0], fs[1] = fs[1], fs[0]
	other, e := CalculateDay(c, "a", v.Day, regs, fs)
	a, _ := json.Marshal(v)
	b, _ := json.Marshal(other)
	if e != nil || string(a) != string(b) {
		t.Fatal("iteration order changes money")
	}
}
func TestRejectMalformedFactsAndRelationships(t *testing.T) {
	c := config()
	day := at("2026-10-01T00:00:00Z")
	regs, fs := fixture()
	tests := []struct {
		name string
		f    func([]Registration, []Fact) ([]Registration, []Fact)
	}{
		{"cycle", func(r []Registration, f []Fact) ([]Registration, []Fact) { r[0].ParentID = "E"; return r, f }},
		{"missing parent", func(r []Registration, f []Fact) ([]Registration, []Fact) { r[0].ParentID = "missing"; return r, f }},
		{"duplicate event", func(r []Registration, f []Fact) ([]Registration, []Fact) { return r, append(f, f[0]) }},
		{"valid greater than bet", func(r []Registration, f []Fact) ([]Registration, []Fact) { f[0].ValidBet = "100001"; return r, f }},
		{"wrong currency", func(r []Registration, f []Fact) ([]Registration, []Fact) { f[0].Currency = "USD"; return r, f }},
		{"before registration", func(r []Registration, f []Fact) ([]Registration, []Fact) {
			f[0].At = at("2026-08-01T00:00:00Z")
			return r, f
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, f := tc.f(append([]Registration(nil), regs...), append([]Fact(nil), fs...))
			if _, e := CalculateDay(c, "A", day, r, f); e == nil {
				t.Fatal("accepted invalid evidence")
			}
		})
	}
}

func TestAttributedDayKeepsBusinessTimeOwnershipAcrossApprovedMove(t *testing.T) {
	day := time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)
	c := Config{Enabled: true, Currency: "USD", CurrencyScale: 2, Timezone: "UTC", GradePeriod: "DAY", GradeCondition: Threshold{Deposit: "1", Turnover: "1"}, ReferralCondition: Threshold{Deposit: "1", Turnover: "1"}, InviteeCondition: Threshold{Deposit: "1", Turnover: "1"}, InviteeReward: "1", Steps: []Step{{Number: 1, Members: 1, Amount: "1"}}, VenueIDs: []string{"SPORTS"}, Levels: []Level{{Number: 0, Turnover: "0", Rates: [3]int{100000, 50000, 10000}}}}
	// 关账时会员已迁入新代理；迁移前订单仍属于旧代理，不能被最终等级快照改写归属。
	regs := []Registration{{UserID: "old", At: day}, {UserID: "new", At: day}, {UserID: "member", ParentID: "new", At: day}}
	facts := []Fact{{ID: "before-move", UserID: "member", Kind: "BET_SETTLED", Currency: "USD", Amount: "1000", ValidBet: "1000", VenueID: "SPORTS", At: day.Add(time.Hour)}, {ID: "after-move", UserID: "member", Kind: "BET_SETTLED", Currency: "USD", Amount: "2000", ValidBet: "2000", VenueID: "SPORTS", At: day.Add(15 * time.Hour)}}
	ownership := []Attribution{{FactID: "before-move", Ancestors: [3]string{"old"}}, {FactID: "after-move", Ancestors: [3]string{"new"}}}
	old, e := CalculateAttributedDay(c, "old", day, regs, facts, ownership)
	if e != nil || old.Amount != "100" || old.Turnover != "1000" {
		t.Fatal(old, e)
	}
	current, e := CalculateAttributedDay(c, "new", day, regs, facts, ownership)
	if e != nil || current.Amount != "200" || current.Turnover != "2000" {
		t.Fatal(current, e)
	}
	ownership[0].Ancestors = [3]string{"old", "", "new"}
	if _, e = CalculateAttributedDay(c, "new", day, regs, facts, ownership); e == nil {
		t.Fatal("压缩/断裂的物理祖先链通过")
	}
}
