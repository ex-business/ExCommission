package ports

import (
	"context"
	model "github.com/ex-business/ExCommission/internal/payments/domain"
)

type Provider interface {
	Enabled() bool
	Submit(context.Context, model.Payment) (model.Outcome, error)
	Query(context.Context, model.Payment) (model.Outcome, error)
	VerifyCallback([]byte, string) (model.Outcome, error)
	Reconcile(context.Context, string) ([]model.Outcome, error)
}
