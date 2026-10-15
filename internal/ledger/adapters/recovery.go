package adapters

import (
	"context"
	"errors"
	model "github.com/ex-business/ExCommission/internal/ledger/domain"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"math/big"
	"time"
)

func (r Repository) OldestDebt(ctx context.Context, tx db.Executor, s foundation.Scope, agent, currency string) (string, error) {
	var id string
	err := tx.QueryRowContext(ctx, `SELECT e.entitlement_id FROM ledger_entitlements e JOIN ledger_debt_ages d ON d.tenant_id=e.tenant_id AND d.app_id=e.app_id AND d.entitlement_id=e.entitlement_id WHERE e.tenant_id=? AND e.app_id=? AND e.beneficiary_id=? AND e.currency=? AND e.debt>0 ORDER BY d.created_at,e.entitlement_id LIMIT 1`, s.Tenant, s.App, agent, currency).Scan(&id)
	return id, err
}
func (r Repository) AvailableFor(ctx context.Context, tx db.Executor, s foundation.Scope, agent, currency string) ([]model.Entitlement, error) {
	rows, err := tx.QueryContext(ctx, `SELECT entitlement_id FROM ledger_entitlements WHERE tenant_id=? AND app_id=? AND beneficiary_id=? AND currency=? AND available>0 ORDER BY posted_at,entitlement_id`, s.Tenant, s.App, agent, currency)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := []model.Entitlement{}
	for _, id := range ids {
		e, err := r.Get(ctx, tx, s, id, true)
		if err != nil {
			return nil, err
		}
		if !e.Frozen {
			out = append(out, e)
		}
	}
	return out, nil
}
func (r Repository) Recover(ctx context.Context, tx db.Executor, s foundation.Scope, id string, revision uint64, method, amount, reference, evidence string, now time.Time) error {
	if !validAmount(amount) || (method != "FUTURE_OFFSET" && method != "CASH_CONFIRMED") {
		return foundation.ErrValidation
	}
	e, err := r.Get(ctx, tx, s, id, true)
	if err != nil {
		return err
	}
	if e.Revision != revision {
		return errors.New("STALE_APPROVAL")
	}
	first, err := r.OldestDebt(ctx, tx, s, e.AgentID, e.Currency)
	if err != nil {
		return err
	}
	if first != id {
		return errors.New("FIFO_DEBT_REQUIRED")
	}
	n, _ := new(big.Int).SetString(amount, 10)
	debt, _ := new(big.Int).SetString(e.Debt, 10)
	if debt.Cmp(n) < 0 {
		return errors.New("INSUFFICIENT_BALANCE")
	}
	if method == "FUTURE_OFFSET" {
		funds, err := r.AvailableFor(ctx, tx, s, e.AgentID, e.Currency)
		if err != nil {
			return err
		}
		remaining := new(big.Int).Set(n)
		for _, fund := range funds {
			if remaining.Sign() == 0 {
				break
			}
			available, _ := new(big.Int).SetString(fund.Available, 10)
			take := new(big.Int).Set(available)
			if take.Cmp(remaining) > 0 {
				take.Set(remaining)
			}
			available.Sub(available, take)
			paid, _ := new(big.Int).SetString(fund.Paid, 10)
			paid.Add(paid, take)
			if len(paid.String()) > 38 {
				return errors.New("AMOUNT_OUT_OF_RANGE")
			}
			if _, err = tx.ExecContext(ctx, `UPDATE ledger_entitlements SET available=?,paid=?,revision=revision+1 WHERE tenant_id=? AND app_id=? AND entitlement_id=?`, available.String(), paid.String(), s.Tenant, s.App, fund.ID); err != nil {
				return err
			}
			if err = appendJournal(ctx, tx, s, stableID(reference, fund.ID), reference, fund.ID, e.Currency, "PAYABLE_AVAILABLE", "AGENT_RECEIVABLE", take.String(), now); err != nil {
				return err
			}
			remaining.Sub(remaining, take)
		}
		if remaining.Sign() != 0 {
			return errors.New("INSUFFICIENT_BALANCE")
		}
	} else {
		if err = appendJournal(ctx, tx, s, stableID(reference, "cash"), reference, id, e.Currency, "CASH_CLEARING", "AGENT_RECEIVABLE", amount, now); err != nil {
			return err
		}
	}
	// Read current recovered because an offset may also consume available on this same entitlement.
	current, err := r.Get(ctx, tx, s, id, true)
	if err != nil {
		return err
	}
	recovered, _ := new(big.Int).SetString(current.Recovered, 10)
	recovered.Add(recovered, n)
	debt.Sub(debt, n)
	if len(recovered.String()) > 38 {
		return errors.New("AMOUNT_OUT_OF_RANGE")
	}
	if _, err = tx.ExecContext(ctx, `UPDATE ledger_entitlements SET debt=?,recovered=?,revision=revision+1 WHERE tenant_id=? AND app_id=? AND entitlement_id=?`, debt.String(), recovered.String(), s.Tenant, s.App, id); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO ledger_recoveries VALUES(?,?,?,?,?,?,?,?)`, s.Tenant, s.App, reference, id, method, amount, evidence, now)
	return err
}
