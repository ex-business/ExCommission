package ports

import (
	"context"
	model "github.com/ex-business/ExCommission/internal/affiliateflow/domain"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"time"
)

type ReversalSource interface {
	VerifyReversal(context.Context, foundation.Scope, []byte, string) (model.Reversal, error)
}
type Reversals interface {
	ReversalEvents(context.Context, db.Executor, foundation.Scope) ([]model.Reversal, error)
	RecordReversal(context.Context, db.Executor, foundation.Scope, model.Reversal, time.Time) (bool, error)
	ReversalDays(context.Context, db.Executor, foundation.Scope) ([]model.Daily, error)
	ReversalBaseline(context.Context, db.Executor, foundation.Scope, model.Reward) (model.Reward, error)
	SaveReversalAdjustment(context.Context, db.Executor, foundation.Scope, model.ReversalAdjustment) error
	ReversalRecords(context.Context, db.Executor, foundation.Scope) ([]model.ReversalAdjustment, error)
}
type ReversalFunds interface {
	Adjust(context.Context, db.Executor, foundation.Scope, model.Reward, string, string, time.Time) (string, string, error)
}
