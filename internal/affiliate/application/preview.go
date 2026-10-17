package application

import (
	"context"
	model "github.com/ex-business/ExCommission/internal/affiliate/domain"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	"time"
)

// ConfigurationPreview 只读预检；迁移采用新方案，绝不把新币种或新自然日口径写入旧方案。
type ConfigurationPreview struct {
	Revision            uint64    `json:"revision"`
	Effective           time.Time `json:"earliest_effective_from"`
	Strategy            string    `json:"strategy"`
	RequiresIndependent bool      `json:"requires_independent_plan"`
	Changes             []string  `json:"independent_fields"`
	Depth               int       `json:"commission_depth"`
	HistoricalPolicy    string    `json:"historical_policy"`
}

func (s ConfigurationService) Preview(ctx context.Context, scope foundation.Scope, in model.Change) (ConfigurationPreview, error) {
	out := ConfigurationPreview{Strategy: "VERSION_UPDATE", Changes: []string{}, Depth: in.Config.DepthLimit(), HistoricalPolicy: "PRESERVE_AND_SETTLE"}
	if e := in.Normalize(); e != nil {
		return out, e
	}
	at := s.Now()
	old, e := s.Repo.Configuration(ctx, s.Query, scope, at, false)
	if e != nil {
		return out, e
	}
	out.Revision = old.Revision
	out.Effective = at
	if in.Revision != old.Revision {
		return out, foundation.ErrConflict
	}
	if old.Current != nil {
		c := old.Current.Config
		if in.Config.Currency != c.Currency || in.Config.CurrencyScale != c.CurrencyScale {
			out.Changes = append(out.Changes, "CURRENCY")
		}
		if in.Config.Timezone != c.Timezone {
			out.Changes = append(out.Changes, "TIMEZONE")
		}
		if in.Config.GradePeriod != c.GradePeriod {
			out.Changes = append(out.Changes, "GRADE_PERIOD")
		}
		_, out.Effective = c.Day(at)
	}
	if len(out.Changes) > 0 {
		out.RequiresIndependent = true
		out.Strategy = "NEW_INDEPENDENT_PLAN"
	} else {
		// 普通更新使用与真实申请相同的业务校验，预检通过不等于免除后续审批复核。
		if e := s.validate(ctx, s.Query, scope, in, at, false); e != nil {
			return out, e
		}
	}
	return out, nil
}
