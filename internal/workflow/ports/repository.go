package ports

import (
	"context"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"github.com/ex-business/ExCommission/internal/workflow/domain"
	"time"
)

type Repository interface {
	Create(context.Context, db.Executor, foundation.Scope, domain.Request, string, time.Time) error
	Get(context.Context, db.Executor, foundation.Scope, string, bool) (domain.Request, error)
	Finish(context.Context, db.Executor, foundation.Scope, string, string, string, string, string, time.Time) error
}
