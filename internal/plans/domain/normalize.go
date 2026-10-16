package domain

import (
	"encoding/json"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	"sort"
	"time"
)

func Normalize(config *Config) error {
	decimal := func(value *string) error {
		parsed, err := foundation.ParseDecimal(*value)
		if err != nil {
			return err
		}
		*value = parsed.String()
		return nil
	}
	integer := func(value *string) error {
		parsed, err := Unsigned(*value)
		if err != nil {
			return err
		}
		*value = parsed.String()
		return nil
	}
	optional := func(value *string) error {
		if value == nil {
			return nil
		}
		return integer(value)
	}
	if config.AgentPolicy.DayEnd != nil {
		sort.Ints(config.AgentPolicy.DayEnd.MemberDepths)
		sort.Ints(config.AgentPolicy.DayEnd.VolumeDepths)
	}
	sort.Strings(config.Scope.AgentIDs)
	for i := range config.Grades {
		for j := range config.Grades[i].Conditions {
			condition := &config.Grades[i].Conditions[j]
			if condition.Metric == "ACTIVE_DIRECT_COUNT" || condition.Metric == "EFFECTIVE_MEMBER_COUNT" {
				if err := integer(&condition.Value); err != nil {
					return err
				}
			} else if err := decimal(&condition.Value); err != nil {
				return err
			}
		}
	}
	for i := range config.Rules {
		rule := &config.Rules[i]
		if err := normalizeCondition(&rule.Condition); err != nil {
			return err
		}
		for _, value := range []*string{rule.Funding, rule.BeneficiaryCap, rule.EventCap} {
			if err := optional(value); err != nil {
				return err
			}
		}
		p := &rule.Parameters
		for _, value := range []*string{&p.RatePPM, &p.AmountMinor, &p.PoolMinor} {
			if *value != "" {
				if err := integer(value); err != nil {
					return err
				}
			}
		}
		if p.UnitPrice != "" {
			if err := decimal(&p.UnitPrice); err != nil {
				return err
			}
		}
		for j := range p.Rates {
			if err := integer(&p.Rates[j].PPM); err != nil {
				return err
			}
		}
		for j := range p.Tiers {
			tier := &p.Tiers[j]
			if err := decimal(&tier.Lower); err != nil {
				return err
			}
			if tier.Upper != nil {
				if err := decimal(tier.Upper); err != nil {
					return err
				}
			}
			if err := integer(&tier.PPM); err != nil {
				return err
			}
		}
		for j := range p.Beneficiaries {
			if err := integer(&p.Beneficiaries[j].Weight); err != nil {
				return err
			}
		}
	}
	if err := optional(config.EventBudget); err != nil {
		return err
	}
	if err := integer(&config.Settlement.Minimum); err != nil {
		return err
	}
	return optional(config.Settlement.Maximum)
}
func normalizeCondition(condition *Condition) error {
	if condition.Logic != "" {
		for i := range condition.Children {
			if err := normalizeCondition(&condition.Children[i]); err != nil {
				return err
			}
		}
		return nil
	}
	if condition.Metric == "basis_amount" || condition.Metric == "quantity" {
		var value string
		if err := json.Unmarshal(condition.Value, &value); err != nil {
			return err
		}
		parsed, err := foundation.ParseDecimal(value)
		if err != nil {
			return err
		}
		condition.Value, _ = json.Marshal(parsed.String())
	}
	if condition.Metric == "business_time" {
		var value string
		if err := json.Unmarshal(condition.Value, &value); err != nil {
			return err
		}
		parsed, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			return err
		}
		condition.Value, _ = json.Marshal(parsed.UTC().Format("2006-01-02T15:04:05.000000Z"))
	}
	return nil
}
