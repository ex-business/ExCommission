package domain

import (
	"testing"
	"time"
)

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func TestStatisticsNaturalPeriods(t *testing.T) {
	tests := []struct{ name, kind, zone, asOf, start, end string }{
		{"日末归属于刚结束当天", NaturalDay, "Asia/Shanghai", "2026-10-16T16:00:00Z", "2026-10-15T16:00:00Z", "2026-10-16T16:00:00Z"},
		{"周一到周日跨年", NaturalWeek, "Asia/Shanghai", "2026-01-01T16:00:00Z", "2025-12-28T16:00:00Z", "2026-01-04T16:00:00Z"},
		{"周一零点仍是上周截止", NaturalWeek, "UTC", "2026-10-19T00:00:00Z", "2026-10-12T00:00:00Z", "2026-10-19T00:00:00Z"},
		{"周一中午新一周", NaturalWeek, "UTC", "2026-10-19T12:00:00Z", "2026-10-19T00:00:00Z", "2026-10-26T00:00:00Z"},
		{"闰年二月", NaturalMonth, "UTC", "2024-03-01T00:00:00Z", "2024-02-01T00:00:00Z", "2024-03-01T00:00:00Z"},
		{"一号中午新月", NaturalMonth, "UTC", "2026-11-01T12:00:00Z", "2026-11-01T00:00:00Z", "2026-12-01T00:00:00Z"},
		{"夏令时23小时日", NaturalDay, "America/New_York", "2026-03-09T04:00:00Z", "2026-03-08T05:00:00Z", "2026-03-09T04:00:00Z"},
		{"夏令时25小时日", NaturalDay, "America/New_York", "2026-11-02T05:00:00Z", "2026-11-01T04:00:00Z", "2026-11-02T05:00:00Z"},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			w, err := WindowAt(&StatisticsPolicy{Range: c.kind, Timezone: c.zone}, mustTime(t, c.asOf))
			if err != nil {
				t.Fatal(err)
			}
			if w.Start == nil || w.End == nil || !w.Start.Equal(mustTime(t, c.start)) || !w.End.Equal(mustTime(t, c.end)) {
				t.Fatal(w)
			}
			if !w.Contains(*w.Start) || w.Contains(w.Start.Add(-time.Microsecond)) || w.Contains(*w.End) || w.Contains(w.AsOf) {
				t.Fatal("边界应左闭右开", w)
			}
		})
	}
	for _, p := range []*StatisticsPolicy{nil, {Range: Forever, Timezone: "Asia/Shanghai"}} {
		w, err := WindowAt(p, mustTime(t, "2026-10-16T00:00:00Z"))
		if err != nil || w.Start != nil || w.End != nil || !w.Contains(mustTime(t, "2001-01-01T00:00:00Z")) {
			t.Fatal(w, err)
		}
	}
	for _, p := range []*StatisticsPolicy{{Range: "ROLLING_WEEK", Timezone: "UTC"}, {Range: NaturalDay, Timezone: "Local"}, {Range: NaturalDay, Timezone: "Mars/Test"}, {Range: NaturalMonth}} {
		if _, err := WindowAt(p, time.Now().Truncate(time.Microsecond)); err == nil {
			t.Fatal(p)
		}
	}
}
func TestStatisticsDoesNotResetQualificationAccumulation(t *testing.T) {
	parent := "parent"
	member := Member{UserID: "u", AgentID: "a", ParentID: &parent, RegisteredAt: mustTime(t, "2026-10-01T00:00:00Z")}
	venue := "SPORTS"
	for _, kind := range []string{NaturalDay, NaturalWeek, NaturalMonth, Forever} {
		p := Policy{ID: "p", Source: "sandbox", Currency: "USD", Deposit: "50", Bet: "100", Venues: []string{venue}, Statistics: &StatisticsPolicy{Range: kind, Timezone: "UTC"}}
		at := mustTime(t, "2026-10-16T08:00:00Z")
		facts := []Fact{{PolicyID: "p", Source: "sandbox", Currency: "USD", Type: "DEPOSIT_SUCCEEDED", Amount: "50", At: member.RegisteredAt}, {PolicyID: "p", Source: "sandbox", Currency: "USD", Type: "BET_SETTLED", Amount: "100", Venue: &venue, At: at}}
		state, err := Evaluate(p, member, facts)
		if err != nil || !state.Effective || !state.QualifiedAt.Equal(at) {
			t.Fatal(kind, state, err)
		}
	}
}
