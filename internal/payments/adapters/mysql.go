package adapters

import (
	"context"
	"database/sql"
	"errors"
	model "github.com/ex-business/ExCommission/internal/payments/domain"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"time"
)

type Repository struct{}

func (Repository) Create(ctx context.Context, tx db.Executor, s foundation.Scope, p model.Payment, now time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO payment_orders(tenant_id,app_id,payment_id,statement_id,merchant_payment_id,channel_id,currency,amount,status,reconciliation_status,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,'CREATED','PENDING',?,?)`, s.Tenant, s.App, p.ID, p.StatementID, p.MerchantID, p.ChannelID, p.Currency, p.Amount, now, now)
	if err != nil {
		return err
	}
	for _, a := range p.Allocations {
		if _, err = tx.ExecContext(ctx, `INSERT INTO payment_allocations VALUES(?,?,?,?,?)`, s.Tenant, s.App, p.ID, a.EntitlementID, a.Amount); err != nil {
			return err
		}
	}
	return nil
}
func (Repository) Get(ctx context.Context, tx db.Executor, s foundation.Scope, id string, lock bool) (model.Payment, error) {
	var p model.Payment
	q := `SELECT payment_id,statement_id,merchant_payment_id,channel_id,currency,CAST(amount AS CHAR),status,reconciliation_status,receipt_ref,revision,created_at FROM payment_orders WHERE tenant_id=? AND app_id=? AND payment_id=?`
	if lock {
		q += " FOR UPDATE"
	}
	err := tx.QueryRowContext(ctx, q, s.Tenant, s.App, id).Scan(&p.ID, &p.StatementID, &p.MerchantID, &p.ChannelID, &p.Currency, &p.Amount, &p.Status, &p.Reconciliation, &p.Receipt, &p.Revision, &p.Created)
	if errors.Is(err, sql.ErrNoRows) {
		return p, errors.New("NOT_FOUND")
	}
	if err != nil {
		return p, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT entitlement_id,CAST(amount AS CHAR) FROM payment_allocations WHERE tenant_id=? AND app_id=? AND payment_id=? ORDER BY entitlement_id`, s.Tenant, s.App, id)
	if err != nil {
		return p, err
	}
	defer rows.Close()
	p.Allocations = []model.Allocation{}
	for rows.Next() {
		var a model.Allocation
		if err = rows.Scan(&a.EntitlementID, &a.Amount); err != nil {
			return p, err
		}
		p.Allocations = append(p.Allocations, a)
	}
	return p, rows.Err()
}
func (r Repository) ByMerchant(ctx context.Context, tx db.Executor, s foundation.Scope, id string) (model.Payment, error) {
	var pid string
	err := tx.QueryRowContext(ctx, `SELECT payment_id FROM payment_orders WHERE tenant_id=? AND app_id=? AND merchant_payment_id=?`, s.Tenant, s.App, id).Scan(&pid)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Payment{}, errors.New("NOT_FOUND")
	}
	if err != nil {
		return model.Payment{}, err
	}
	return r.Get(ctx, tx, s, pid, false)
}
func (Repository) State(ctx context.Context, tx db.Executor, s foundation.Scope, p model.Payment, status, reconciliation string, receipt *string, now time.Time) error {
	res, err := tx.ExecContext(ctx, `UPDATE payment_orders SET status=?,reconciliation_status=?,receipt_ref=COALESCE(?,receipt_ref),revision=revision+1,updated_at=? WHERE tenant_id=? AND app_id=? AND payment_id=? AND revision=?`, status, reconciliation, receipt, now, s.Tenant, s.App, p.ID, p.Revision)
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
	return nil
}
func (Repository) Callback(ctx context.Context, tx db.Executor, s foundation.Scope, channel, id, hash, payment string, now time.Time) (bool, error) {
	res, err := tx.ExecContext(ctx, `INSERT IGNORE INTO payment_callbacks VALUES(?,?,?,?,?,?,?)`, s.Tenant, s.App, channel, id, hash, payment, now)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if n > 0 {
		return true, nil
	}
	var old string
	err = tx.QueryRowContext(ctx, `SELECT body_hash FROM payment_callbacks WHERE tenant_id=? AND app_id=? AND channel_id=? AND callback_id=? FOR UPDATE`, s.Tenant, s.App, channel, id).Scan(&old)
	if err != nil {
		return false, err
	}
	if old != hash {
		return false, foundation.ErrConflict
	}
	return false, nil
}

func (r Repository) Pending(ctx context.Context, tx db.Executor, s foundation.Scope) ([]model.Payment, error) {
	rows, err := tx.QueryContext(ctx, `SELECT payment_id FROM payment_orders WHERE tenant_id=? AND app_id=? AND status IN ('CREATED','SUBMITTED','UNKNOWN') ORDER BY updated_at,payment_id LIMIT 20`, s.Tenant, s.App)
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
	out := []model.Payment{}
	for _, id := range ids {
		p, err := r.Get(ctx, tx, s, id, false)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}
