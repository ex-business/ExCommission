package ports

import (
	"context"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	model "github.com/ex-business/ExCommission/internal/reporting/domain"
	"time"
)

type Reader interface {
	Read(context.Context, foundation.Scope, string, model.Filters) (model.Report, error)
}
type Exports interface {
	Save(context.Context, db.Executor, foundation.Scope, string, string, model.Filters, model.Report, []byte, string, time.Time, time.Time) error
	Get(context.Context, db.Executor, foundation.Scope, string, time.Time) (model.Export, error)
}
