package application

import (
	"context"
	plan "github.com/ex-business/ExCommission/internal/plans/domain"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"time"
)

func dayBoundary(config plan.Config, at time.Time) error {
	if config.AgentPolicy.GradeWindow != "BUSINESS_DAY" {
		return nil
	}
	d := config.AgentPolicy.DayEnd
	if d == nil {
		return foundation.ErrValidation
	}
	loc, err := time.LoadLocation(d.Timezone)
	if err != nil {
		return foundation.ErrValidation
	}
	local := at.In(loc)
	if local.Hour() != 0 || local.Minute() != 0 || local.Second() != 0 || local.Nanosecond() != 0 {
		return foundation.ErrValidation
	}
	return nil
}
func (s Service) validateDayEndPublication(ctx context.Context, tx db.Executor, scope foundation.Scope, config plan.Config, at time.Time) error {
	if err := dayBoundary(config, at); err != nil {
		return err
	}
	prior, err := s.Repo.AtPlan(ctx, tx, scope, config.PlanID, at.Add(-time.Microsecond))
	if err != nil {
		if err.Error() == "NO_EFFECTIVE_PLAN_VERSION" {
			return nil
		}
		return err
	}
	return dayBoundary(prior.Config, at)
}
