package domain

import (
	"encoding/json"
	"fmt"
	calculation "github.com/ex-business/ExCommission/internal/platform/domain"
	"math/big"
	"regexp"
	"sort"
	"strconv"
	"time"
)

var integer = regexp.MustCompile(`^[0-9]+$`)

func invalid(message string) error { return fmt.Errorf("VALIDATION_FAILED: %s", message) }
func PPM(value string) (int64, error) {
	if !integer.MatchString(value) {
		return 0, invalid("rate")
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n > 1000000 {
		return 0, invalid("rate range")
	}
	return n, nil
}
func Unsigned(value string) (*big.Int, error) {
	if !integer.MatchString(value) {
		return nil, invalid("unsigned integer")
	}
	n, _ := new(big.Int).SetString(value, 10)
	return n, nil
}
func Minor(value string) (*big.Int, error) {
	if !integer.MatchString(value) {
		return nil, invalid("minor units")
	}
	n, _ := new(big.Int).SetString(value, 10)
	if len(n.String()) > 38 {
		return nil, invalid("persistence range")
	}
	return n, nil
}
func Validate(config Config, exponent int) error {
	if config.StandardVersion != "1" || config.PlanID == "" || config.Name == "" || exponent < 0 || exponent > 8 {
		return invalid("version or currency")
	}
	if config.Route.Product == "" || config.Route.Scene == "" || (config.Route.EventType != "FEE_CONFIRMED" && config.Route.EventType != "TRADE_CLOSED" && config.Route.EventType != "ORDER_CONFIRMED") {
		return invalid("route")
	}
	policy := config.AgentPolicy
	if policy.MaxDepth < 1 || policy.MaxDepth > 64 || policy.SelfReward || !policy.EligibilityRequired || (policy.GradeWindow != "PREVIOUS_CALENDAR_MONTH" && policy.GradeWindow != "BUSINESS_DAY") || policy.GradeEvaluation != "DAILY" {
		return invalid("agent policy")
	}
	// 日终口径必须随方案审批发布；旧月度策略拒绝附带新配置，防止静默改变行为。
	if policy.GradeWindow == "BUSINESS_DAY" {
		d := policy.DayEnd
		if d == nil || d.SourceID == "" || d.QualificationPolicyID == "" || policy.MaxDepth != 3 {
			return invalid("day end policy")
		}
		if _, err := time.LoadLocation(d.Timezone); err != nil || d.Timezone == "" || d.Timezone == "Local" {
			return invalid("day end timezone")
		}
		for _, depths := range [][]int{d.MemberDepths, d.VolumeDepths} {
			seen := map[int]bool{}
			if len(depths) == 0 {
				return invalid("day end depths")
			}
			for _, depth := range depths {
				if depth < 1 || depth > 3 || seen[depth] {
					return invalid("day end depths")
				}
				seen[depth] = true
			}
		}
		for _, g := range config.Grades {
			for _, c := range g.Conditions {
				if c.Metric != "EFFECTIVE_MEMBER_COUNT" && c.Metric != "TEAM_VALID_VOLUME" {
					return invalid("day end grade metric")
				}
			}
		}
		for _, rule := range config.Rules {
			if rule.Algorithm != "INDEPENDENT" || rule.Recipient != "ANCESTORS" {
				return invalid("day end algorithm")
			}
		}
	} else if policy.DayEnd != nil {
		return invalid("unexpected day end policy")
	}
	if config.Scope.Type != "ALL_ACTIVE_AGENTS" && config.Scope.Type != "EXPLICIT_AGENT_IDS" {
		return invalid("scope")
	}
	if config.Scope.Type == "ALL_ACTIVE_AGENTS" && len(config.Scope.AgentIDs) != 0 || config.Scope.Type == "EXPLICIT_AGENT_IDS" && len(config.Scope.AgentIDs) == 0 {
		return invalid("scope members")
	}
	members := map[string]bool{}
	for _, id := range config.Scope.AgentIDs {
		if id == "" || members[id] {
			return invalid("duplicate scope")
		}
		members[id] = true
	}
	grades := map[string]bool{}
	ranks := map[int]bool{}
	base := false
	for _, grade := range config.Grades {
		if grade.ID == "" || grades[grade.ID] || grade.Rank < 0 || ranks[grade.Rank] {
			return invalid("grade dimensions")
		}
		grades[grade.ID] = true
		ranks[grade.Rank] = true
		if grade.Rank == 0 {
			if len(grade.Conditions) != 0 {
				return invalid("base grade")
			}
			base = true
		}
		for _, condition := range grade.Conditions {
			if condition.Metric != "PERSONAL_NET_BASIS" && condition.Metric != "TEAM_NET_BASIS" && condition.Metric != "ACTIVE_DIRECT_COUNT" && !(policy.GradeWindow == "BUSINESS_DAY" && (condition.Metric == "EFFECTIVE_MEMBER_COUNT" || condition.Metric == "TEAM_VALID_VOLUME")) {
				return invalid("grade metric")
			}
			if condition.Metric == "ACTIVE_DIRECT_COUNT" || condition.Metric == "EFFECTIVE_MEMBER_COUNT" {
				if condition.Unit != "COUNT" || !integer.MatchString(condition.Value) {
					return invalid("count units")
				}
			} else if condition.Unit != config.Currency {
				return invalid("grade currency")
			}
			if _, err := calculation.ParseDecimal(condition.Value); err != nil {
				return err
			}
			if !comparison(condition.Operator) {
				return invalid("grade operator")
			}
		}
	}
	if !base {
		return invalid("base grade missing")
	}
	if config.Settlement.Period != "DAILY" || config.Settlement.LatePolicy != "NEXT_OPEN_PERIOD" || config.Settlement.FeeBearer != "PLATFORM" {
		return invalid("settlement policy")
	}
	if _, err := Minor(config.Settlement.Minimum); err != nil {
		return err
	}
	if config.Settlement.Maximum != nil {
		maximum, err := Minor(*config.Settlement.Maximum)
		if err != nil {
			return err
		}
		minimum, _ := Minor(config.Settlement.Minimum)
		if maximum.Cmp(minimum) < 0 {
			return invalid("payout limits")
		}
	}
	if config.EventBudget != nil {
		if _, err := Minor(*config.EventBudget); err != nil {
			return err
		}
	}
	seen := map[string]bool{}
	if len(config.Rules) == 0 {
		return invalid("empty rules")
	}
	for _, rule := range config.Rules {
		if rule.ID == "" || seen[rule.ID] || rule.Version != "1" || rule.Frequency != "ONCE_PER_BUSINESS_RULE" {
			return invalid("rule identity")
		}
		seen[rule.ID] = true
		leaves := 0
		conditionCurrency := config.Currency
		if rule.Algorithm == "FIXED" || rule.Algorithm == "WEIGHTED" {
			conditionCurrency = ""
		}
		if err := validateCondition(rule.Condition, conditionCurrency, 1, &leaves, true); err != nil {
			return err
		}
		for _, cap := range []*string{rule.BeneficiaryCap, rule.EventCap} {
			if cap != nil {
				if _, err := Minor(*cap); err != nil {
					return err
				}
			}
		}
		count := int64(policy.MaxDepth)
		if rule.Recipient == "DIRECT" {
			count = 1
		} else if rule.Recipient != "ANCESTORS" && rule.Algorithm != "WEIGHTED" {
			return invalid("recipient")
		}
		p := rule.Parameters
		maxSpend := int64(0)
		switch rule.Algorithm {
		case "PERCENT":
			rate, err := PPM(p.RatePPM)
			if err != nil {
				return err
			}
			maxSpend = rate * count
		case "FIXED", "UNIT_PRICE", "WEIGHTED":
			if config.EventBudget == nil || rule.Funding != nil || p.RewardCurrency != config.Currency {
				return invalid("explicit reward budget")
			}
			if rule.Algorithm == "FIXED" {
				if _, err := Minor(p.AmountMinor); err != nil {
					return err
				}
			}
			if rule.Algorithm == "UNIT_PRICE" {
				if p.RewardCurrency != config.Currency || p.QuantityUnit == "" {
					return invalid("unit price currency or unit")
				}
				d, err := calculation.ParseDecimal(p.UnitPrice)
				if err != nil {
					return err
				}
				factor := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(8-exponent)), nil)
				if new(big.Int).Mod(d.Atoms(), factor).Sign() != 0 {
					return invalid("unit price precision")
				}
			}
			if rule.Algorithm == "WEIGHTED" {
				if rule.Recipient != "EXPLICIT" || len(p.Beneficiaries) == 0 {
					return invalid("weighted recipients")
				}
				if _, err := Minor(p.PoolMinor); err != nil {
					return err
				}
				ids := map[string]bool{}
				for _, beneficiary := range p.Beneficiaries {
					weight, err := Unsigned(beneficiary.Weight)
					if err != nil || weight.Sign() <= 0 || beneficiary.BeneficiaryID == "" || ids[beneficiary.BeneficiaryID] {
						return invalid("weights")
					}
					ids[beneficiary.BeneficiaryID] = true
				}
			}
		case "INDEPENDENT", "DIFFERENTIAL":
			dimensions := map[string]bool{}
			depthMax := map[int]int64{}
			for _, rate := range p.Rates {
				if rate.Product != config.Route.Product || !grades[rate.Grade] {
					return invalid("rate dimension")
				}
				ppm, err := PPM(rate.PPM)
				if err != nil {
					return err
				}
				depth := 0
				if rule.Algorithm == "INDEPENDENT" {
					if rate.Depth == nil || *rate.Depth < 1 || *rate.Depth > int(count) {
						return invalid("rate depth")
					}
					depth = *rate.Depth
				} else if rate.Depth != nil {
					return invalid("differential depth")
				}
				key := rate.Grade + ":" + strconv.Itoa(depth)
				if dimensions[key] {
					return invalid("duplicate rate")
				}
				dimensions[key] = true
				if ppm > depthMax[depth] {
					depthMax[depth] = ppm
				}
			}
			for grade := range grades {
				if rule.Algorithm == "DIFFERENTIAL" {
					if !dimensions[grade+":0"] {
						return invalid("rate missing")
					}
				} else {
					for depth := 1; depth <= int(count); depth++ {
						if !dimensions[grade+":"+strconv.Itoa(depth)] {
							return invalid("rate missing")
						}
					}
				}
			}
			for _, ppm := range depthMax {
				maxSpend += ppm
			}
		case "MARGINAL", "HIGHEST_RATE":
			if len(p.Tiers) == 0 {
				return invalid("tiers missing")
			}
			previous := new(big.Int)
			previousRate := int64(-1)
			for index, tier := range p.Tiers {
				lower, err := calculation.ParseDecimal(tier.Lower)
				if err != nil || lower.Atoms().Cmp(previous) != 0 {
					return invalid("tier gap")
				}
				rate, err := PPM(tier.PPM)
				if err != nil || rate < previousRate {
					return invalid("non-monotonic tiers")
				}
				previousRate = rate
				if tier.Upper == nil {
					if index != len(p.Tiers)-1 {
						return invalid("infinite tier")
					}
				} else {
					upper, err := calculation.ParseDecimal(*tier.Upper)
					if err != nil || upper.Atoms().Cmp(lower.Atoms()) <= 0 || index == len(p.Tiers)-1 {
						return invalid("tier upper")
					}
					previous = upper.Atoms()
				}
			}
			maxSpend = previousRate * count
		default:
			return fmt.Errorf("UNSUPPORTED_CAPABILITY")
		}
		if rule.Algorithm != "FIXED" && rule.Algorithm != "UNIT_PRICE" && rule.Algorithm != "WEIGHTED" {
			if rule.Funding == nil {
				return invalid("funding missing")
			}
			limit, err := Unsigned(*rule.Funding)
			if err != nil || big.NewInt(maxSpend).Cmp(limit) > 0 {
				return invalid("funding limit")
			}
		}
	}
	return nil
}
func comparison(operator string) bool {
	switch operator {
	case "EQ", "NE", "GT", "GTE", "LT", "LTE":
		return true
	}
	return false
}
func validateCondition(c Condition, currency string, depth int, leaves *int, top bool) error {
	if depth > 8 {
		return invalid("condition depth")
	}
	if c.Logic != "" {
		if c.Metric != "" || c.Operator != "" || len(c.Value) != 0 || c.Unit != "" {
			return invalid("mixed condition")
		}
		if c.Logic != "AND" && c.Logic != "OR" && c.Logic != "NOT" {
			return invalid("logic")
		}
		if len(c.Children) == 0 && !(top && c.Logic == "AND") {
			return invalid("empty condition group")
		}
		if c.Logic == "NOT" && len(c.Children) != 1 {
			return invalid("not arity")
		}
		for _, child := range c.Children {
			if err := validateCondition(child, currency, depth+1, leaves, false); err != nil {
				return err
			}
		}
		return nil
	}
	*leaves++
	if *leaves > 100 {
		return invalid("condition leaves")
	}
	if len(c.Children) != 0 {
		return invalid("leaf children")
	}
	if !comparison(c.Operator) && c.Operator != "IN" {
		return invalid("condition operator")
	}
	switch c.Metric {
	case "basis_amount", "quantity":
		var value string
		if err := json.Unmarshal(c.Value, &value); err != nil {
			return invalid("decimal condition")
		}
		if _, err := calculation.ParseDecimal(value); err != nil {
			return err
		}
		if c.Operator == "IN" || c.Unit == "" || c.Metric == "basis_amount" && currency != "" && c.Unit != currency {
			return invalid("metric unit")
		}
	case "confirmed":
		var value bool
		if err := json.Unmarshal(c.Value, &value); err != nil || c.Operator != "EQ" && c.Operator != "NE" || c.Unit != "BOOLEAN" {
			return invalid("boolean condition")
		}
	case "product_code", "scene":
		if c.Unit != "ENUM" {
			return invalid("enum unit")
		}
		if c.Operator == "IN" {
			var values []string
			if err := json.Unmarshal(c.Value, &values); err != nil || len(values) == 0 {
				return invalid("enum set")
			}
		} else {
			var value string
			if err := json.Unmarshal(c.Value, &value); err != nil || c.Operator != "EQ" && c.Operator != "NE" {
				return invalid("enum comparison")
			}
		}
	case "business_time":
		var value string
		if err := json.Unmarshal(c.Value, &value); err != nil {
			return invalid("time condition")
		}
		at, err := time.Parse(time.RFC3339Nano, value)
		if err != nil || at.Nanosecond()%1000 != 0 || c.Unit != "UTC" || c.Operator == "IN" {
			return invalid("time precision or unit")
		}
	default:
		return invalid("unknown metric")
	}
	return nil
}

