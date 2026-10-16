package ports

import (
	"context"
	calc "github.com/ex-business/ExCommission/internal/calculation/domain"
	model "github.com/ex-business/ExCommission/internal/dayend/domain"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	qualification "github.com/ex-business/ExCommission/internal/qualification/ports"
	"time"
)

type Repository interface {
	Lock(context.Context, db.Executor, foundation.Scope, string, string) error
	Get(context.Context, db.Executor, foundation.Scope, string, string) (model.Snapshot, error)
	Save(context.Context, db.Executor, foundation.Scope, model.Snapshot) error
}
type Tasks interface {
	DayFacts(context.Context, db.Executor, foundation.Scope, string, time.Time, time.Time, time.Time) ([]calc.Task, error)
	ReleaseDayEnd(context.Context, db.Executor, foundation.Scope, []string, time.Time) error
}
type EffectiveMembers interface {
	qualification.StatisticsReader
}

type Subjects interface {
	DayEndSubjects(context.Context, db.Executor, foundation.Scope, time.Time) ([]string, error)
}
