package ports

import (
	"context"
	model "github.com/ex-business/ExCommission/internal/affiliate/domain"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"time"
)

type Configurations interface {
	Ensure(context.Context, db.Executor, foundation.Scope) error
	Configuration(context.Context, db.Executor, foundation.Scope, time.Time, bool) (model.Configuration, error)
	Venues(context.Context, db.Executor, foundation.Scope) ([]model.Venue, error)
	Apply(context.Context, db.Executor, foundation.Scope, model.Change, string, time.Time) error
}
