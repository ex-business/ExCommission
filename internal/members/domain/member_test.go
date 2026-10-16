package domain

import (
	plan "github.com/ex-business/ExCommission/internal/plans/domain"
	"strings"
	"testing"
)

func TestSharingDestinationAndExactPublicRules(t *testing.T) {
	for _, base := range []string{"javascript:alert(1)", "http://external.example/register", "https://user:password@example.com", "https://example.com/#fragment"} {
		if _, e := ShareURL(base, "code"); e == nil {
			t.Fatal(base)
		}
	}
	u, e := ShareURL("https://member.example/register?campaign=one", "opaque")
	if e != nil || !strings.Contains(u, "invite=opaque") || !strings.Contains(u, "campaign=one") {
		t.Fatal(u, e)
	}
	for value, want := range map[string]string{"100000": "10%", "1": "0.0001%", "0": "0%", "12345": "1.2345%"} {
		if got := Percent(value); got != want {
			t.Fatal(value, got)
		}
	}
	c := plan.Config{Name: "佣金方案", Currency: "USD", Rules: []plan.Rule{{Algorithm: "WEIGHTED", Parameters: plan.Parameters{PoolMinor: "5000", Beneficiaries: []plan.Weight{{BeneficiaryID: "private-agent", Weight: "1"}}}, Condition: plan.Condition{Logic: "AND"}}}}
	view := PublicRules(c)
	if strings.Contains(view.Rules[0].Description, "private-agent") || !strings.Contains(view.Rules[0].Description, "50.00 USD") {
		t.Fatal(view)
	}
}
