package ports

import (
	"context"
	model "github.com/ex-business/ExCommission/internal/disbursement/domain"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"time"
)

type Repository interface {
	Create(context.Context, db.Executor, foundation.Scope, model.Instruction) error
	Get(context.Context, db.Executor, foundation.Scope, string, bool) (model.Instruction, error)
	Update(context.Context, db.Executor, foundation.Scope, model.Instruction, uint64) error
	Queued(context.Context, db.Executor, foundation.Scope) ([]model.Instruction, error)
}

// Clock is supplied by the application for deterministic business-time tests.
type Clock func() time.Time
