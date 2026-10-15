package ports

import (
	"context"
	"github.com/ex-business/ExCommission/internal/agents/domain"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"time"
)

type Grades interface {
	GradeAt(context.Context, db.Executor, foundation.Scope, string, string, time.Time) (domain.GradeState, bool, error)
	GradeRevision(context.Context, db.Executor, foundation.Scope, string, string) (uint64, error)
	ApplyGrade(context.Context, db.Executor, foundation.Scope, domain.GradeChange, string, string, time.Time) error
}

type GradeSubjects interface {
	SubjectsAt(context.Context, db.Executor, foundation.Scope, time.Time) ([]string, error)
}
