package ports

import (
	"context"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	model "github.com/ex-business/ExCommission/internal/sites/domain"
	"time"
)

type Repository interface {
	Current(context.Context, db.Executor, foundation.Scope, bool) (model.Config, error)
	At(context.Context, db.Executor, foundation.Scope, time.Time) (model.Config, error)
	Apply(context.Context, db.Executor, foundation.Scope, model.Change, string, time.Time) error
}
