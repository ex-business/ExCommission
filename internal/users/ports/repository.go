package ports

import (
	"context"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"github.com/ex-business/ExCommission/internal/users/domain"
	"time"
)

type Job struct {
	ID        string   `json:"job_id"`
	Status    string   `json:"status"`
	Watermark *string  `json:"watermark"`
	Success   int      `json:"success_count"`
	Failure   int      `json:"failure_count"`
	ErrorRefs []string `json:"error_refs"`
}
type Repository interface {
	Upsert(context.Context, db.Executor, foundation.Scope, string, domain.Record, time.Time) (string, error)
	SaveJob(context.Context, db.Executor, foundation.Scope, Job, string, string, time.Time) error
	GetJob(context.Context, db.Executor, foundation.Scope, string) (Job, error)
}
type UserLookup interface {
	Exists(context.Context, db.Executor, foundation.Scope, string) error
}
