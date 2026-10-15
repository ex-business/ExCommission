package ports

import (
	"context"
	model "github.com/ex-business/ExCommission/internal/calculation/domain"
	money "github.com/ex-business/ExCommission/internal/ledger/domain"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"time"
)

type TargetPolicy interface {
	Prepare(context.Context, db.Executor, foundation.Scope, model.Task, model.Input, model.Result) (model.Result, error)
}
type RefundRepository interface {
	Lock(context.Context, db.Executor, foundation.Scope, string, string, string) error
	Accept(context.Context, db.Executor, foundation.Scope, model.RefundRecord, []byte, time.Time) (model.RefundRecord, error)
	Records(context.Context, db.Executor, foundation.Scope, string, string, string) ([]model.RefundRecord, error)
	State(context.Context, db.Executor, foundation.Scope, string, string, *string, time.Time) error
	Get(context.Context, db.Executor, foundation.Scope, string) (model.RefundRecord, error)
	Pending(context.Context, db.Executor, foundation.Scope) ([]model.RefundRecord, error)
}
type RefundLedger interface {
	CloseHold(context.Context, db.Executor, foundation.Scope, string, string, time.Time) error
	TaskEntitlements(context.Context, db.Executor, foundation.Scope, string) ([]money.Entitlement, error)
	Reduce(context.Context, db.Executor, foundation.Scope, string, string, string, time.Time) error
	Hold(context.Context, db.Executor, foundation.Scope, string, string, string, time.Time) error
}
