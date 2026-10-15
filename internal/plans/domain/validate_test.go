package domain

import (
	"encoding/json"
	"testing"
)

func Sample() Config {
	limit := "1000000"
	return Config{PlanID: "P1", Name: "佣金计划", StandardVersion: "1", Route: Route{Product: "BTC-USDT", EventType: "FEE_CONFIRMED", Scene: "SPOT"}, Scope: Scope{Type: "ALL_ACTIVE_AGENTS", AgentIDs: []string{}}, Currency: "USD", CalendarVersion: "UTC-v1", AgentPolicy: Policy{MaxDepth: 3, GradeWindow: "PREVIOUS_CALENDAR_MONTH", GradeEvaluation: "DAILY", EligibilityRequired: true}, Grades: []Grade{{ID: "G0", Rank: 0, Conditions: []GradeCondition{}}}, Rules: []Rule{{ID: "R1", Algorithm: "PERCENT", Version: "1", Recipient: "DIRECT", Condition: Condition{Logic: "AND", Children: []Condition{}}, Parameters: Parameters{RatePPM: "100000"}, Funding: &limit, Frequency: "ONCE_PER_BUSINESS_RULE"}}, Settlement: SettlementPolicy{Period: "DAILY", Minimum: "0", LatePolicy: "NEXT_OPEN_PERIOD", FeeBearer: "PLATFORM"}}
}
func TestPublicationValidation(t *testing.T) {
	config := Sample()
	if err := Validate(config, 2); err != nil {
		t.Fatal(err)
	}
	for _, change := range []string{"depth", "rate", "budget", "missing-rate", "tier-gap", "tier-rate", "unsupported", "condition-depth", "unknown-fact"} {
		t.Run(change, func(t *testing.T) {
			config := Sample()
			rule := &config.Rules[0]
			switch change {
			case "depth":
				config.AgentPolicy.MaxDepth = 65
			case "rate":
				rule.Parameters.RatePPM = "1000001"
			case "budget":
				rule.Algorithm = "FIXED"
				rule.Parameters = Parameters{AmountMinor: "100", RewardCurrency: "USD"}
				rule.Funding = nil
			case "missing-rate":
				rule.Algorithm = "DIFFERENTIAL"
				rule.Parameters = Parameters{}
			case "tier-gap":
				upper := "100"
				rule.Algorithm = "MARGINAL"
				rule.Parameters = Parameters{Tiers: []Tier{{Lower: "0", Upper: &upper, PPM: "100000"}, {Lower: "101", PPM: "200000"}}}
			case "tier-rate":
				upper := "100"
				rule.Algorithm = "MARGINAL"
				rule.Parameters = Parameters{Tiers: []Tier{{Lower: "0", Upper: &upper, PPM: "200000"}, {Lower: "100", PPM: "100000"}}}
			case "unsupported":
				rule.Algorithm = "SCRIPT"
			case "condition-depth":
				condition := Condition{Metric: "confirmed", Operator: "EQ", Value: json.RawMessage(`true`), Unit: "BOOLEAN"}
				for range 9 {
					condition = Condition{Logic: "NOT", Children: []Condition{condition}}
				}
				rule.Condition = condition
			case "unknown-fact":
				rule.Condition = Condition{Metric: "arbitrary_db_field", Operator: "EQ", Value: json.RawMessage(`"1"`), Unit: "USD"}
			}
			if err := Validate(config, 2); err == nil {
				t.Fatal("invalid plan accepted")
			}
		})
	}
}
func TestHighestGradeFrozenMetrics(t *testing.T) {
	grades := []Grade{{ID: "G0", Rank: 0}, {ID: "G1", Rank: 1, Conditions: []GradeCondition{{Metric: "PERSONAL_NET_BASIS", Operator: "GTE", Value: "100", Unit: "USD"}}}, {ID: "G2", Rank: 2, Conditions: []GradeCondition{{Metric: "PERSONAL_NET_BASIS", Operator: "GTE", Value: "200", Unit: "USD"}, {Metric: "ACTIVE_DIRECT_COUNT", Operator: "GTE", Value: "2", Unit: "COUNT"}}}}
	id, err := SelectGrade(grades, map[string]string{"PERSONAL_NET_BASIS": "250", "ACTIVE_DIRECT_COUNT": "1"}, "USD")
	if err != nil || id != "G1" {
		t.Fatalf("grade %s %v", id, err)
	}
	if _, err = SelectGrade(grades, map[string]string{"PERSONAL_NET_BASIS": "250"}, "USD"); err == nil {
		t.Fatal("missing metrics accepted")
	}
}
