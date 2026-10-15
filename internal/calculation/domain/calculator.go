package domain

import (
	plan "github.com/ex-business/ExCommission/internal/plans/domain"
	"math/big"
	"sort"
	"time"
)

type Facts struct {
	Basis        string    `json:"basis_amount"`
	Currency     string    `json:"basis_currency"`
	Quantity     string    `json:"quantity,omitempty"`
	QuantityUnit string    `json:"quantity_unit,omitempty"`
	Product      string    `json:"product_code"`
	Scene        string    `json:"scene"`
	Confirmed    *bool     `json:"confirmed"`
	BusinessTime time.Time `json:"business_time"`
}
type Actor struct {
	ID       string `json:"agent_id"`
	Depth    int    `json:"depth"`
	Grade    string `json:"grade_id"`
	Eligible *bool  `json:"eligible"`
	Status   string `json:"status"`
}
type ContractFact struct {
	PlanID string
	From   time.Time
	To     *time.Time
}
type Input struct {
	TaskID     string
	SnapshotID string
	Plan       plan.Config
	Facts      Facts
	Ancestors  []Actor
	Contracts  map[string]ContractFact
	Exponent   int
}
type Item struct {
	RuleID        string `json:"rule_id"`
	BeneficiaryID string `json:"beneficiary_id"`
	Depth         int    `json:"depth"`
	Currency      string `json:"currency"`
	Minor         string `json:"minor_units"`
	Algorithm     string `json:"algorithm_id"`
	Version       string `json:"algorithm_version"`
}
type Trace struct {
	RuleID        string `json:"rule_id"`
	BeneficiaryID string `json:"beneficiary_id,omitempty"`
	Depth         int    `json:"depth,omitempty"`
	Reason        string `json:"reason_code"`
}
type Result struct {
	TaskID     string   `json:"task_id"`
	SnapshotID string   `json:"snapshot_id"`
	Status     string   `json:"status"`
	Items      []Item   `json:"items"`
	Reasons    []string `json:"reason_codes"`
	Trace      []Trace  `json:"trace"`
}

