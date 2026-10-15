package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	identity "github.com/ex-business/ExCommission/internal/identity/domain"
	model "github.com/ex-business/ExCommission/internal/payments/domain"
	"github.com/ex-business/ExCommission/internal/payments/ports"
	response "github.com/ex-business/ExCommission/internal/platform/application"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"math/big"
	"time"
)

type CreateInput struct {
	StatementID string      `json:"statement_id"`
	Amount      model.Money `json:"amount"`
	Channel     string      `json:"channel_id"`
	Revision    uint64      `json:"expected_statement_revision"`
}
type Service struct {
	Commands   db.Commands
	UOW        db.UnitOfWork
	Query      db.Executor
	Repo       ports.Repository
	Ledger     ports.Ledger
	Statements ports.Statements
	Providers  map[string]ports.Provider
	Now        func() time.Time
	ID         func() string
}

func number(s string) (*big.Int, bool) {
	n, ok := new(big.Int).SetString(s, 10)
	return n, ok && n.Sign() > 0 && n.String() == s && len(s) <= 38
}
func (s Service) Create(ctx context.Context, p identity.Principal, input CreateInput, key, request string) (foundation.CommandResult, error) {
	saved, found, err := s.Commands.Lookup(ctx, foundation.Scope{Tenant: p.Tenant, App: p.App}, "payment-create", key, input)
	if err != nil || found {
		return saved, err
	}
	n, ok := number(input.Amount.Minor)
	provider, exists := s.Providers[input.Channel]
	if !ok || !exists || !provider.Enabled() {
		return foundation.CommandResult{}, foundation.ErrValidation
	}
	scope := foundation.Scope{Tenant: p.Tenant, App: p.App}
	return s.Commands.Execute(ctx, scope, "payment-create", key, input, func(tx db.Executor) (foundation.CommandResult, error) {
		v, err := s.Statements.Get(ctx, tx, scope, input.StatementID, true)
		if err != nil {
			return foundation.CommandResult{}, err
		}
		if v.Revision != input.Revision {
			return foundation.CommandResult{}, errors.New("REVISION_CONFLICT")
		}
		if (v.Status != "APPROVED" && v.Status != "PARTIALLY_PAID") || v.Currency != input.Amount.Currency {
			return foundation.CommandResult{}, errors.New("STATE_CONFLICT")
		}
		payment := model.Payment{ID: s.ID(), StatementID: v.ID, ChannelID: input.Channel, Currency: v.Currency, Amount: input.Amount.Minor, Status: "CREATED", Reconciliation: "PENDING", Allocations: []model.Allocation{}}
		b, _ := json.Marshal([]string{scope.Tenant, scope.App, payment.ID})
		h := sha256.Sum256(b)
		payment.MerchantID = hex.EncodeToString(h[:])
		remaining := new(big.Int).Set(n)
		for _, item := range v.Items {
			e, err := s.Ledger.Get(ctx, tx, scope, item.ID, true)
			if err != nil {
				return foundation.CommandResult{}, err
			}
			owns, err := s.Statements.Owns(ctx, tx, scope, v.ID, item.ID)
			if err != nil {
				return foundation.CommandResult{}, err
			}
			if e.Frozen || e.Pending != "0" || !owns {
				return foundation.CommandResult{}, errors.New("STALE_APPROVAL")
			}
			available, _ := new(big.Int).SetString(e.Available, 10)
			if remaining.Sign() == 0 || available.Sign() == 0 {
				continue
			}
			take := new(big.Int).Set(available)
			if take.Cmp(remaining) > 0 {
				take.Set(remaining)
			}
			payment.Allocations = append(payment.Allocations, model.Allocation{EntitlementID: item.ID, Amount: take.String()})
			remaining.Sub(remaining, take)
		}
		if remaining.Sign() != 0 {
			return foundation.CommandResult{}, errors.New("INSUFFICIENT_BALANCE")
		}
		for _, a := range payment.Allocations {
			if err = s.Ledger.Transfer(ctx, tx, scope, a.EntitlementID, "available", "reserved", a.Amount, "reserve-"+payment.ID, s.Now()); err != nil {
				return foundation.CommandResult{}, err
			}
		}
		if err = s.Repo.Create(ctx, tx, scope, payment, s.Now()); err != nil {
			return foundation.CommandResult{}, err
		}
		if err = s.Statements.PaymentState(ctx, tx, scope, v, v.Status); err != nil {
			return foundation.CommandResult{}, err
		}
		return response.Result(202, payment.ID, request, map[string]string{"payment_id": payment.ID, "merchant_payment_id": payment.MerchantID, "status": "CREATED"})
	})
}

