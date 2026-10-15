package ports

import (
	"context"
	money "github.com/ex-business/ExCommission/internal/ledger/domain"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	model "github.com/ex-business/ExCommission/internal/settlement/domain"
	"time"
)

type Ledger interface {
	Get(context.Context, db.Executor, foundation.Scope, string, bool) (money.Entitlement, error)
	PendingBefore(context.Context, db.Executor, foundation.Scope, time.Time, time.Time) ([]money.Entitlement, error)
	Transfer(context.Context, db.Executor, foundation.Scope, string, string, string, string, string, time.Time) error
}
type Repository interface {
	RunID(context.Context, db.Executor, foundation.Scope, string) (string, error)
	Owns(context.Context, db.Executor, foundation.Scope, string, string) (bool, error)
	Lock(context.Context, db.Executor, foundation.Scope) error
	ExistingRun(context.Context, db.Executor, foundation.Scope, string, string) ([]model.Statement, bool, error)
	CreateRun(context.Context, db.Executor, foundation.Scope, string, string, string, string, time.Time) error
	Get(context.Context, db.Executor, foundation.Scope, string, bool) (model.Statement, error)
	Create(context.Context, db.Executor, foundation.Scope, model.Statement, time.Time) error
	Claimed(context.Context, db.Executor, foundation.Scope, string) (bool, error)
	Claim(context.Context, db.Executor, foundation.Scope, model.Statement) error
	State(context.Context, db.Executor, foundation.Scope, model.Statement, string) error
}

type CalendarPolicy interface {
	ValidateCalendar(context.Context, db.Executor, foundation.Scope, string, time.Time) error
}
