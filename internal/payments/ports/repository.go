package ports

import (
	"context"
	money "github.com/ex-business/ExCommission/internal/ledger/domain"
	model "github.com/ex-business/ExCommission/internal/payments/domain"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	statement "github.com/ex-business/ExCommission/internal/settlement/domain"
	"time"
)

type Repository interface {
	Pending(context.Context, db.Executor, foundation.Scope) ([]model.Payment, error)
	Create(context.Context, db.Executor, foundation.Scope, model.Payment, time.Time) error
	Get(context.Context, db.Executor, foundation.Scope, string, bool) (model.Payment, error)
	ByMerchant(context.Context, db.Executor, foundation.Scope, string) (model.Payment, error)
	State(context.Context, db.Executor, foundation.Scope, model.Payment, string, string, *string, time.Time) error
	Callback(context.Context, db.Executor, foundation.Scope, string, string, string, string, time.Time) (bool, error)
}
type Ledger interface {
	Get(context.Context, db.Executor, foundation.Scope, string, bool) (money.Entitlement, error)
	Transfer(context.Context, db.Executor, foundation.Scope, string, string, string, string, string, time.Time) error
	Hold(context.Context, db.Executor, foundation.Scope, string, string, string, time.Time) error
}
type Statements interface {
	Owns(context.Context, db.Executor, foundation.Scope, string, string) (bool, error)
	Get(context.Context, db.Executor, foundation.Scope, string, bool) (statement.Statement, error)
	PaymentState(context.Context, db.Executor, foundation.Scope, statement.Statement, string) error
}

type PaymentAccounts interface {
	PaymentAllowed(context.Context, db.Executor, foundation.Scope, string) error
}
