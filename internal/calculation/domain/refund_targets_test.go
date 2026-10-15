package domain

import (
	plan "github.com/ex-business/ExCommission/internal/plans/domain"
	"testing"
)

func TestRefundFrozenTargetsAndCumulativeRounding(t *testing.T) {
	input := sampleInput(t)
	input.Facts.Basis = "0.03"
	input.Plan.Rules[0].Parameters.RatePPM = "500000"
	funding := "500000"
	input.Plan.Rules[0].Funding = &funding
	for _, v := range []struct{ total, want string }{{"0", "1"}, {"0.01", "1"}, {"0.02", "0"}, {"0.03", "0"}} {
		targets, err := RefundTargets(input, v.total, "", "")
		if err != nil || targets[input.Plan.Rules[0].ID+"\x00A1"] != v.want {
			t.Fatalf("%s: %v %v", v.total, targets, err)
		}
	}
	if _, err := RefundTargets(input, "0.04", "", ""); err == nil {
		t.Fatal("excess refund accepted")
	}
	input = sampleInput(t)
	budget := "10000"
	input.Plan.EventBudget = &budget
	input.Plan.Rules[0].Algorithm = "FIXED"
	input.Plan.Rules[0].Funding = nil
	input.Plan.Rules[0].Parameters = plan.Parameters{AmountMinor: "100", RewardCurrency: "USD"}
	for _, v := range []struct{ total, want string }{{"1", "100"}, {"999.99", "100"}, {"1000", "0"}} {
		targets, err := RefundTargets(input, v.total, "", "")
		if err != nil || targets[input.Plan.Rules[0].ID+"\x00A1"] != v.want {
			t.Fatalf("fixed %v %v", targets, err)
		}
	}
	input.Plan.Rules[0].Algorithm = "UNIT_PRICE"
	input.Facts.Quantity = "10"
	input.Facts.QuantityUnit = "LOT"
	input.Plan.Rules[0].Parameters = plan.Parameters{UnitPrice: "1.25", QuantityUnit: "LOT", RewardCurrency: "USD"}
	if _, err := RefundTargets(input, "1", "", ""); err == nil || err.Error() != "REFUND_QUANTITY_MISSING" {
		t.Fatal("quantity inferred", err)
	}
	targets, err := RefundTargets(input, "1", "2", "LOT")
	if err != nil || targets[input.Plan.Rules[0].ID+"\x00A1"] != "1000" {
		t.Fatal("unit refund", targets, err)
	}
	targets, err = RefundTargets(input, "0", "", "")
	if err != nil || targets[input.Plan.Rules[0].ID+"\x00A1"] != "1250" {
		t.Fatal("unrefunded unit reward blocked", targets, err)
	}
}

func TestRefundTargetsEveryRateAlgorithmAndWeightedPool(t *testing.T) {
	for _, algorithm := range []string{"PERCENT", "INDEPENDENT", "DIFFERENTIAL", "MARGINAL", "HIGHEST_RATE", "WEIGHTED"} {
		t.Run(algorithm, func(t *testing.T) {
			input := sampleInput(t)
			rule := &input.Plan.Rules[0]
			rule.Algorithm = algorithm
			one := 1
			switch algorithm {
			case "INDEPENDENT":
				rule.Parameters = plan.Parameters{Rates: []plan.Rate{{Product: "BTC-USDT", Grade: "G0", Depth: &one, PPM: "100000"}}}
			case "DIFFERENTIAL":
				rule.Parameters = plan.Parameters{Rates: []plan.Rate{{Product: "BTC-USDT", Grade: "G0", PPM: "100000"}}}
			case "MARGINAL", "HIGHEST_RATE":
				rule.Parameters = plan.Parameters{Tiers: []plan.Tier{{Lower: "0", Upper: nil, PPM: "100000"}}}
			case "WEIGHTED":
				budget := "10000"
				input.Plan.EventBudget = &budget
				rule.Funding = nil
				rule.Recipient = "EXPLICIT"
				rule.Parameters = plan.Parameters{PoolMinor: "1000", RewardCurrency: "USD", Beneficiaries: []plan.Weight{{BeneficiaryID: "A1", Weight: "1"}}}
			}
			targets, err := RefundTargets(input, "500", "", "")
			want := "5000"
			if algorithm == "WEIGHTED" {
				want = "1000"
			}
			if err != nil || targets[rule.ID+"\x00A1"] != want {
				t.Fatal(algorithm, targets, err)
			}
			targets, err = RefundTargets(input, "1000", "", "")
			if err != nil || targets[rule.ID+"\x00A1"] != "0" {
				t.Fatal("full refund", targets, err)
			}
		})
	}
}
