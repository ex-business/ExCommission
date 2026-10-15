package adapters

import (
	"context"
	"database/sql"
	"errors"
	"github.com/ex-business/ExCommission/internal/ledger/domain"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	riskmodel "github.com/ex-business/ExCommission/internal/risk/domain"
	"math/big"
	"time"
)

func (Repository) Get(ctx context.Context, tx db.Executor, scope foundation.Scope, id string, lock bool) (domain.Entitlement, error) {
	e := domain.Entitlement{}
	if !scope.Valid() || !foundation.ValidID(id) {
		return e, foundation.ErrValidation
	}
	q := `SELECT entitlement_id,rule_id,task_id,beneficiary_id,currency,CAST(target AS CHAR),CAST(pending AS CHAR),CAST(available AS CHAR),CAST(reserved AS CHAR),CAST(paid AS CHAR),CAST(debt AS CHAR),CAST(recovered AS CHAR),revision,business_time FROM ledger_entitlements WHERE tenant_id=? AND app_id=? AND entitlement_id=?`
	if lock {
		q += " FOR UPDATE"
	}
	err := tx.QueryRowContext(ctx, q, scope.Tenant, scope.App, id).Scan(&e.ID, &e.RuleID, &e.TaskID, &e.AgentID, &e.Currency, &e.Target, &e.Pending, &e.Available, &e.Reserved, &e.Paid, &e.Debt, &e.Recovered, &e.Revision, &e.BusinessTime)
	if errors.Is(err, sql.ErrNoRows) {
		return e, errors.New("NOT_FOUND")
	}
	if err != nil {
		return e, err
	}
	var n int
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM ledger_restrictions WHERE tenant_id=? AND app_id=? AND entitlement_id=? AND closed_at IS NULL`, scope.Tenant, scope.App, id).Scan(&n)
	e.Frozen = n > 0
	e.RestrictedPending = "0"
	e.RestrictedAvailable = "0"
	if e.Frozen {
		e.RestrictedPending = e.Pending
		e.RestrictedAvailable = e.Available
	}
	e.PaymentHold = e.Frozen && e.Reserved != "0"
	return e, err
}

// Transfer is the only bucket mutation path. Callers supply an approved business reference;
// the transaction and row lock are shared with the owning domain's state transition.
func (r Repository) Transfer(ctx context.Context, tx db.Executor, scope foundation.Scope, id, from, to, amount, reference string, now time.Time) error {
	pairs := map[string][2]string{"pending:available": {"PAYABLE_PENDING", "PAYABLE_AVAILABLE"}, "available:reserved": {"PAYABLE_AVAILABLE", "PAYABLE_RESERVED"}, "reserved:available": {"PAYABLE_RESERVED", "PAYABLE_AVAILABLE"}, "reserved:paid": {"PAYABLE_RESERVED", "CASH_CLEARING"}}
	accounts, ok := pairs[from+":"+to]
	if !ok || !validAmount(amount) || !foundation.ValidID(reference) {
		return foundation.ErrValidation
	}
	e, err := r.Get(ctx, tx, scope, id, true)
	if err != nil {
		return err
	}
	journal := stableID(id, reference)
	var previous, previousDebit, previousCredit string
	err = tx.QueryRowContext(ctx, `SELECT CAST(amount AS CHAR),debit_account,credit_account FROM ledger_journals WHERE tenant_id=? AND app_id=? AND journal_id=?`, scope.Tenant, scope.App, journal).Scan(&previous, &previousDebit, &previousCredit)
	if err == nil {
		if previous == amount && previousDebit == accounts[0] && previousCredit == accounts[1] {
			return nil
		}
		return foundation.ErrConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if e.Frozen && (from == "pending" || from == "available") {
		return errors.New("FUNDS_FROZEN")
	}
	values := map[string]string{"pending": e.Pending, "available": e.Available, "reserved": e.Reserved, "paid": e.Paid}
	source, _ := new(big.Int).SetString(values[from], 10)
	dest, _ := new(big.Int).SetString(values[to], 10)
	n, _ := new(big.Int).SetString(amount, 10)
	if source.Cmp(n) < 0 {
		return errors.New("INSUFFICIENT_BALANCE")
	}
	source.Sub(source, n)
	dest.Add(dest, n)
	if len(dest.String()) > 38 {
		return errors.New("AMOUNT_OUT_OF_RANGE")
	}
	_, err = tx.ExecContext(ctx, `UPDATE ledger_entitlements SET `+from+`=?,`+to+`=?,revision=revision+1 WHERE tenant_id=? AND app_id=? AND entitlement_id=?`, source.String(), dest.String(), scope.Tenant, scope.App, id)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO ledger_journals(tenant_id,app_id,journal_id,business_ref,idempotency_key,currency,debit_account,credit_account,amount,occurred_at,posted_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, scope.Tenant, scope.App, journal, reference, journal, e.Currency, accounts[0], accounts[1], amount, now, now)
	return err
}

// Hold locks the entitlement before adding a reason; restrictions never move money.
func (r Repository) Hold(ctx context.Context, tx db.Executor, s foundation.Scope, id, risk, reason string, now time.Time) error {
	if !foundation.ValidID(risk) || !foundation.ValidID(reason) {
		return foundation.ErrValidation
	}
	if _, err := r.Get(ctx, tx, s, id, true); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT IGNORE INTO ledger_restrictions(tenant_id,app_id,entitlement_id,risk_id,reason_code,created_at) VALUES(?,?,?,?,?,?)`, s.Tenant, s.App, id, risk, reason, now)
	return err
}