// Dispatch records submission before the network call; a crash leaves the original ID queryable.
func (s Service) Dispatch(ctx context.Context, scope foundation.Scope, id string) error {
	var p model.Payment
	send := false
	err := s.UOW.Within(ctx, func(tx db.Executor) error {
		var err error
		p, err = s.Repo.Get(ctx, tx, scope, id, true)
		if err != nil {
			return err
		}
		if p.Status != "CREATED" {
			return nil
		}
		provider, ok := s.Providers[p.ChannelID]
		if !ok || !provider.Enabled() {
			return errors.New("UNSUPPORTED_CAPABILITY")
		}
		send = true
		return s.Repo.State(ctx, tx, scope, p, "SUBMITTED", "PENDING", nil, s.Now())
	})
	if err != nil || !send {
		return err
	}
	outcome, err := s.Providers[p.ChannelID].Submit(ctx, p)
	if err != nil {
		outcome = model.Outcome{MerchantID: p.MerchantID, Currency: p.Currency, Amount: p.Amount, Status: "UNKNOWN"}
	}
	if ctx.Err() != nil {
		recovery, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		ctx = recovery
	}
	return s.Apply(ctx, scope, p.ChannelID, outcome)
}
func (s Service) Verify(ctx context.Context, scope foundation.Scope, id string) error {
	p, err := s.Repo.Get(ctx, s.Query, scope, id, false)
	if err != nil {
		return err
	}
	provider, ok := s.Providers[p.ChannelID]
	if !ok {
		return errors.New("UNSUPPORTED_CAPABILITY")
	}
	outcome, err := provider.Query(ctx, p)
	if err != nil {
		return err
	}
	return s.Apply(ctx, scope, p.ChannelID, outcome)
}
func (s Service) Apply(ctx context.Context, scope foundation.Scope, channel string, outcome model.Outcome) error {
	if outcome.Status != "UNKNOWN" && !model.Terminal(outcome.Status) {
		return foundation.ErrValidation
	}
	if _, ok := number(outcome.Amount); !ok {
		return foundation.ErrValidation
	}
	return s.UOW.Within(ctx, func(tx db.Executor) error {
		found, err := s.Repo.ByMerchant(ctx, tx, scope, outcome.MerchantID)
		if err != nil {
			return err
		}
		// Lock statement before payment, consistently with payment creation.
		statement, err := s.Statements.Get(ctx, tx, scope, found.StatementID, true)
		if err != nil {
			return err
		}
		p, err := s.Repo.Get(ctx, tx, scope, found.ID, true)
		if err != nil {
			return err
		}
		if p.ChannelID != channel {
			return errors.New("SCOPE_MISMATCH")
		}
		exception := outcome.Currency != p.Currency || outcome.Amount != p.Amount || (model.Terminal(p.Status) && model.Terminal(outcome.Status) && p.Status != outcome.Status)
		if outcome.CallbackID != "" {
			body, _ := json.Marshal(outcome)
			h := sha256.Sum256(body)
			fresh, e := s.Repo.Callback(ctx, tx, scope, channel, outcome.CallbackID, hex.EncodeToString(h[:]), p.ID, s.Now())
			if e != nil {
				if errors.Is(e, foundation.ErrConflict) {
					exception = true
				} else {
					return e
				}
			} else if !fresh {
				return nil
			}
		}
		if exception {
			for _, a := range p.Allocations {
				if err = s.Ledger.Hold(ctx, tx, scope, a.EntitlementID, "payment-"+p.ID, "RECONCILIATION_EXCEPTION", s.Now()); err != nil {
					return err
				}
			}
			return s.Repo.State(ctx, tx, scope, p, p.Status, "RECONCILIATION_EXCEPTION", outcome.Receipt, s.Now())
		}
		if model.Terminal(p.Status) {
			return nil
		}
		if outcome.Status == "UNKNOWN" {
			return s.Repo.State(ctx, tx, scope, p, "UNKNOWN", "PENDING", outcome.Receipt, s.Now())
		}
		to := "paid"
		if outcome.Status == "FAILED_FINAL" {
			to = "available"
		}
		for _, a := range p.Allocations {
			if err = s.Ledger.Transfer(ctx, tx, scope, a.EntitlementID, "reserved", to, a.Amount, "result-"+p.ID, s.Now()); err != nil {
				return err
			}
		}
		if err = s.Repo.State(ctx, tx, scope, p, outcome.Status, "MATCHED", outcome.Receipt, s.Now()); err != nil {
			return err
		}
		status := "APPROVED"
		paid := new(big.Int)
		unpaid := new(big.Int)
		for _, item := range statement.Items {
			e, err := s.Ledger.Get(ctx, tx, scope, item.ID, true)
			if err != nil {
				return err
			}
			v, _ := new(big.Int).SetString(e.Paid, 10)
			paid.Add(paid, v)
			for _, a := range []string{e.Pending, e.Available, e.Reserved} {
				v, _ = new(big.Int).SetString(a, 10)
				unpaid.Add(unpaid, v)
			}
		}
		if paid.Sign() > 0 {
			status = "PARTIALLY_PAID"
		}
		if unpaid.Sign() == 0 {
			status = "PAID"
		}
		return s.Statements.PaymentState(ctx, tx, scope, statement, status)
	})
}
func (s Service) Callback(ctx context.Context, scope foundation.Scope, channel string, raw []byte, signature string) error {
	provider, ok := s.Providers[channel]
	if !ok {
		return errors.New("NOT_FOUND")
	}
	outcome, err := provider.VerifyCallback(raw, signature)
	if err != nil {
		return err
	}
	return s.Apply(ctx, scope, channel, outcome)
}
func (s Service) Reconcile(ctx context.Context, scope foundation.Scope, channel, ref string) error {
	provider, ok := s.Providers[channel]
	if !ok {
		return errors.New("NOT_FOUND")
	}
	rows, err := provider.Reconcile(ctx, ref)
	if err != nil {
		return err
	}
	for _, outcome := range rows {
		if err = s.Apply(ctx, scope, channel, outcome); err != nil {
			return err
		}
	}
	return nil
}

// Recover only sends never-submitted orders; ambiguous submissions are queried by the original ID.
func (s Service) Recover(ctx context.Context, scope foundation.Scope) error {
	payments, err := s.Repo.Pending(ctx, s.Query, scope)
	if err != nil {
		return err
	}
	for _, p := range payments {
		if p.Status == "CREATED" {
			err = s.Dispatch(ctx, scope, p.ID)
		} else {
			err = s.Verify(ctx, scope, p.ID)
		}
		if err != nil {
			return err
		}
	}
	return nil
}
