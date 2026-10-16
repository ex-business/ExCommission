package ports

import (
	"context"
	model "github.com/ex-business/ExCommission/internal/members/domain"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"time"
)

type Repository interface {
	OwnAgent(context.Context, db.Executor, foundation.Scope, string, bool) (string, error)
	Link(context.Context, db.Executor, foundation.Scope, string) (model.Link, error)
	Create(context.Context, db.Executor, foundation.Scope, string, string, time.Time) (model.Link, error)
	Resolve(context.Context, db.Executor, foundation.Scope, string) (string, error)
}
type Bindings interface {
	BindInitial(context.Context, db.Executor, foundation.Scope, string, string, string, time.Time) error
}
