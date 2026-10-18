package domain

import (
	"encoding/json"
	"errors"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	"time"
)

// Rule 按来源和标准业务类型选择引擎，EffectiveFrom按业务发生时间比较，不按接收时间切流。
type Rule struct {
	ID            string    `json:"id"`
	Source        string    `json:"source_id"`
	Kind          string    `json:"event_kind"`
	Engine        string    `json:"engine"`
	EffectiveFrom time.Time `json:"effective_from"`
}
type Config struct {
	Rules []Rule `json:"rules"`
}

func (c Config) Validate() error {
	if len(c.Rules) > 1024 {
		return foundation.ErrValidation
	}
	ids := map[string]bool{}
	times := map[string]bool{}
	for _, r := range c.Rules {
		if !foundation.ValidID(r.ID) || ids[r.ID] || r.EffectiveFrom.IsZero() {
			return foundation.ErrValidation
		}
		if (Claim{Source: r.Source, EventID: r.ID, BusinessID: r.ID, Kind: r.Kind, Engine: r.Engine}).Validate() != nil {
			return foundation.ErrValidation
		}
		raw, _ := json.Marshal([]string{r.Source, r.Kind, r.EffectiveFrom.UTC().Format(time.RFC3339Nano)})
		if times[string(raw)] {
			return foundation.ErrValidation
		}
		ids[r.ID] = true
		times[string(raw)] = true
	}
	return nil
}

// ValidateChange 只允许追加未来规则，禁止改删历史或回填生效点，以免重试或迟到事件改变引擎。
func ValidateChange(old, next Config, now time.Time) error {
	if err := next.Validate(); err != nil {
		return err
	}
	byID := map[string]Rule{}
	for _, r := range next.Rules {
		byID[r.ID] = r
	}
	for _, r := range old.Rules {
		n, ok := byID[r.ID]
		if !ok || n.Source != r.Source || n.Kind != r.Kind || n.Engine != r.Engine || !n.EffectiveFrom.Equal(r.EffectiveFrom) {
			return errors.New("STATE_CONFLICT:IMMUTABLE_EXECUTION_ROUTE")
		}
		delete(byID, r.ID)
	}
	if len(byID) == 0 {
		return foundation.ErrValidation
	}
	for _, r := range byID {
		if !r.EffectiveFrom.After(now) {
			return foundation.ErrValidation
		}
	}
	return nil
}

// Select 取业务发生时最近生效的规则；没有规则时保留旧入口行为，归属登记仍阻止双引擎受理。
func (c Config) Select(source, kind string, at time.Time) (Rule, bool) {
	var out Rule
	found := false
	for _, r := range c.Rules {
		if r.Source == source && r.Kind == kind && !r.EffectiveFrom.After(at) && (!found || r.EffectiveFrom.After(out.EffectiveFrom)) {
			out = r
			found = true
		}
	}
	return out, found
}
