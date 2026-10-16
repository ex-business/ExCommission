package application

import (
	"context"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	model "github.com/ex-business/ExCommission/internal/wallet/domain"
	"github.com/ex-business/ExCommission/internal/wallet/ports"
	"math/big"
	"time"
)

type Service struct{ Repo ports.Repository }

func (s Service) Credit(ctx context.Context, tx db.Executor, scope foundation.Scope, c model.Credit, at time.Time) error {
	n, ok := new(big.Int).SetString(c.Amount, 10)
	if !ok || n.Sign() <= 0 || n.String() != c.Amount || len(c.Amount) > 38 || !scope.Valid() {
		return foundation.ErrValidation
	}
	for _, id := range []string{c.UserID, c.Currency, c.Reference, c.PaymentID, c.StatementID} {
		if !foundation.ValidID(id) {
			return foundation.ErrValidation
		}
	}
	return s.Repo.Credit(ctx, tx, scope, c, at)
}
