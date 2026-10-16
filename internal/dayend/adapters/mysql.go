package adapters

import (
	"context"
	"encoding/json"
	model "github.com/ex-business/ExCommission/internal/dayend/domain"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
)

type Repository struct{}

// Lock 串行同一方案业务日的冻结；事务失败占位也回滚，重试不会产生半份快照。
func (Repository) Lock(ctx context.Context, tx db.Executor, s foundation.Scope, plan, day string) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO dayend_snapshots(tenant_id,app_id,plan_id,business_day) VALUES(?,?,?,?) ON DUPLICATE KEY UPDATE plan_id=plan_id`, s.Tenant, s.App, plan, day); err != nil {
		return err
	}
	var id string
	return tx.QueryRowContext(ctx, `SELECT plan_id FROM dayend_snapshots WHERE tenant_id=? AND app_id=? AND plan_id=? AND business_day=? FOR UPDATE`, s.Tenant, s.App, plan, day).Scan(&id)
}
func (Repository) Get(ctx context.Context, tx db.Executor, s foundation.Scope, plan, day string) (model.Snapshot, error) {
	var raw []byte
	var result model.Snapshot
	err := tx.QueryRowContext(ctx, `SELECT snapshot_json FROM dayend_snapshots WHERE tenant_id=? AND app_id=? AND plan_id=? AND business_day=? AND snapshot_json IS NOT NULL FOR UPDATE`, s.Tenant, s.App, plan, day).Scan(&raw)
	if err != nil {
		return result, err
	}
	err = json.Unmarshal(raw, &result)
	return result, err
}
func (Repository) Save(ctx context.Context, tx db.Executor, s foundation.Scope, v model.Snapshot) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE dayend_snapshots SET snapshot_json=? WHERE tenant_id=? AND app_id=? AND plan_id=? AND business_day=? AND snapshot_json IS NULL`, raw, s.Tenant, s.App, v.PlanID, v.Day)
	return err
}
