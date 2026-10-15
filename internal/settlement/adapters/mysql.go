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
	rows, err := tx.QueryContext(ctx, `SELECT entitlement_id,CAST(amount AS CHAR),entitlement_revision FROM settlement_items WHERE tenant_id=? AND app_id=? AND statement_id=? ORDER BY entitlement_id`, s.Tenant, s.App, id)
	if err != nil {
		return v, err
	}
	defer rows.Close()
	v.Items = []model.Item{}
	for rows.Next() {
		var item model.Item
		if err = rows.Scan(&item.ID, &item.Amount, &item.Revision); err != nil {
			return v, err
		}
		v.Items = append(v.Items, item)
	}
	return v, rows.Err()
}
func (Repository) Create(ctx context.Context, tx db.Executor, s foundation.Scope, v model.Statement, now time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO settlement_statements VALUES(?,?,?,?,?,?,'DRAFT',0,?,?,?)`, s.Tenant, s.App, v.ID, v.AgentID, v.Currency, v.Period, v.Hash, v.Total, now)
	if err != nil {
		return err
	}
	for _, item := range v.Items {
		if _, err = tx.ExecContext(ctx, `INSERT INTO settlement_items VALUES(?,?,?,?,?,?)`, s.Tenant, s.App, v.ID, item.ID, item.Amount, item.Revision); err != nil {
			return err
		}
	}
	return nil
}
func (Repository) Claimed(ctx context.Context, tx db.Executor, s foundation.Scope, id string) (bool, error) {
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
