package adapters

import (
	"context"
	"errors"
	payment "github.com/ex-business/ExCommission/internal/payments/domain"
)

// Provider authorizes an internal wallet transfer. The actual credit occurs atomically in payment.Apply.
type Provider struct{}

func (Provider) Enabled() bool { return true }
func (Provider) Submit(ctx context.Context, p payment.Payment) (payment.Outcome, error) {
	if err := ctx.Err(); err != nil {
		return payment.Outcome{}, err
	}
	receipt := "wallet-" + p.MerchantID
	return payment.Outcome{MerchantID: p.MerchantID, Currency: p.Currency, Amount: p.Amount, Status: "SUCCEEDED", Receipt: &receipt}, nil
}
func (p Provider) Query(ctx context.Context, v payment.Payment) (payment.Outcome, error) {
	return p.Submit(ctx, v)
}
func (Provider) VerifyCallback([]byte, string) (payment.Outcome, error) {
	return payment.Outcome{}, errors.New("PERMISSION_DENIED")
}
func (Provider) Reconcile(context.Context, string) ([]payment.Outcome, error) {
	return nil, errors.New("UNSUPPORTED_CAPABILITY")
}

func (Provider) Supports(currency string) bool { return currency == "USD" || currency == "BTC" }
