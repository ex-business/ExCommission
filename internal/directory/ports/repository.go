package ports

import (
	"context"
	model "github.com/ex-business/ExCommission/internal/directory/domain"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"time"
)

type Repository interface {
	Save(context.Context, db.Executor, foundation.Scope, model.Statistics) error
	Rows(context.Context, db.Executor, foundation.Scope, string, string, time.Time, int) ([]model.Row, error)
}
