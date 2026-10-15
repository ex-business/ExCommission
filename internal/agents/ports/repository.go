package ports

import (
	"context"
	"github.com/ex-business/ExCommission/internal/agents/domain"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"time"
)

type Repository interface {
	Create(context.Context, db.Executor, foundation.Scope, string, string, string, time.Time) error
	Revision(context.Context, db.Executor, foundation.Scope, string) (uint64, error)
	Move(context.Context, db.Executor, foundation.Scope, domain.RelationChange, time.Time) error
	Ancestors(context.Context, db.Executor, foundation.Scope, string, time.Time, int, int) ([]domain.Ancestor, bool, error)
}

type Contracts interface {
	AddContract(context.Context, db.Executor, foundation.Scope, domain.Contract) error
	ContractAt(context.Context, db.Executor, foundation.Scope, string, string, time.Time) (domain.Contract, error)
}
