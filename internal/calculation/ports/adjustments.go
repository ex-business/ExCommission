package ports

import (
	"context"
	model "github.com/ex-business/ExCommission/internal/calculation/domain"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"time"
)

type Adjustments interface {
	Latest(context.Context, db.Executor, foundation.Scope, model.Task) (model.Input, uint64, error)
	Save(context.Context, db.Executor, foundation.Scope, string, string, uint64, model.Input, model.Result, string, time.Time) error
}
type AdjustmentLedger interface {
	RefundLedger
	Commit(context.Context, db.Executor, foundation.Scope, model.Task, model.Input, model.Result, time.Time) error
	Increase(context.Context, db.Executor, foundation.Scope, string, string, string, time.Time) error
}
