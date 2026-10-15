package domain

import (
	"encoding/json"
	plan "github.com/ex-business/ExCommission/internal/plans/domain"
	"os"
	"testing"
	"time"
)

func sampleInput(t *testing.T) Input {
	t.Helper()
	body, err := os.ReadFile("../../../conformance/v1/plan-percent.json")
	if err != nil {
		t.Fatal(err)
	}
	var config plan.Config
	if err = json.Unmarshal(body, &config); err != nil {
		t.Fatal(err)
	}
	yes := true
	return Input{TaskID: "T1", SnapshotID: "S1", Plan: config, Facts: Facts{Basis: "1000", Currency: "USD", Product: "BTC-USDT", Scene: "SPOT", Confirmed: &yes, BusinessTime: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}, Ancestors: []Actor{{ID: "A1", Depth: 1, Grade: "G0", Eligible: &yes, Status: "ACTIVE"}}, Contracts: map[string]ContractFact{}, Exponent: 2}
}
func TestPercentTrialExactnessAndTaskBudget(t *testing.T) {
	for _, tc := range []struct {
		basis, rate, want string
		exponent          int
	}{{"1000", "100000", "10000", 2}, {"0.01", "500000", "0", 2}, {"0.01", "500000", "500000", 8}, {"0", "1000000", "0", 2}, {"999999999999999999.99999999", "1000000", "99999999999999999999999999", 8}, {"10.99999999", "123457", "135", 2}} {
		input := sampleInput(t)
		input.Facts.Basis = tc.basis
		input.Plan.Rules[0].Parameters.RatePPM = tc.rate
		input.Exponent = tc.exponent
		result := Calculate(input)
		if result.Status != "SUCCEEDED" {
			t.Fatalf("%+v", result)
		}
		got := "0"
		if len(result.Items) > 0 {
			got = result.Items[0].Minor
		}
		if got != tc.want {
			t.Fatalf("basis=%s got=%s want=%s", tc.basis, got, tc.want)
		}
	}
	input := sampleInput(t)
	budget := "1"
	input.Plan.EventBudget = &budget
	result := Calculate(input)
	if result.Status != "SUSPENDED" || len(result.Items) != 0 || result.Reasons[0] != "BUDGET_EXCEEDED" {
		t.Fatalf("partial budget result %+v", result)
	}

	input = sampleInput(t)
	unknown := input.Plan.Rules[0]
	unknown.ID = "R2"
	unknown.Condition = plan.Condition{Metric: "quantity", Operator: "GTE", Value: json.RawMessage(`"1"`), Unit: "LOT"}
	input.Plan.Rules = append(input.Plan.Rules, unknown)
	result = Calculate(input)
	if result.Status != "SUSPENDED" || len(result.Items) != 0 || result.Reasons[0] != "UNKNOWN_FACT" {
		t.Fatalf("unknown fact must prevent partial earning %+v", result)
	}
}
