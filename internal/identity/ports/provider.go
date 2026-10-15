package ports

import (
	"context"
	"github.com/ex-business/ExCommission/internal/identity/domain"
)

type Provider interface {
	Authenticate(context.Context, string) (domain.Principal, error)
}

type Authorizer interface {
	Authorize(context.Context, domain.Principal, string, string, string) error
}
