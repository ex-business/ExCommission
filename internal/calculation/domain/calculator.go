package domain

import (
	"encoding/json"
	"errors"
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
type QualificationEvidence struct {
	Joined      bool       `json:"joined"`
	GradeID     string     `json:"grade_id"`
	Eligibility string     `json:"eligibility"`
	Revision    uint64     `json:"revision"`
	From        time.Time  `json:"effective_from"`
	To          *time.Time `json:"effective_to"`
	Source      string     `json:"source_kind"`
	Evidence    string     `json:"evidence_ref"`
	StatusFrom  time.Time  `json:"status_effective_from"`
	StatusTo    *time.Time `json:"status_effective_to"`
}
type Actor struct {
	Qualification *QualificationEvidence `json:"-"`
	ID            string                 `json:"agent_id"`
	Depth         int                    `json:"depth"`
	Grade         string                 `json:"grade_id"`
	Eligible      *bool                  `json:"eligible"`
	Status        string                 `json:"status"`
}
type ContractFact struct {
	ID       string
	Evidence string
	PlanID   string
	From     time.Time
	To       *time.Time
}

// DayEndEvidence 单独保存计算等级来源，原资格证据仍保留审批等级，二者不能混淆。
type DayEndEvidence struct {
	BusinessDay   string            `json:"business_day"`
	PlanVersionID string            `json:"plan_version_id"`
	Cutoff        time.Time         `json:"cutoff"`
	Watermark     string            `json:"source_watermark"`
	Grades        map[string]string `json:"grades"`
}
type Input struct {
	DayEnd                                                                              *DayEndEvidence `json:",omitempty"`
	TriggerEvidence                                                                     json.RawMessage `json:"trigger_evidence,omitempty"`
	QualificationFacts                                                                  map[string]QualificationEvidence
	RelationshipFacts                                                                   []RelationshipEvidence
	StandardVersion, PlanVersionID, ConfigHash, CurrencyCatalogVersion, CalendarVersion string
	AlgorithmVersions                                                                   map[string]string
	DepthLimitReached                                                                   bool
	Recipients                                                                          []Actor

	TaskID     string
	SnapshotID string
	Plan       plan.Config
	Facts      Facts
	Ancestors  []Actor
	Contracts  map[string]ContractFact
	Exponent   int
}
type RelationshipEvidence struct {
	Kind     string
	Evidence string
	AgentID  string
	Depth    int
	From     time.Time
	To       *time.Time
}
type Item struct {
	OriginalMinor string `json:"-"`
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
	for _, rule := range input.Plan.Rules {
		if Capabilities()[rule.Algorithm] != rule.Version {
			return suspend("UNSUPPORTED_CAPABILITY")
		}
	}
	if err := plan.Validate(input.Plan, input.Exponent); err != nil {
		return suspend("VALIDATION_FAILED")
	}
	if input.Facts.Confirmed == nil || !*input.Facts.Confirmed || input.Facts.BusinessTime.IsZero() || input.Facts.BusinessTime.Nanosecond()%1000 != 0 || input.Facts.Currency == "" || input.Facts.Product == "" || input.Facts.Scene == "" {
		return suspend("UNKNOWN_FACT")
	}
	basis, err := ParseDecimal(input.Facts.Basis)
	if err != nil {
		return suspend("VALIDATION_FAILED")
	}
	if input.Facts.Product != input.Plan.Route.Product || input.Facts.Scene != input.Plan.Route.Scene {
		return suspend("VALIDATION_FAILED")
	}
	seen := map[string]bool{}
	last := 0
	actors := map[string]Actor{}
	for _, actor := range input.Ancestors {
		if actor.ID == "" || seen[actor.ID] || actor.Depth != last+1 || actor.Grade == "" || actor.Eligible == nil || !knownStatus(actor.Status) {
			return suspend("RELATION_HISTORY_MISSING")
		}
		seen[actor.ID] = true
		last = actor.Depth
		actors[actor.ID] = actor
	}
	for _, actor := range input.Recipients {
		if actor.ID == "" || actor.Eligible == nil || !knownStatus(actor.Status) {
			return suspend("UNKNOWN_FACT")
		}
		if previous, ok := actors[actor.ID]; ok {
			if previous.Status != actor.Status || *previous.Eligible != *actor.Eligible {
				return suspend("UNKNOWN_FACT")
			}
			continue
		}
		actors[actor.ID] = actor
	}
	total := new(big.Int)
	matched := false
	for _, rule := range input.Plan.Rules {
		truth, err := Evaluate(rule.Condition, input.Facts)
		if err != nil {
			return suspend(err.Error())
		}
		if truth == Unknown {
			return suspend("UNKNOWN_FACT")
		}
		if truth == False {
			result.Trace = append(result.Trace, Trace{RuleID: rule.ID, Reason: "NO_MATCH"})
			continue
		}
		matched = true
		if rule.Algorithm != "FIXED" && rule.Algorithm != "WEIGHTED" && input.Facts.Currency != input.Plan.Currency {
			return suspend("BASIS_CURRENCY_MISMATCH")
		}
		candidates := []Actor{}
		weighted := map[string]*big.Int{}
		if rule.Algorithm == "WEIGHTED" {
			weights := make([]WeightedCandidate, 0, len(rule.Parameters.Beneficiaries))
			for _, beneficiary := range rule.Parameters.Beneficiaries {
				actor, ok := actors[beneficiary.BeneficiaryID]
				if !ok {
					return suspend("UNKNOWN_FACT")
				}
				eligible, reason, err := earningEligibility(input, actor)
				if err != nil {
					return suspend(err.Error())
				}
				if !eligible {
					result.Trace = append(result.Trace, Trace{RuleID: rule.ID, BeneficiaryID: actor.ID, Depth: actor.Depth, Reason: reason})
				}
				weights = append(weights, WeightedCandidate{ID: actor.ID, Weight: beneficiary.Weight, Eligible: eligible})
				candidates = append(candidates, actor)
			}
			pool, _ := plan.Minor(rule.Parameters.PoolMinor)
			if basis.Atoms().Sign() == 0 {
				pool = new(big.Int)
			}
			allocated, err := AllocateWeighted(pool, weights)
			if err != nil {
				return suspend("VALIDATION_FAILED")
			}
			weighted = allocated.Targets
		} else {
			for _, actor := range input.Ancestors {
				if actor.Depth > input.Plan.AgentPolicy.MaxDepth {
					result.Trace = append(result.Trace, Trace{RuleID: rule.ID, Depth: actor.Depth, Reason: "DEPTH_LIMIT_REACHED"})
					break
				}
				if rule.Recipient == "DIRECT" && actor.Depth != 1 {
					continue
				}
				candidates = append(candidates, actor)
			}
		}
		if input.DepthLimitReached && rule.Recipient != "EXPLICIT" {
			result.Trace = append(result.Trace, Trace{RuleID: rule.ID, Depth: input.Plan.AgentPolicy.MaxDepth + 1, Reason: "DEPTH_LIMIT_REACHED"})
		}
		ruleTotal := new(big.Int)
		covered := int64(0)
		for _, actor := range candidates {
			eligible, reason, err := earningEligibility(input, actor)
			if err != nil {
				return suspend(err.Error())
			}
			if !eligible {
				if rule.Algorithm != "WEIGHTED" {
					result.Trace = append(result.Trace, Trace{RuleID: rule.ID, BeneficiaryID: actor.ID, Depth: actor.Depth, Reason: reason})
				}
				continue
			}
			target := new(big.Int)
			switch rule.Algorithm {
			case "PERCENT":
				rate, _ := plan.PPM(rule.Parameters.RatePPM)
				target = percent(basis.Atoms(), rate, input.Exponent)
			case "FIXED":
				if basis.Atoms().Sign() > 0 {
					target, _ = plan.Minor(rule.Parameters.AmountMinor)
				}
			case "UNIT_PRICE":
				if input.Facts.Quantity == "" || input.Facts.QuantityUnit == "" {
					return suspend("UNKNOWN_FACT")
				}
				if input.Facts.QuantityUnit != rule.Parameters.QuantityUnit {
					return suspend("UNIT_MISMATCH")
				}
				quantity, err := ParseDecimal(input.Facts.Quantity)
				if err != nil {
					return suspend("VALIDATION_FAILED")
				}
				price, err := ParseDecimal(rule.Parameters.UnitPrice)
				if err != nil {
					return suspend("VALIDATION_FAILED")
				}
				if basis.Atoms().Sign() > 0 {
					target = scaled(new(big.Int).Mul(quantity.Atoms(), price.Atoms()), big.NewInt(10000000000000000), input.Exponent)
				}
			case "INDEPENDENT", "DIFFERENTIAL":
				rate, found := int64(0), false
				for _, dimension := range rule.Parameters.Rates {
					if dimension.Product == input.Facts.Product && dimension.Grade == actor.Grade && (rule.Algorithm == "DIFFERENTIAL" || dimension.Depth != nil && *dimension.Depth == actor.Depth) {
						rate, _ = plan.PPM(dimension.PPM)
						found = true
						break
					}
				}
				if !found {
					return suspend("RATE_MISSING")
				}
				delta := rate
				if rule.Algorithm == "DIFFERENTIAL" {
					delta = rate - covered
					if delta <= 0 {
						delta = 0
						reason = "PEER_NO_DIFFERENTIAL"
						if rate < covered {
							reason = "INVERTED_RATE"
						}
					}
					if rate > covered {
						covered = rate
					}
				}
				target = percent(basis.Atoms(), delta, input.Exponent)
			case "MARGINAL", "HIGHEST_RATE":
				target, err = TierTarget(basis, rule.Parameters.Tiers, rule.Algorithm == "HIGHEST_RATE", input.Exponent)
				if err != nil {
					return suspend(err.Error())
				}
			case "WEIGHTED":
				target = new(big.Int).Set(weighted[actor.ID])
			default:
				return suspend("UNSUPPORTED_CAPABILITY")
			}
			if rule.BeneficiaryCap != nil {
				cap, _ := plan.Minor(*rule.BeneficiaryCap)
				if target.Cmp(cap) > 0 {
					target = cap
				}
			}
			if len(target.String()) > 38 {
				return suspend("AMOUNT_OUT_OF_RANGE")
			}
			if target.Sign() == 0 {
				if reason == "" {
					reason = "ZERO_RATE"
					if basis.Atoms().Sign() == 0 {
						reason = "ZERO_BASIS"
					}
				}
				result.Trace = append(result.Trace, Trace{RuleID: rule.ID, BeneficiaryID: actor.ID, Depth: actor.Depth, Reason: reason})
				continue
			}
			result.Items = append(result.Items, Item{RuleID: rule.ID, BeneficiaryID: actor.ID, Depth: actor.Depth, Currency: input.Plan.Currency, Minor: target.String(), Algorithm: rule.Algorithm, Version: rule.Version})
			ruleTotal.Add(ruleTotal, target)
			if actor.Status == "FROZEN" {
				result.Trace = append(result.Trace, Trace{RuleID: rule.ID, BeneficiaryID: actor.ID, Depth: actor.Depth, Reason: "FROZEN_PAYMENT_HOLD"})
			}
		}
		if rule.EventCap != nil {
			cap, _ := plan.Minor(*rule.EventCap)
			if ruleTotal.Cmp(cap) > 0 {
				return suspend("BUDGET_EXCEEDED")
			}
		}
		if rule.Funding != nil {
			funding, _ := plan.Unsigned(*rule.Funding)
			limit := scaled(new(big.Int).Mul(basis.Atoms(), funding), big.NewInt(100000000000000), input.Exponent)
			if ruleTotal.Cmp(limit) > 0 {
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
	if !matched {
		result.Status = "NO_MATCH"
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
	sort.Slice(result.Trace, func(i, j int) bool {
		a, b := result.Trace[i], result.Trace[j]
		if a.RuleID != b.RuleID {
			return a.RuleID < b.RuleID
		}
		if a.Depth != b.Depth {
			return a.Depth < b.Depth
		}
		if a.BeneficiaryID != b.BeneficiaryID {
			return a.BeneficiaryID < b.BeneficiaryID
		}
		return a.Reason < b.Reason
	})
	return result
}
func earningEligibility(input Input, actor Actor) (bool, string, error) {
	if actor.Eligible == nil {
		return false, "", errors.New("UNKNOWN_FACT")
	}
	if !*actor.Eligible {
		return false, "INELIGIBLE", nil
	}
	if input.Plan.Scope.Type == "EXPLICIT_AGENT_IDS" {
		found := false
		for _, id := range input.Plan.Scope.AgentIDs {
			if actor.ID == id {
				found = true
			}
		}
		if !found {
			return false, "INELIGIBLE", nil
		}
	}
	switch actor.Status {
	case "ACTIVE":
	case "FROZEN":
		if !input.Plan.AgentPolicy.EarnFrozen {
			return false, "FROZEN_NO_EARNING", nil
		}
	case "DISABLED", "CLOSED":
		return false, "INELIGIBLE", nil
	default:
		return false, "", errors.New("UNKNOWN_FACT")
	}
	if input.Plan.AgentPolicy.RequireContract {
		fact, ok := input.Contracts[actor.ID]
		if !ok {
			return false, "", errors.New("UNKNOWN_FACT")
		}
		if fact.PlanID != input.Plan.PlanID || input.Facts.BusinessTime.Before(fact.From) || (fact.To != nil && !input.Facts.BusinessTime.Before(*fact.To)) {
			return false, "INELIGIBLE", nil
		}
	}
	return true, "", nil
}
func percent(atoms *big.Int, rate int64, exponent int) *big.Int {
	numerator := new(big.Int).Mul(atoms, big.NewInt(rate))
	numerator.Mul(numerator, new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(exponent)), nil))
	return numerator.Quo(numerator, big.NewInt(100000000000000))
}
func Capabilities() map[string]string {
	return map[string]string{"PERCENT": "1", "FIXED": "1", "UNIT_PRICE": "1", "INDEPENDENT": "1", "DIFFERENTIAL": "1", "MARGINAL": "1", "HIGHEST_RATE": "1", "WEIGHTED": "1"}
}

func knownStatus(status string) bool {
	return status == "ACTIVE" || status == "FROZEN" || status == "DISABLED" || status == "CLOSED"
}
