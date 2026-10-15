package ports

import (
	"context"
	plan "github.com/ex-business/ExCommission/internal/plans/domain"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"time"
)

type Simulation struct {
	ID, PlanID, ConfigHash, SnapshotHash string
	Snapshot, Result                     []byte
	Passed                               bool
	At                                   time.Time
}
type Simulations interface {
	LatestPassed(context.Context, db.Executor, foundation.Scope, string) (Simulation, error)
	Save(context.Context, db.Executor, foundation.Scope, Simulation) error
	Proof(context.Context, db.Executor, foundation.Scope, string) (Simulation, error)
	ByHash(context.Context, db.Executor, foundation.Scope, string) (plan.Draft, error)
	ByVersion(context.Context, db.Executor, foundation.Scope, string) (plan.Version, error)
}
