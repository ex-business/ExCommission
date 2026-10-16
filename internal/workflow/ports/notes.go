package ports

import (
	"context"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"time"
)

type DecisionNotes interface {
	Note(context.Context, db.Executor, foundation.Scope, string, uint64, string, string, time.Time) error
}
