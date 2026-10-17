package adapters

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	model "github.com/ex-business/ExCommission/internal/affiliateflow/domain"
	agents "github.com/ex-business/ExCommission/internal/agents/adapters"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	registration "github.com/ex-business/ExCommission/internal/registration/adapters"
	receipt "github.com/ex-business/ExCommission/internal/registration/domain"
	"time"
)

// Members 只读取可信注册回执，既有手工同步用户不会被静默补进新业务或重建邀请关系。
type Members struct{}

func (Members) Resolve(ctx context.Context, tx db.Executor, s foundation.Scope, source, external string) (model.Member, error) {
	r, _, e := (registration.Repository{}).Receipt(ctx, tx, s, source, external)
	if errors.Is(e, sql.ErrNoRows) {
		return model.Member{}, errors.New("UNKNOWN_FACT")
	}
	if e != nil {
		return model.Member{}, e
	}
	m := model.Member{UserID: r.UserID, AgentID: r.AgentID, RegisteredAt: r.RegisteredAt}
	if r.ParentID != nil {
		e = tx.QueryRowContext(ctx, `SELECT user_id FROM agent_accounts WHERE tenant_id=? AND app_id=? AND agent_id=?`, s.Tenant, s.App, *r.ParentID).Scan(&m.ParentUser)
	}
	return m, e
}
func (Members) List(ctx context.Context, tx db.Executor, s foundation.Scope, at time.Time) ([]model.Member, error) {
	rows, e := tx.QueryContext(ctx, `SELECT r.receipt,p.user_id FROM registration_receipts r LEFT JOIN agent_accounts p ON p.tenant_id=r.tenant_id AND p.app_id=r.app_id AND p.agent_id=JSON_UNQUOTE(JSON_EXTRACT(r.receipt,'$.parent_agent_id')) WHERE r.tenant_id=? AND r.app_id=? ORDER BY r.source_id,r.external_user_id`, s.Tenant, s.App)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []model.Member{}
	for rows.Next() {
		var b []byte
		var p sql.NullString
		if e = rows.Scan(&b, &p); e != nil {
			return nil, e
		}
		var r receipt.Receipt
		if e = json.Unmarshal(b, &r); e != nil {
			return nil, e
		}
		if r.RegisteredAt.After(at) {
			continue
		}
		m := model.Member{UserID: r.UserID, AgentID: r.AgentID, RegisteredAt: r.RegisteredAt}
		if p.Valid {
			m.ParentUser = p.String
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
func (m Members) Own(ctx context.Context, tx db.Executor, s foundation.Scope, user string) (model.Member, error) {
	var source, external string
	e := tx.QueryRowContext(ctx, `SELECT source_id,external_user_id FROM users_external WHERE tenant_id=? AND app_id=? AND user_id=?`, s.Tenant, s.App, user).Scan(&source, &external)
	if errors.Is(e, sql.ErrNoRows) {
		return model.Member{}, errors.New("NOT_FOUND")
	}
	if e != nil {
		return model.Member{}, e
	}
	return m.Resolve(ctx, tx, s, source, external)
}
func (Members) Allowed(ctx context.Context, tx db.Executor, s foundation.Scope, agent string, at time.Time) error {
	return (agents.Repository{}).Active(ctx, tx, s, agent, at)
}
