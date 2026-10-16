package adapters

import (
	"context"
	"database/sql"
	"errors"
	model "github.com/ex-business/ExCommission/internal/members/domain"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"time"
)

type Repository struct{}

func (Repository) OwnAgent(ctx context.Context, tx db.Executor, s foundation.Scope, user string, lock bool) (string, error) {
	q := `SELECT agent_id FROM agent_accounts WHERE tenant_id=? AND app_id=? AND user_id=?`
	if lock {
		q += " FOR UPDATE"
	}
	var id string
	err := tx.QueryRowContext(ctx, q, s.Tenant, s.App, user).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", errors.New("NOT_FOUND")
	}
	return id, err
}
func (Repository) Link(ctx context.Context, tx db.Executor, s foundation.Scope, agent string) (model.Link, error) {
	var v model.Link
	err := tx.QueryRowContext(ctx, `SELECT code FROM member_referral_links WHERE tenant_id=? AND app_id=? AND agent_id=?`, s.Tenant, s.App, agent).Scan(&v.Code)
	if errors.Is(err, sql.ErrNoRows) {
		return v, errors.New("NOT_FOUND")
	}
	return v, err
}
func (r Repository) Create(ctx context.Context, tx db.Executor, s foundation.Scope, agent, code string, at time.Time) (model.Link, error) {
	if _, err := tx.ExecContext(ctx, `INSERT IGNORE INTO member_referral_links VALUES(?,?,?,?,?)`, s.Tenant, s.App, agent, code, at); err != nil {
		return model.Link{}, err
	}
	return r.Link(ctx, tx, s, agent)
}
func (Repository) Resolve(ctx context.Context, tx db.Executor, s foundation.Scope, code string) (string, error) {
	var agent string
	err := tx.QueryRowContext(ctx, `SELECT agent_id FROM member_referral_links WHERE tenant_id=? AND app_id=? AND code=?`, s.Tenant, s.App, code).Scan(&agent)
	if errors.Is(err, sql.ErrNoRows) {
		return "", errors.New("NOT_FOUND")
	}
	return agent, err
}
func (Repository) Earnings(ctx context.Context, tx db.Executor, s foundation.Scope, agent string) ([]model.Earnings, error) {
	rows, err := tx.QueryContext(ctx, `SELECT currency,CAST(SUM(target) AS CHAR),CAST(SUM(pending) AS CHAR),CAST(SUM(CASE WHEN NOT EXISTS(SELECT 1 FROM ledger_restrictions r WHERE r.tenant_id=e.tenant_id AND r.app_id=e.app_id AND r.entitlement_id=e.entitlement_id AND r.closed_at IS NULL) THEN available ELSE 0 END) AS CHAR),CAST(SUM(CASE WHEN EXISTS(SELECT 1 FROM ledger_restrictions r WHERE r.tenant_id=e.tenant_id AND r.app_id=e.app_id AND r.entitlement_id=e.entitlement_id AND r.closed_at IS NULL) THEN available ELSE 0 END) AS CHAR),CAST(SUM(reserved) AS CHAR),CAST(SUM(paid) AS CHAR),CAST(SUM(recovered) AS CHAR),CAST(SUM(debt) AS CHAR) FROM ledger_entitlements e WHERE tenant_id=? AND app_id=? AND beneficiary_id=? GROUP BY currency ORDER BY currency`, s.Tenant, s.App, agent)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Earnings{}
	for rows.Next() {
		var v model.Earnings
		if err = rows.Scan(&v.Currency, &v.Target, &v.Pending, &v.Available, &v.Frozen, &v.Reserved, &v.Paid, &v.Recovered, &v.Debt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
