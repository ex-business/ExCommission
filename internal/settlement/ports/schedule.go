package ports

import (
	"context"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	model "github.com/ex-business/ExCommission/internal/settlement/domain"
	"time"
)

type ScheduleRepository interface {
	Schedule(context.Context, db.Executor, foundation.Scope) (model.Schedule, error)
	LockSchedule(context.Context, db.Executor, foundation.Scope, string, time.Time) (model.Schedule, error)
	Advance(context.Context, db.Executor, foundation.Scope, string, model.ScheduledResult, time.Time) error
}
type RequestHistory interface {
	HasRequest(context.Context, db.Executor, foundation.Scope, string, string) (bool, error)
}
type CalendarSelector interface {
	CalendarForPeriod(context.Context, db.Executor, foundation.Scope, time.Time) (string, error)
}
