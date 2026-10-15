package ports

import (
	"context"
	money "github.com/ex-business/ExCommission/internal/ledger/domain"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"time"
)

type RecoveryLedger interface {
	Get(context.Context, db.Executor, foundation.Scope, string, bool) (money.Entitlement, error)
	Recover(context.Context, db.Executor, foundation.Scope, string, uint64, string, string, string, string, time.Time) error
}
type CashEvidence interface {
	Verify(context.Context, foundation.Scope, string, string, string, string) (string, error)
}
