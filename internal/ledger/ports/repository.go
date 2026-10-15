package ports

import (
	"context"
	"github.com/ex-business/ExCommission/internal/ledger/domain"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"time"
)

type Repository interface {
	Get(context.Context, db.Executor, foundation.Scope, string, bool) (domain.Entitlement, error)
	Transfer(context.Context, db.Executor, foundation.Scope, string, string, string, string, string, time.Time) error
}
