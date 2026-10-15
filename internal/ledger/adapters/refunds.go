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

func (r Repository) TaskEntitlements(ctx context.Context, tx db.Executor, s foundation.Scope, task string) ([]model.Entitlement, error) {
	rows, err := tx.QueryContext(ctx, `SELECT entitlement_id FROM ledger_entitlements WHERE tenant_id=? AND app_id=? AND task_id=? ORDER BY entitlement_id`, s.Tenant, s.App, task)
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
		out = append(out, e)
	}
	return out, nil
}
func (r Repository) Reduce(ctx context.Context, tx db.Executor, s foundation.Scope, id, target, reference string, now time.Time) error {
	net, ok := new(big.Int).SetString(target, 10)
	if !ok || net.Sign() < 0 || net.String() != target || len(target) > 38 {
		return foundation.ErrValidation
	}
	e, err := r.Get(ctx, tx, s, id, true)
	if err != nil {
		return err
	}
	if e.Reserved != "0" {
		return errors.New("PAYMENT_RESULT_UNKNOWN")
	}
	old, _ := new(big.Int).SetString(e.Target, 10)
	delta := new(big.Int).Sub(old, net)
	if delta.Sign() < 0 {
		return errors.New("REFUND_TARGET_INCREASE")
	}
	if delta.Sign() == 0 {
		return nil
	}
	pending, _ := new(big.Int).SetString(e.Pending, 10)
	available, _ := new(big.Int).SetString(e.Available, 10)
	debt, _ := new(big.Int).SetString(e.Debt, 10)
	post := func(bucket string, balance *big.Int, account string) error {
		take := new(big.Int).Set(balance)
		if take.Cmp(delta) > 0 {
			take.Set(delta)
		}
		if take.Sign() == 0 {
			return nil
		}
		balance.Sub(balance, take)
		delta.Sub(delta, take)
		return appendJournal(ctx, tx, s, stableID(id, reference, bucket), reference, e.Currency, account, "COMMISSION_EXPENSE", take.String(), now)
	}
	if err = post("pending", pending, "PAYABLE_PENDING"); err != nil {
		return err
	}
	if err = post("available", available, "PAYABLE_AVAILABLE"); err != nil {
		return err
	}
	if delta.Sign() > 0 {
		debt.Add(debt, delta)
		if err = appendJournal(ctx, tx, s, stableID(id, reference, "debt"), reference, e.Currency, "AGENT_RECEIVABLE", "COMMISSION_EXPENSE", delta.String(), now); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT IGNORE INTO ledger_debt_ages VALUES(?,?,?,?)`, s.Tenant, s.App, id, now); err != nil {
			return err
		}
	}

	if available.Sign() > 0 {
		amount := available.String()
		pending.Add(pending, available)
		available.SetInt64(0)
		if err = appendJournal(ctx, tx, s, stableID(id, reference, "reapproval"), reference, e.Currency, "PAYABLE_AVAILABLE", "PAYABLE_PENDING", amount, now); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE ledger_entitlements SET target=?,pending=?,available=?,debt=?,revision=revision+1 WHERE tenant_id=? AND app_id=? AND entitlement_id=?`, target, pending.String(), available.String(), debt.String(), s.Tenant, s.App, id)
	return err
}
func appendJournal(ctx context.Context, tx db.Executor, s foundation.Scope, id, ref, currency, debit, credit, amount string, now time.Time) error {
	if !validAmount(amount) || debit == credit || !foundation.ValidID(ref) {
		return foundation.ErrValidation
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO ledger_journals(tenant_id,app_id,journal_id,business_ref,idempotency_key,currency,debit_account,credit_account,amount,occurred_at,posted_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, s.Tenant, s.App, id, ref, id, currency, debit, credit, amount, now, now)
	return err
}

func (r Repository) Increase(ctx context.Context, tx db.Executor, s foundation.Scope, id, target, reference string, now time.Time) error {
	n, ok := new(big.Int).SetString(target, 10)
	if !ok || n.Sign() < 0 || n.String() != target || len(target) > 38 {
		return foundation.ErrValidation
	}
	e, err := r.Get(ctx, tx, s, id, true)
	if err != nil {
		return err
	}
	if e.Reserved != "0" {
		return errors.New("PAYMENT_RESULT_UNKNOWN")
	}
	old, _ := new(big.Int).SetString(e.Target, 10)
	delta := new(big.Int).Sub(n, old)
	if delta.Sign() < 0 {
		return errors.New("STATE_CONFLICT")
	}
	if delta.Sign() == 0 {
		return nil
	}
	debt, _ := new(big.Int).SetString(e.Debt, 10)
	clear := new(big.Int).Set(debt)
	if clear.Cmp(delta) > 0 {
		clear.Set(delta)
	}
	if clear.Sign() > 0 {
		if err = appendJournal(ctx, tx, s, stableID(id, reference, "debt-restore"), reference, e.Currency, "COMMISSION_EXPENSE", "AGENT_RECEIVABLE", clear.String(), now); err != nil {
			return err
		}
		debt.Sub(debt, clear)
		delta.Sub(delta, clear)
	}
	pending, _ := new(big.Int).SetString(e.Pending, 10)
	if delta.Sign() > 0 {
		pending.Add(pending, delta)
		if len(pending.String()) > 38 {
			return errors.New("AMOUNT_OUT_OF_RANGE")
		}
		if err = appendJournal(ctx, tx, s, stableID(id, reference, "increase"), reference, e.Currency, "COMMISSION_EXPENSE", "PAYABLE_PENDING", delta.String(), now); err != nil {
			return err
		}
	}

	available, _ := new(big.Int).SetString(e.Available, 10)
	if available.Sign() > 0 {
		pending.Add(pending, available)
		if err = appendJournal(ctx, tx, s, stableID(id, reference, "reapproval"), reference, e.Currency, "PAYABLE_AVAILABLE", "PAYABLE_PENDING", available.String(), now); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE ledger_entitlements SET target=?,pending=?,debt=?,available=0,revision=revision+1 WHERE tenant_id=? AND app_id=? AND entitlement_id=?`, target, pending.String(), debt.String(), s.Tenant, s.App, id)
	return err
}
