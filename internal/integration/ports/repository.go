package ports

import (
	"context"
	"github.com/ex-business/ExCommission/internal/integration/domain"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"time"
)

type Repository interface {
	Accept(context.Context, db.Executor, foundation.Scope, domain.Event, []byte, time.Time) (string, error)
	SaveRaw(context.Context, db.Executor, foundation.Scope, domain.Event, []byte) error
}
type Source interface {
	Normalize([]byte, string) (domain.Event, error)
}
