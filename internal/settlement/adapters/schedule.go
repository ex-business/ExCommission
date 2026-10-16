package adapters

import (
	"context"
	"encoding/json"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	model "github.com/ex-business/ExCommission/internal/settlement/domain"
	"time"
)

func (Repository) Schedule(ctx context.Context, tx db.Executor, s foundation.Scope) (model.Schedule, error) {
	var v model.Schedule
	var body []byte
	err := tx.QueryRowContext(ctx, `SELECT next_period,last_result FROM settlement_schedule WHERE tenant_id=? AND app_id=?`, s.Tenant, s.App).Scan(&v.NextPeriod, &body)
	if err != nil {
		return v, err
	}
	if len(body) > 0 {
		err = json.Unmarshal(body, &v.Last)
	}
	return v, err
}
func (r Repository) LockSchedule(ctx context.Context, tx db.Executor, s foundation.Scope, initial string, at time.Time) (model.Schedule, error) {
	if _, err := tx.ExecContext(ctx, `INSERT INTO settlement_schedule VALUES(?,?,?,NULL,?) ON DUPLICATE KEY UPDATE next_period=next_period`, s.Tenant, s.App, initial, at); err != nil {
		return model.Schedule{}, err
	}
	var period string
	if err := tx.QueryRowContext(ctx, `SELECT next_period FROM settlement_schedule WHERE tenant_id=? AND app_id=? FOR UPDATE`, s.Tenant, s.App).Scan(&period); err != nil {
		return model.Schedule{}, err
	}
	return r.Schedule(ctx, tx, s)
}
func (Repository) Advance(ctx context.Context, tx db.Executor, s foundation.Scope, next string, result model.ScheduledResult, at time.Time) error {
	body, err := json.Marshal(result)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE settlement_schedule SET next_period=?,last_result=?,updated_at=? WHERE tenant_id=? AND app_id=?`, next, body, at, s.Tenant, s.App)
	return err
}
