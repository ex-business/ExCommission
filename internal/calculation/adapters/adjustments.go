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

type Adjustments struct{}

func (Adjustments) Latest(ctx context.Context, tx db.Executor, s foundation.Scope, task model.Task) (model.Input, uint64, error) {
	var input model.Input
	var body []byte
	var revision uint64
	err := tx.QueryRowContext(ctx, `SELECT snapshot,revision FROM calculation_adjustments WHERE tenant_id=? AND app_id=? AND original_task_id=? ORDER BY revision DESC LIMIT 1`, s.Tenant, s.App, task.ID).Scan(&body, &revision)
	if errors.Is(err, sql.ErrNoRows) {
		body = task.Snapshot
		revision = task.Revision
	} else if err != nil {
		return input, 0, err
	}
	err = json.Unmarshal(body, &input)
	return input, revision, err
}
func (Adjustments) Save(ctx context.Context, tx db.Executor, s foundation.Scope, id, task string, revision uint64, input model.Input, result model.Result, evidence string, now time.Time) error {
	snapshot, err := json.Marshal(input)
	if err != nil {
		return err
	}
	body, err := json.Marshal(result)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO calculation_adjustments VALUES(?,?,?,?,?,?,?,?,?)`, s.Tenant, s.App, id, task, revision, snapshot, body, evidence, now)
	return err
}
