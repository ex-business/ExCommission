package application

import (
	plan "github.com/ex-business/ExCommission/internal/plans/domain"
	"testing"
)

func TestOneTimeRewardRequiresUnconditionalDirectFixedAndSufficientBudget(t *testing.T) {
	amount := "2500"
	budget := "2500"
	v := plan.Version{Config: plan.Config{Route: plan.Route{EventType: "MILESTONE_REACHED"}, EventBudget: &budget, Rules: []plan.Rule{{Algorithm: "FIXED", Recipient: "DIRECT", Condition: plan.Condition{Logic: "AND"}, Parameters: plan.Parameters{AmountMinor: amount}}}}}
	if e := ValidateReward(v); e != nil {
		t.Fatal(e)
	}
	v.Config.Rules[0].Recipient = "ANCESTORS"
	if ValidateReward(v) == nil {
		t.Fatal("multiple recipients accepted")
	}
	v.Config.Rules[0].Recipient = "DIRECT"
	cap := "2499"
	v.Config.Rules[0].BeneficiaryCap = &cap
	if e := ValidateReward(v); e == nil || e.Error() != "BUDGET_EXCEEDED" {
		t.Fatal(e)
	}
	v.Config.Rules[0].BeneficiaryCap = nil
	budget = "2499"
	if ValidateReward(v) == nil {
		t.Fatal("insufficient budget accepted")
	}
	budget = "2500"
	v.Config.Rules[0].Condition.Children = []plan.Condition{{Metric: "confirmed"}}
	if ValidateReward(v) == nil {
		t.Fatal("second condition gate accepted")
	}
}
