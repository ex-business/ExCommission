package adapters

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	model "github.com/ex-business/ExCommission/internal/executionrouting/domain"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"time"
)

type State struct {
	Revision uint64       `json:"revision"`
	Config   model.Config `json:"config"`
}

// Head 的锁定读取同时串行化本站配置发布和新业务归属，避免同一切流点读到不同配置。
func (Repository) Head(ctx context.Context, tx db.Executor, sc foundation.Scope, lock bool) (State, error) {
	out := State{Config: model.Config{Rules: []model.Rule{}}}
	if lock {
		if _, e := tx.ExecContext(ctx, `INSERT IGNORE INTO execution_route_heads VALUES(?,?,0,?)`, sc.Tenant, sc.App, `{"rules":[]}`); e != nil {
			return out, e
		}
	}
	query := `SELECT revision,config FROM execution_route_heads WHERE tenant_id=? AND app_id=?`
	if lock {
		query += " FOR UPDATE"
	}
	var raw []byte
	e := tx.QueryRowContext(ctx, query, sc.Tenant, sc.App).Scan(&out.Revision, &raw)
	if errors.Is(e, sql.ErrNoRows) {
		return out, nil
	}
	if e != nil {
		return out, e
	}
	e = json.Unmarshal(raw, &out.Config)
	return out, e
}

// Apply 仅在原独立审批事务内执行，禁止覆盖历史时间表；审批错过新规则生效时间须重新申请。
func (r Repository) Apply(ctx context.Context, tx db.Executor, sc foundation.Scope, expected uint64, config model.Config, approval string, at time.Time) error {
	old, e := r.Head(ctx, tx, sc, true)
	if e != nil {
		return e
	}
	if old.Revision != expected {
		return errors.New("REVISION_CONFLICT")
	}
	if e = model.ValidateChange(old.Config, config, at); e != nil {
		return e
	}
	raw, e := json.Marshal(config)
	if e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO execution_route_versions VALUES(?,?,?,?,?,?)`, sc.Tenant, sc.App, expected+1, raw, approval, at.UTC()); e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `UPDATE execution_route_heads SET revision=?,config=? WHERE tenant_id=? AND app_id=?`, expected+1, raw, sc.Tenant, sc.App)
	return e
}

// Records 只读本站最近受理的归属与路由版本，排除正文及会员身份资料。
func (Repository) Records(ctx context.Context, q db.Executor, sc foundation.Scope) ([]map[string]any, error) {
	rows, e := q.QueryContext(ctx, `SELECT DISTINCT o.source_id,o.event_id,o.business_id,o.event_kind,o.engine,r.route_id,r.config_revision,r.business_time,o.created_at FROM execution_ownership o JOIN execution_route_receipts r ON r.tenant_id=o.tenant_id AND r.app_id=o.app_id AND r.identity_hash=o.identity_hash WHERE o.tenant_id=? AND o.app_id=? ORDER BY o.created_at DESC,o.event_id LIMIT 100`, sc.Tenant, sc.App)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var source, event, business, kind, engine, route string
		var revision uint64
		var at, created time.Time
		if e = rows.Scan(&source, &event, &business, &kind, &engine, &route, &revision, &at, &created); e != nil {
			return nil, e
		}
		out = append(out, map[string]any{"source_id": source, "event_id": event, "business_id": business, "event_kind": kind, "engine": engine, "route_id": route, "config_revision": revision, "business_time": at, "received_at": created})
	}
	return out, rows.Err()
}
