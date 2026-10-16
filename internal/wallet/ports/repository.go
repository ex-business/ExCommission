package ports

import (
	"context"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	model "github.com/ex-business/ExCommission/internal/wallet/domain"
	"time"
)

type Repository interface {
	Credit(context.Context, db.Executor, foundation.Scope, model.Credit, time.Time) error
}