func (r Repository) CloseHold(ctx context.Context, tx db.Executor, s foundation.Scope, id, risk string, now time.Time) error {
	e, err := r.Get(ctx, tx, s, id, true)
	if err != nil {
		return err
	}
	if e.Reserved != "0" {
		return errors.New("PAYMENT_RESULT_UNKNOWN")
	}
	_, err = tx.ExecContext(ctx, `UPDATE ledger_restrictions SET closed_at=? WHERE tenant_id=? AND app_id=? AND entitlement_id=? AND risk_id=? AND closed_at IS NULL`, now, s.Tenant, s.App, id, risk)
	return err
}

func (r Repository) Requeue(ctx context.Context, tx db.Executor, s foundation.Scope, id, reference string, now time.Time) error {
	e, err := r.Get(ctx, tx, s, id, true)
	if err != nil {
		return err
	}
	if e.Available == "0" {
		return nil
	}
	pending, _ := new(big.Int).SetString(e.Pending, 10)
	a, _ := new(big.Int).SetString(e.Available, 10)
	pending.Add(pending, a)
	if len(pending.String()) > 38 {
		return errors.New("AMOUNT_OUT_OF_RANGE")
	}
	if err = appendJournal(ctx, tx, s, stableID(id, reference, "requeue"), reference, e.Currency, "PAYABLE_AVAILABLE", "PAYABLE_PENDING", e.Available, now); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE ledger_entitlements SET pending=?,available=0,revision=revision+1 WHERE tenant_id=? AND app_id=? AND entitlement_id=?`, pending.String(), s.Tenant, s.App, id)
	return err
}
func (r Repository) ForAgent(ctx context.Context, tx db.Executor, s foundation.Scope, agent string) ([]domain.Entitlement, error) {
	rows, err := tx.QueryContext(ctx, `SELECT entitlement_id FROM ledger_entitlements WHERE tenant_id=? AND app_id=? AND beneficiary_id=? AND (pending>0 OR available>0 OR reserved>0 OR debt>0) ORDER BY entitlement_id`, s.Tenant, s.App, agent)
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
	out := []domain.Entitlement{}
	for _, id := range ids {
		e, err := r.Get(ctx, tx, s, id, true)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

func (Repository) Restrictions(ctx context.Context, tx db.Executor, s foundation.Scope, risk string) ([]riskmodel.Restriction, error) {
	rows, err := tx.QueryContext(ctx, `SELECT entitlement_id,risk_id,reason_code,created_at,closed_at FROM ledger_restrictions WHERE tenant_id=? AND app_id=? AND risk_id=? ORDER BY entitlement_id`, s.Tenant, s.App, risk)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []riskmodel.Restriction{}
	for rows.Next() {
		var r riskmodel.Restriction
		if err = rows.Scan(&r.EntitlementID, &r.RiskID, &r.Reason, &r.Created, &r.Closed); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