func Calculate(input Input) Result {
	result := Result{TaskID: input.TaskID, SnapshotID: input.SnapshotID, Status: "SUCCEEDED", Items: []Item{}, Reasons: []string{}, Trace: []Trace{}}
	suspend := func(code string) Result {
		result.Status = "SUSPENDED"
		result.Items = []Item{}
		result.Reasons = []string{code}
		return result
	}
	if err := plan.Validate(input.Plan, input.Exponent); err != nil {
		return suspend("VALIDATION_FAILED")
	}
	if input.Facts.Confirmed == nil || !*input.Facts.Confirmed || input.Facts.BusinessTime.IsZero() {
		return suspend("UNKNOWN_FACT")
	}
	basis, err := ParseDecimal(input.Facts.Basis)
	if err != nil {
		return suspend("VALIDATION_FAILED")
	}
	if input.Facts.Currency != input.Plan.Currency {
		return suspend("BASIS_CURRENCY_MISMATCH")
	}
	if input.Facts.Product != input.Plan.Route.Product || input.Facts.Scene != input.Plan.Route.Scene {
		return suspend("VALIDATION_FAILED")
	}
	seen := map[string]bool{}
	last := 0
	for _, actor := range input.Ancestors {
		if actor.ID == "" || seen[actor.ID] || actor.Depth != last+1 || actor.Grade == "" || actor.Eligible == nil {
			return suspend("RELATION_HISTORY_MISSING")
		}
		seen[actor.ID] = true
		last = actor.Depth
	}
	total := new(big.Int)
	for _, rule := range input.Plan.Rules {
		// 完整算法注册及条件三值求值由任务06补齐；未实现能力明确拒绝试算通过。
		if rule.Algorithm != "PERCENT" || rule.Condition.Logic != "AND" || len(rule.Condition.Children) != 0 {
			return suspend("UNSUPPORTED_CAPABILITY")
		}
		rate, err := plan.PPM(rule.Parameters.RatePPM)
		if err != nil {
			return suspend("RATE_MISSING")
		}
		ruleTotal := new(big.Int)
		for _, actor := range input.Ancestors {
			if actor.Depth > input.Plan.AgentPolicy.MaxDepth {
				result.Trace = append(result.Trace, Trace{RuleID: rule.ID, Depth: actor.Depth, Reason: "DEPTH_LIMIT_REACHED"})
				break
			}
			if rule.Recipient == "DIRECT" && actor.Depth != 1 {
				continue
			}
			reason := ""
			if !*actor.Eligible {
				reason = "INELIGIBLE"
			}
			if input.Plan.Scope.Type == "EXPLICIT_AGENT_IDS" {
				included := false
				for _, id := range input.Plan.Scope.AgentIDs {
					if id == actor.ID {
						included = true
					}
				}
				if !included {
					reason = "INELIGIBLE"
				}
			}
			switch actor.Status {
			case "ACTIVE":
			case "FROZEN":
				if !input.Plan.AgentPolicy.EarnFrozen {
					reason = "FROZEN_NO_EARNING"
				}
			case "DISABLED", "CLOSED":
				reason = "INELIGIBLE"
			default:
				return suspend("UNKNOWN_FACT")
			}
			if input.Plan.AgentPolicy.RequireContract {
				fact, ok := input.Contracts[actor.ID]
				if !ok {
					return suspend("UNKNOWN_FACT")
				}
				if fact.PlanID != input.Plan.PlanID || input.Facts.BusinessTime.Before(fact.From) || (fact.To != nil && !input.Facts.BusinessTime.Before(*fact.To)) {
					reason = "INELIGIBLE"
				}
			}
			if reason != "" {
				result.Trace = append(result.Trace, Trace{RuleID: rule.ID, BeneficiaryID: actor.ID, Depth: actor.Depth, Reason: reason})
				continue
			}
			target := percent(basis.Atoms(), rate, input.Exponent)
			if rule.BeneficiaryCap != nil {
				cap, _ := plan.Minor(*rule.BeneficiaryCap)
				if target.Cmp(cap) > 0 {
					target = cap
				}
			}
			if target.Sign() == 0 {
				reason = "ZERO_RATE"
				result.Trace = append(result.Trace, Trace{RuleID: rule.ID, BeneficiaryID: actor.ID, Depth: actor.Depth, Reason: reason})
				continue
			}
			result.Items = append(result.Items, Item{RuleID: rule.ID, BeneficiaryID: actor.ID, Depth: actor.Depth, Currency: input.Plan.Currency, Minor: target.String(), Algorithm: rule.Algorithm, Version: rule.Version})
			ruleTotal.Add(ruleTotal, target)
		}
		if rule.EventCap != nil {
			cap, _ := plan.Minor(*rule.EventCap)
			if ruleTotal.Cmp(cap) > 0 {
				return suspend("BUDGET_EXCEEDED")
			}
		}
		if rule.Funding != nil {
			funding, _ := plan.PPM(*rule.Funding)
			if ruleTotal.Cmp(percent(basis.Atoms(), funding, input.Exponent)) > 0 {
				return suspend("BUDGET_EXCEEDED")
			}
		}
		total.Add(total, ruleTotal)
	}
	budget, _ := basis.Minor(input.Exponent)
	if input.Plan.EventBudget != nil {
		budget, _ = plan.Minor(*input.Plan.EventBudget)
	}
	if total.Cmp(budget) > 0 {
		return suspend("BUDGET_EXCEEDED")
	}
	sort.Slice(result.Items, func(i, j int) bool {
		a, b := result.Items[i], result.Items[j]
		if a.RuleID != b.RuleID {
			return a.RuleID < b.RuleID
		}
		if a.Depth != b.Depth {
			return a.Depth < b.Depth
		}
		return a.BeneficiaryID < b.BeneficiaryID
	})
	return result
}
func percent(atoms *big.Int, rate int64, exponent int) *big.Int {
	numerator := new(big.Int).Mul(atoms, big.NewInt(rate))
	numerator.Mul(numerator, new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(exponent)), nil))
	return numerator.Quo(numerator, big.NewInt(100000000000000))
}
func Capabilities() map[string]string { return map[string]string{"PERCENT": "1"} }
