package ports

import (
	"context"
	"github.com/ex-business/ExCommission/internal/calculation/domain"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"time"
)

type Tasks interface {
	ByBusiness(context.Context, db.Executor, foundation.Scope, string, string, string) (domain.Task, error)
	Create(context.Context, db.Executor, foundation.Scope, domain.Task, time.Time) error
	Get(context.Context, db.Executor, foundation.Scope, string) (domain.Task, error)
	BySource(context.Context, db.Executor, foundation.Scope, string, string) (domain.Task, error)
	ByEvent(context.Context, db.Executor, foundation.Scope, string) (domain.Task, error)
	Claim(context.Context, foundation.Scope, time.Time, time.Duration) (domain.Task, bool, error)
	SaveSnapshot(context.Context, db.Executor, foundation.Scope, domain.Task, []byte, string, time.Time) error
	Finish(context.Context, db.Executor, foundation.Scope, domain.Task, string, *string, []byte, time.Time) error
	Retry(context.Context, foundation.Scope, domain.Task, time.Time) error
	Resume(context.Context, db.Executor, foundation.Scope, string, uint64, string, time.Time) error
}
type SnapshotResolver interface {
	Resolve(context.Context, db.Executor, foundation.Scope, domain.Task) (domain.Input, error)
}
type Committer interface {
	Commit(context.Context, db.Executor, foundation.Scope, domain.Task, domain.Input, domain.Result, time.Time) error
}
