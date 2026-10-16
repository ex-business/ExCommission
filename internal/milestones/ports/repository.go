package ports

import (
	"context"
	"github.com/ex-business/ExCommission/internal/milestones/domain"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"time"
)

type ProgramRecord struct {
	Program    domain.Program `json:"program"`
	Hash       string         `json:"config_hash"`
	ApprovalID string         `json:"approval_id"`
}
type Repository interface {
	Program(context.Context, db.Executor, foundation.Scope, string) (ProgramRecord, error)
	Activate(context.Context, db.Executor, foundation.Scope, ProgramRecord, time.Time) error
	LockMember(context.Context, db.Executor, foundation.Scope, string, domain.Progress, time.Time) (domain.Progress, error)
	Member(context.Context, db.Executor, foundation.Scope, string, string) (domain.Progress, error)
	SaveMember(context.Context, db.Executor, foundation.Scope, string, domain.Progress, time.Time) error
	Receipt(context.Context, db.Executor, foundation.Scope, string, string) (string, foundation.CommandResult, error)
	SaveReceipt(context.Context, db.Executor, foundation.Scope, domain.Observation, []byte, string, foundation.CommandResult, time.Time) error
}
