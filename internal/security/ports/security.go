package ports

import (
	"context"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
)

type Secrets interface {
	Secret(context.Context, foundation.Scope, string) ([]byte, error)
}
type Repository interface {
	Attempt(context.Context, db.Executor, foundation.Scope, string, int64) (int, error)
	Consume(context.Context, db.Executor, foundation.Scope, string, uint64, string) error
}
