package domain

import (
	"errors"
	plan "github.com/ex-business/ExCommission/internal/plans/domain"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	"math/big"
)

// RefundTargets derives monotonically decreasing targets from the original frozen facts.
// Original eligibility and matching are retained; refunding cannot create new recipients.
func RefundTargets(input Input, total, quantity, unit string) (map[string]string, error) {
	original := Calculate(input)
	if original.Status == "SUSPENDED" {
		return nil, errors.New("UNKNOWN_FACT")
	}
	basis, err := foundation.ParseDecimal(input.Facts.Basis)
	if err != nil {
		return nil, err
	}
	refund, err := foundation.ParseDecimal(total)
	if err != nil {
		return nil, err
	}
	remaining, err := RemainingBasis(basis, refund)
	if err != nil {
		return nil, err
	}
	targets := map[string]string{}
	byRule := map[string][]Item{}
	for _, item := range original.Items {
		byRule[item.RuleID] = append(byRule[item.RuleID], item)
		targets[item.RuleID+"\x00"+item.BeneficiaryID] = "0"
	}
	if refund.Atoms().Sign() == 0 && quantity == "" {
		for _, item := range original.Items {
			targets[item.RuleID+"\x00"+item.BeneficiaryID] = item.Minor
		}
		return targets, nil
	}
	for _, rule := range input.Plan.Rules {
		items := byRule[rule.ID]
		if len(items) == 0 {
			continue
		}
		if rule.Algorithm == "FIXED" || rule.Algorithm == "WEIGHTED" {
			if remaining.Atoms().Sign() > 0 {
				for _, item := range items {
					targets[item.RuleID+"\x00"+item.BeneficiaryID] = item.Minor
				}
			}
			continue
		}
		derived := input
		derived.Facts.Basis = remaining.String()
		if rule.Algorithm == "UNIT_PRICE" {
			if quantity == "" {
				return nil, errors.New("REFUND_QUANTITY_MISSING")
			}
			if unit != input.Facts.QuantityUnit {
				return nil, errors.New("UNIT_MISMATCH")
			}
			old, err := foundation.ParseDecimal(input.Facts.Quantity)
			if err != nil {
				return nil, err
			}
			cancelled, err := foundation.ParseDecimal(quantity)
			if err != nil {
				return nil, err
			}
			left, err := RemainingBasis(old, cancelled)
			if err != nil {
				return nil, err
			}
			derived.Facts.Quantity = left.String()
		}
		// Re-evaluate amounts only. The original match, qualification and ceilings are frozen.
		sum := new(big.Int)
		for _, item := range items {
			n, _ := new(big.Int).SetString(item.Minor, 10)
			sum.Add(sum, n)
		}
		ceiling := sum.String()
		rule.Condition = plan.Condition{Logic: "AND", Children: []plan.Condition{}}
		rule.EventCap = &ceiling
		derived.Plan.Rules = []plan.Rule{rule}
		derived.Plan.EventBudget = &ceiling
		result := Calculate(derived)
		if result.Status == "SUSPENDED" {
			return nil, errors.New("UNKNOWN_FACT")
		}
		values := map[string]string{}
		for _, item := range result.Items {
			values[item.BeneficiaryID] = item.Minor
		}
		for _, item := range items {
			value := values[item.BeneficiaryID]
			if value == "" {
				value = "0"
			}
			n, _ := new(big.Int).SetString(value, 10)
			old, _ := new(big.Int).SetString(item.Minor, 10)
			if n.Cmp(old) > 0 {
				return nil, errors.New("REFUND_TARGET_INCREASE")
			}
			targets[item.RuleID+"\x00"+item.BeneficiaryID] = value
		}
	}
	return targets, nil
}
