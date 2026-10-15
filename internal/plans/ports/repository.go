package ports

import (
	"context"
	"github.com/ex-business/ExCommission/internal/plans/domain"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"time"
)

type Repository interface {
	Create(context.Context, db.Executor, foundation.Scope, domain.Config, []byte, string, time.Time) error
	Read(context.Context, db.Executor, foundation.Scope, string, bool) (domain.Draft, error)
	Update(context.Context, db.Executor, foundation.Scope, domain.Config, []byte, string, uint64, time.Time) error
	SetPending(context.Context, db.Executor, foundation.Scope, string, uint64, string) error
	Reject(context.Context, db.Executor, foundation.Scope, string, uint64, string, time.Time) error
	Publish(context.Context, db.Executor, foundation.Scope, string, uint64, string, string, time.Time, time.Time) error
	Select(context.Context, db.Executor, foundation.Scope, domain.Route, time.Time) (domain.Version, error)
	AtPlan(context.Context, db.Executor, foundation.Scope, string, time.Time) (domain.Version, error)
	VersionPage(context.Context, db.Executor, foundation.Scope, string, int, int) ([]domain.Version, bool, error)
	Versions(context.Context, db.Executor, foundation.Scope, string) ([]domain.Version, error)
}
type Validator interface {
	Decode([]byte) (domain.Config, []byte, error)
}
