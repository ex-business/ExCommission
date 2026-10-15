package adapters

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	model "github.com/ex-business/ExCommission/internal/calculation/domain"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"time"
)

type Refunds struct{}

func (Refunds) Lock(ctx context.Context, tx db.Executor, s foundation.Scope, source, business, kind string) error {
	if _, err := tx.ExecContext(ctx, `INSERT IGNORE INTO calculation_refund_spaces VALUES(?,?,?,?,?)`, s.Tenant, s.App, source, business, kind); err != nil {
		return err
	}
	var id string
	return tx.QueryRowContext(ctx, `SELECT business_id FROM calculation_refund_spaces WHERE tenant_id=? AND app_id=? AND source_id=? AND business_id=? AND event_type=? FOR UPDATE`, s.Tenant, s.App, source, business, kind).Scan(&id)
}
func (r Refunds) Accept(ctx context.Context, tx db.Executor, s foundation.Scope, v model.RefundRecord, canonical []byte, now time.Time) (model.RefundRecord, error) {
	var existing string
	var old []byte
	err := tx.QueryRowContext(ctx, `SELECT refund_task_id,canonical_body FROM calculation_refunds WHERE tenant_id=? AND app_id=? AND source_id=? AND refund_id=? FOR UPDATE`, s.Tenant, s.App, v.Input.Source, v.Input.ID).Scan(&existing, &old)
	if err == nil {
		if string(old) != string(canonical) {
			return v, foundation.ErrConflict
		}
		return r.Get(ctx, tx, s, existing)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return v, err
	}
	body, _ := json.Marshal(v.Input)
	_, err = tx.ExecContext(ctx, `INSERT INTO calculation_refunds VALUES(?,?,?,?,?,?,?,?,?,'WAITING_ORIGINAL',NULL,?,?)`, s.Tenant, s.App, v.Input.Source, v.Input.ID, v.Input.BusinessID, v.Input.EventType, v.ID, canonical, body, now, now)
	if err == nil {
		_, err = tx.ExecContext(ctx, `INSERT INTO calculation_refund_order(tenant_id,app_id,refund_task_id) VALUES(?,?,?)`, s.Tenant, s.App, v.ID)
	}
	v.Status = "WAITING_ORIGINAL"
	return v, err
}
func (Refunds) Get(ctx context.Context, tx db.Executor, s foundation.Scope, id string) (model.RefundRecord, error) {
	var r model.RefundRecord
	var body []byte
	err := tx.QueryRowContext(ctx, `SELECT refund_task_id,payload,status,reason_code,created_at FROM calculation_refunds WHERE tenant_id=? AND app_id=? AND refund_task_id=?`, s.Tenant, s.App, id).Scan(&r.ID, &body, &r.Status, &r.Reason, &r.Created)
	if errors.Is(err, sql.ErrNoRows) {
		return r, errors.New("NOT_FOUND")
	}
	if err != nil {
		return r, err
	}
	err = json.Unmarshal(body, &r.Input)
	return r, err
}
func (Refunds) Records(ctx context.Context, tx db.Executor, s foundation.Scope, source, business, kind string) ([]model.RefundRecord, error) {
	rows, err := tx.QueryContext(ctx, `SELECT r.refund_task_id,r.payload,r.status,r.reason_code,r.created_at FROM calculation_refunds r JOIN calculation_refund_order o ON o.tenant_id=r.tenant_id AND o.app_id=r.app_id AND o.refund_task_id=r.refund_task_id WHERE r.tenant_id=? AND r.app_id=? AND r.source_id=? AND r.business_id=? AND r.event_type=? ORDER BY o.receipt_sequence`, s.Tenant, s.App, source, business, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.RefundRecord{}
	for rows.Next() {
		var r model.RefundRecord
		var body []byte
		if err = rows.Scan(&r.ID, &body, &r.Status, &r.Reason, &r.Created); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(body, &r.Input); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func (Refunds) State(ctx context.Context, tx db.Executor, s foundation.Scope, id, status string, reason *string, now time.Time) error {
	_, err := tx.ExecContext(ctx, `UPDATE calculation_refunds SET status=?,reason_code=?,updated_at=? WHERE tenant_id=? AND app_id=? AND refund_task_id=?`, status, reason, now, s.Tenant, s.App, id)
	return err
}
func (r Refunds) Pending(ctx context.Context, tx db.Executor, s foundation.Scope) ([]model.RefundRecord, error) {
	rows, err := tx.QueryContext(ctx, `SELECT refund_task_id FROM calculation_refunds WHERE tenant_id=? AND app_id=? AND status NOT IN ('SUCCEEDED','STALE_NOOP','REJECTED') ORDER BY updated_at,refund_task_id LIMIT 20`, s.Tenant, s.App)
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
	out := []model.RefundRecord{}
	for _, id := range ids {
		r, err := r.Get(ctx, tx, s, id)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}
