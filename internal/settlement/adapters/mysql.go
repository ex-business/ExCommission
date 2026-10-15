package adapters

import (
	"context"
	"database/sql"
	"errors"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	model "github.com/ex-business/ExCommission/internal/settlement/domain"
	"time"
)

type Repository struct{}

func (Repository) Lock(ctx context.Context, tx db.Executor, s foundation.Scope) error {
	if _, err := tx.ExecContext(ctx, `INSERT IGNORE INTO settlement_spaces VALUES(?,?)`, s.Tenant, s.App); err != nil {
		return err
	}
	var tenant string
	return tx.QueryRowContext(ctx, `SELECT tenant_id FROM settlement_spaces WHERE tenant_id=? AND app_id=? FOR UPDATE`, s.Tenant, s.App).Scan(&tenant)
}
func (r Repository) ExistingRun(ctx context.Context, tx db.Executor, s foundation.Scope, period, filterHash string) ([]model.Statement, bool, error) {
	var id, savedFilter string
	err := tx.QueryRowContext(ctx, `SELECT run_id,filter_hash FROM settlement_runs WHERE tenant_id=? AND app_id=? AND period=?`, s.Tenant, s.App, period).Scan(&id, &savedFilter)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if savedFilter != filterHash {
		return nil, false, foundation.ErrConflict
	}
	rows, err := tx.QueryContext(ctx, `SELECT statement_id FROM settlement_statements WHERE tenant_id=? AND app_id=? AND period=? ORDER BY statement_id`, s.Tenant, s.App, period)
	if err != nil {
		return nil, false, err
	}
	ids := []string{}
	for rows.Next() {
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, false, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, false, err
	}
	out := []model.Statement{}
	for _, id := range ids {
		v, err := r.Get(ctx, tx, s, id, false)
		if err != nil {
			return nil, false, err
		}
		out = append(out, v)
	}
	return out, true, nil
}
func (Repository) CreateRun(ctx context.Context, tx db.Executor, s foundation.Scope, id, period, calendar, filterHash string, now time.Time) error {
	var latest sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT MAX(period) FROM settlement_runs WHERE tenant_id=? AND app_id=?`, s.Tenant, s.App).Scan(&latest); err != nil {
		return err
	}
	if latest.Valid && period <= latest.String {
		return errors.New("STATE_CONFLICT")
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO settlement_runs VALUES(?,?,?,?,?,?,?)`, s.Tenant, s.App, period, id, calendar, filterHash, now)
	return err
}
func (Repository) Get(ctx context.Context, tx db.Executor, s foundation.Scope, id string, lock bool) (model.Statement, error) {
	var v model.Statement
	q := `SELECT statement_id,beneficiary_id,currency,period,status,revision,content_hash,CAST(total AS CHAR) FROM settlement_statements WHERE tenant_id=? AND app_id=? AND statement_id=?`
	if lock {
		q += " FOR UPDATE"
	}
	err := tx.QueryRowContext(ctx, q, s.Tenant, s.App, id).Scan(&v.ID, &v.AgentID, &v.Currency, &v.Period, &v.Status, &v.Revision, &v.Hash, &v.Total)
	if errors.Is(err, sql.ErrNoRows) {
		return v, errors.New("NOT_FOUND")
	}
	if err != nil {
		return v, err
	}
	if err = tx.QueryRowContext(ctx, `SELECT CAST(minimum AS CHAR),CAST(maximum AS CHAR) FROM settlement_payout_policies WHERE tenant_id=? AND app_id=? AND statement_id=?`, s.Tenant, s.App, id).Scan(&v.Minimum, &v.Maximum); err != nil {
		return v, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT i.entitlement_id,CAST(i.amount AS CHAR),i.entitlement_revision,CAST(t.target AS CHAR),CAST(t.paid_before AS CHAR) FROM settlement_items i LEFT JOIN settlement_item_targets t ON t.tenant_id=i.tenant_id AND t.app_id=i.app_id AND t.statement_id=i.statement_id AND t.entitlement_id=i.entitlement_id WHERE i.tenant_id=? AND i.app_id=? AND i.statement_id=? ORDER BY i.entitlement_id`, s.Tenant, s.App, id)
	if err != nil {
		return v, err
	}
	defer rows.Close()
	v.Items = []model.Item{}
	for rows.Next() {
		var item model.Item
		var target, paidBefore *string
		if err = rows.Scan(&item.ID, &item.Amount, &item.Revision, &target, &paidBefore); err != nil {
			return v, err
		}
		if target == nil || paidBefore == nil {
			return v, errors.New("UNKNOWN_FACT")
		}
		item.Target = *target
		item.PaidBefore = *paidBefore
		v.Items = append(v.Items, item)
	}
	return v, rows.Err()
}
func (Repository) Create(ctx context.Context, tx db.Executor, s foundation.Scope, v model.Statement, now time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO settlement_statements VALUES(?,?,?,?,?,?,'DRAFT',0,?,?,?)`, s.Tenant, s.App, v.ID, v.AgentID, v.Currency, v.Period, v.Hash, v.Total, now)
	if err != nil {
		return err
	}
	if v.Minimum == "" {
		v.Minimum = "0"
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO settlement_payout_policies VALUES(?,?,?,?,?)`, s.Tenant, s.App, v.ID, v.Minimum, v.Maximum); err != nil {
		return err
	}
	for _, item := range v.Items {
		if item.PaidBefore == "" {
			item.PaidBefore = "0"
		}
		if item.Target == "" {
			item.Target = item.Amount
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO settlement_item_targets VALUES(?,?,?,?,?,?)`, s.Tenant, s.App, v.ID, item.ID, item.Target, item.PaidBefore); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO settlement_items VALUES(?,?,?,?,?,?)`, s.Tenant, s.App, v.ID, item.ID, item.Amount, item.Revision); err != nil {
			return err
		}
	}
	return nil
}
func (Repository) Claimed(ctx context.Context, tx db.Executor, s foundation.Scope, id string) (bool, error) {
	// New pending after an approved cycle is a new revision requiring a new statement.
	if _, err := tx.ExecContext(ctx, `DELETE c FROM settlement_claims c JOIN settlement_statements v ON v.tenant_id=c.tenant_id AND v.app_id=c.app_id AND v.statement_id=c.statement_id WHERE c.tenant_id=? AND c.app_id=? AND c.entitlement_id=? AND v.status IN ('APPROVED','PARTIALLY_PAID','PAID')`, s.Tenant, s.App, id); err != nil {
		return false, err
	}

	var n int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM settlement_claims WHERE tenant_id=? AND app_id=? AND entitlement_id=?`, s.Tenant, s.App, id).Scan(&n)
	return n > 0, err
}
func (Repository) Claim(ctx context.Context, tx db.Executor, s foundation.Scope, v model.Statement) error {
	for _, item := range v.Items {
		if _, err := tx.ExecContext(ctx, `INSERT INTO settlement_claims VALUES(?,?,?,?)`, s.Tenant, s.App, item.ID, v.ID); err != nil {
			return err
		}
	}
	return nil
}
func (Repository) State(ctx context.Context, tx db.Executor, s foundation.Scope, v model.Statement, status string) error {
	res, err := tx.ExecContext(ctx, `UPDATE settlement_statements SET status=?,revision=revision+1 WHERE tenant_id=? AND app_id=? AND statement_id=? AND revision=?`, status, s.Tenant, s.App, v.ID, v.Revision)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("REVISION_CONFLICT")
	}
	if status == "DRAFT" || status == "CANCELLED" {
		_, err = tx.ExecContext(ctx, `DELETE FROM settlement_claims WHERE tenant_id=? AND app_id=? AND statement_id=?`, s.Tenant, s.App, v.ID)
	}
	return err
}

// PaymentState is owned by settlement so payment adapters cannot write its tables.
func (r Repository) PaymentState(ctx context.Context, tx db.Executor, s foundation.Scope, v model.Statement, status string) error {
	return r.State(ctx, tx, s, v, status)
}

func (Repository) Owns(ctx context.Context, tx db.Executor, s foundation.Scope, statement, id string) (bool, error) {
	var n int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM settlement_claims WHERE tenant_id=? AND app_id=? AND statement_id=? AND entitlement_id=?`, s.Tenant, s.App, statement, id).Scan(&n)
	return n == 1, err
}

func (Repository) RunID(ctx context.Context, tx db.Executor, s foundation.Scope, period string) (string, error) {
	var id string
	err := tx.QueryRowContext(ctx, `SELECT run_id FROM settlement_runs WHERE tenant_id=? AND app_id=? AND period=?`, s.Tenant, s.App, period).Scan(&id)
	return id, err
}
