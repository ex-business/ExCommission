package domain

import (
	"testing"
	"time"
)

func TestAllCumulativeConditionsExactAndMissing(t *testing.T) {
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	p := Program{ID: "invite", Name: "邀请奖励", Source: "sandbox", Starts: at, Ends: at.AddDate(0, 1, 0), Logic: "ALL", Frequency: "ONCE_PER_SUBJECT", PlanID: "reward", Conditions: []Condition{{"deposit", "USD", "100"}, {"turnover", "USD", "500"}}}
	if err := p.Normalize(); err != nil {
		t.Fatal(err)
	}
	facts := map[string]Fact{}
	check := func(want bool) {
		t.Helper()
		got, _ := p.Reached(facts)
		if got != want {
			t.Fatal(got, facts)
		}
	}
	check(false)
	facts["deposit"] = Fact{Value: "100", At: at.Add(time.Hour)}
	check(false)
	facts["turnover"] = Fact{Value: "499.99999999", At: at.Add(2 * time.Hour)}
	check(false)
	facts["turnover"] = Fact{Value: "500", At: at.Add(3 * time.Hour)}
	check(true)
	if _, when := p.Reached(facts); !when.Equal(at.Add(3 * time.Hour)) {
		t.Fatal(when)
	}
	p.Conditions = append(p.Conditions, p.Conditions[0])
	if p.Normalize() == nil {
		t.Fatal("duplicate metric accepted")
	}
	p.Conditions = []Condition{{"any", "COUNT", "0"}}
	if p.Normalize() == nil {
		t.Fatal("zero threshold accepted")
	}
}
