package ports

import (
	"context"
	"github.com/ex-business/ExCommission/internal/platform/domain"
)

type Commands interface {
	Lookup(context.Context, domain.Scope, string, string, any) (domain.CommandResult, bool, error)
	Execute(context.Context, domain.Scope, string, string, any, func(Executor) (domain.CommandResult, error)) (domain.CommandResult, error)
}
