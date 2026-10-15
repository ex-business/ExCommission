package ports

import (
	"context"
	money "github.com/ex-business/ExCommission/internal/ledger/domain"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	model "github.com/ex-business/ExCommission/internal/risk/domain"
	"time"
)

type Ledger interface {
	Get(context.Context, db.Executor, foundation.Scope, string, bool) (money.Entitlement, error)
	Hold(context.Context, db.Executor, foundation.Scope, string, string, string, time.Time) error
	CloseHold(context.Context, db.Executor, foundation.Scope, string, string, time.Time) error
	Requeue(context.Context, db.Executor, foundation.Scope, string, string, time.Time) error
	Restrictions(context.Context, db.Executor, foundation.Scope, string) ([]model.Restriction, error)
	ForAgent(context.Context, db.Executor, foundation.Scope, string) ([]money.Entitlement, error)
}
type Repository interface {
	Create(context.Context, db.Executor, foundation.Scope, model.Hold, time.Time) error
	Get(context.Context, db.Executor, foundation.Scope, string, bool) (model.Hold, error)
	State(context.Context, db.Executor, foundation.Scope, model.Hold, string) error
	Appeal(context.Context, db.Executor, foundation.Scope, string, string, string, string, string, time.Time) error
}
type Accounts interface {
	SetStatus(context.Context, db.Executor, foundation.Scope, string, string, uint64, string, time.Time) error
	Revision(context.Context, db.Executor, foundation.Scope, string) (uint64, error)
}
