package domain

import (
	"encoding/json"
	plan "github.com/ex-business/ExCommission/internal/plans/domain"
	"math/big"
	"reflect"
	"testing"
)

func TestThreeValuedConditions(t *testing.T) {
	input := sampleInput(t)
	yes := plan.Condition{Metric: "confirmed", Operator: "EQ", Value: json.RawMessage(`true`), Unit: "BOOLEAN"}
	no := yes
	no.Value = json.RawMessage(`false`)
	unknown := plan.Condition{Metric: "quantity", Operator: "GT", Value: json.RawMessage(`"0"`), Unit: "LOT"}
	for _, tc := range []struct {
		condition plan.Condition
		want      Truth
	}{{yes, True}, {no, False}, {unknown, Unknown}, {plan.Condition{Logic: "AND", Children: []plan.Condition{unknown, no}}, False}, {plan.Condition{Logic: "OR", Children: []plan.Condition{unknown, yes}}, True}, {plan.Condition{Logic: "NOT", Children: []plan.Condition{unknown}}, Unknown}} {
		truth, err := Evaluate(tc.condition, input.Facts)
		if err != nil || truth != tc.want {
			t.Fatalf("truth %d expected %d error %v", truth, tc.want, err)
		}
	}
	input.Plan.Rules[0].Condition = plan.Condition{Logic: "AND", Children: []plan.Condition{unknown, no}}
	result := Calculate(input)
	if result.Status != "NO_MATCH" || len(result.Items) != 0 {
		t.Fatalf("false dominates unknown %+v", result)
	}
	input.Facts.Quantity = "1"
	input.Facts.QuantityUnit = "COUNT"
	input.Plan.Rules[0].Condition = unknown
	result = Calculate(input)
	if result.Status != "SUSPENDED" || result.Reasons[0] != "UNIT_MISMATCH" {
		t.Fatalf("unit mismatch %+v", result)
	}
}
func TestWeightedOriginalCandidatesAndConservation(t *testing.T) {
	for pool := int64(0); pool < 100; pool++ {
		candidates := []WeightedCandidate{{ID: "B", Weight: "3", Eligible: true}, {ID: "A", Weight: "1", Eligible: false}, {ID: "C", Weight: "7", Eligible: true}}
		result, err := AllocateWeighted(big.NewInt(pool), candidates)
		if err != nil {
			t.Fatal(err)
		}
		sum := new(big.Int).Set(result.Platform)
		for _, amount := range result.Targets {
			sum.Add(sum, amount)
		}
		if sum.Cmp(big.NewInt(pool)) != 0 {
			t.Fatal("pool not conserved")
		}
		candidates[0], candidates[2] = candidates[2], candidates[0]
		again, err := AllocateWeighted(big.NewInt(pool), candidates)
		if err != nil || !reflect.DeepEqual(result, again) {
			t.Fatal("input iteration order changed allocation")
		}
	}
	input := sampleInput(t)
	rule := &input.Plan.Rules[0]
	rule.Algorithm = "WEIGHTED"
	rule.Recipient = "EXPLICIT"
	rule.Funding = nil
	rule.Parameters = plan.Parameters{PoolMinor: "100", RewardCurrency: "USD", Beneficiaries: []plan.Weight{{BeneficiaryID: "A", Weight: "1"}, {BeneficiaryID: "B", Weight: "1"}, {BeneficiaryID: "C", Weight: "1"}}}
	budget := "100"
	input.Plan.EventBudget = &budget
	yes, no := true, false
	input.Recipients = []Actor{{ID: "A", Eligible: &no, Status: "ACTIVE"}, {ID: "B", Eligible: &yes, Status: "ACTIVE"}, {ID: "C", Eligible: &yes, Status: "ACTIVE"}}
	result := Calculate(input)
	if result.Status != "SUCCEEDED" || len(result.Items) != 2 || result.Items[0].Minor != "33" || result.Items[1].Minor != "33" || result.Items[0].Depth != 0 {
		t.Fatalf("ineligible share was redistributed %+v", result)
	}
}
func TestFixedCrossCurrencyAndFrozenQualification(t *testing.T) {
	input := sampleInput(t)
	rule := &input.Plan.Rules[0]
	rule.Algorithm = "FIXED"
	rule.Funding = nil
	rule.Parameters = plan.Parameters{AmountMinor: "10", RewardCurrency: "USD"}
	budget := "10"
	input.Plan.EventBudget = &budget
	input.Facts.Currency = "BTC"
	result := Calculate(input)
	if result.Status != "SUCCEEDED" || result.Items[0].Minor != "10" {
		t.Fatalf("fixed reward cross-basis currency %+v", result)
	}
	input.Ancestors[0].Status = "FROZEN"
	result = Calculate(input)
	if result.Status != "SUCCEEDED" || len(result.Items) != 0 {
		t.Fatal("frozen earning default")
	}
	input.Plan.AgentPolicy.EarnFrozen = true
	result = Calculate(input)
	if result.Status != "SUCCEEDED" || len(result.Items) != 1 || len(result.Trace) == 0 || result.Trace[0].Reason != "FROZEN_PAYMENT_HOLD" {
		t.Fatalf("frozen payment trace missing %+v", result)
	}
	input.Facts.Basis = "0"
	result = Calculate(input)
	if len(result.Items) != 0 {
		t.Fatal("fixed reward on zero original basis")
	}
}