// SelectGrade 使用已冻结的统计值，不授予资格，不读取系统时间。
func SelectGrade(grades []Grade, metrics map[string]string, currency string) (string, error) {
	ordered := append([]Grade(nil), grades...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Rank < ordered[j].Rank })
	selected := ""
	for _, grade := range ordered {
		matches := true
		for _, condition := range grade.Conditions {
			value, ok := metrics[condition.Metric]
			if !ok {
				return "", fmt.Errorf("UNKNOWN_FACT")
			}
			actual, err := calculation.ParseDecimal(value)
			if err != nil {
				return "", err
			}
			threshold, err := calculation.ParseDecimal(condition.Value)
			if err != nil {
				return "", err
			}
			cmp := actual.Atoms().Cmp(threshold.Atoms())
			match := false
			switch condition.Operator {
			case "EQ":
				match = cmp == 0
			case "NE":
				match = cmp != 0
			case "GT":
				match = cmp > 0
			case "GTE":
				match = cmp >= 0
			case "LT":
				match = cmp < 0
			case "LTE":
				match = cmp <= 0
			default:
				return "", invalid("grade operator")
			}
			if !match {
				matches = false
			}
		}
		if matches {
			selected = grade.ID
		}
	}
	if selected == "" {
		return "", invalid("base grade missing")
	}
	return selected, nil
}
