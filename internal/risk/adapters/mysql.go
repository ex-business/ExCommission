package adapters

import (
	"context"
	"database/sql"
	"errors"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	model "github.com/ex-business/ExCommission/internal/risk/domain"
	"time"
)

type Repository struct{}

func (Repository) Create(ctx context.Context, tx db.Executor, s foundation.Scope, v model.Hold, now time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO risk_requests VALUES(?,?,?,?,?,?,0,'OPEN',?)`, s.Tenant, s.App, v.ID, v.EntitlementID, v.Reason, v.Evidence, now)
	return err
}
func (Repository) Get(ctx context.Context, tx db.Executor, s foundation.Scope, id string, lock bool) (model.Hold, error) {
	var v model.Hold
	q := `SELECT risk_id,entitlement_id,reason_code,evidence_ref,revision,status FROM risk_requests WHERE tenant_id=? AND app_id=? AND risk_id=?`
	if lock {
		q += " FOR UPDATE"
	}
	err := tx.QueryRowContext(ctx, q, s.Tenant, s.App, id).Scan(&v.ID, &v.EntitlementID, &v.Reason, &v.Evidence, &v.Revision, &v.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return v, errors.New("NOT_FOUND")
	}
	return v, err
}
func (Repository) State(ctx context.Context, tx db.Executor, s foundation.Scope, v model.Hold, status string) error {
	res, err := tx.ExecContext(ctx, `UPDATE risk_requests SET status=?,revision=revision+1 WHERE tenant_id=? AND app_id=? AND risk_id=? AND revision=?`, status, s.Tenant, s.App, v.ID, v.Revision)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("STALE_APPROVAL")
	}
	return nil
}
func (Repository) Appeal(ctx context.Context, tx db.Executor, s foundation.Scope, id, risk, actor, reason, evidence string, now time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO risk_appeals VALUES(?,?,?,?,?,?,?,?)`, s.Tenant, s.App, id, risk, actor, reason, evidence, now)
	return err
}
