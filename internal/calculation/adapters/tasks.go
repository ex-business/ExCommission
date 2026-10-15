package adapters

import (
	"context"
	"database/sql"
	"errors"
	"github.com/ex-business/ExCommission/internal/calculation/domain"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"time"
)

type Tasks struct {
	DB     *sql.DB
	Within func(context.Context, func(db.Executor) error) error
}

func (Tasks) Create(ctx context.Context, tx db.Executor, s foundation.Scope, t domain.Task, at time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO calculation_tasks(tenant_id,app_id,task_id,event_resource_id,source_id,source_event_id,business_id,event_type,business_time,event_body,status,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,'QUEUED',?,?)`, s.Tenant, s.App, t.ID, t.EventID, t.Source, t.SourceEvent, t.BusinessID, t.EventType, t.BusinessTime.UTC(), t.EventBody, at.UTC(), at.UTC())
	return err
}

const taskColumns = `task_id,event_resource_id,source_id,source_event_id,business_id,event_type,business_time,event_body,status,reason_code,snapshot,snapshot_hash,result,revision,fencing_token,lease_until,retry_count`

type scanner interface{ Scan(...any) error }

func scanTask(row scanner) (domain.Task, error) {
	var t domain.Task
	err := row.Scan(&t.ID, &t.EventID, &t.Source, &t.SourceEvent, &t.BusinessID, &t.EventType, &t.BusinessTime, &t.EventBody, &t.Status, &t.Reason, &t.Snapshot, &t.SnapshotHash, &t.Result, &t.Revision, &t.Fencing, &t.Lease, &t.Retries)
	if errors.Is(err, sql.ErrNoRows) {
		return t, errors.New("NOT_FOUND")
	}
	return t, err
}
func (Tasks) Get(ctx context.Context, tx db.Executor, s foundation.Scope, id string) (domain.Task, error) {
	return scanTask(tx.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM calculation_tasks WHERE tenant_id=? AND app_id=? AND task_id=?`, s.Tenant, s.App, id))
}
func (Tasks) ByEvent(ctx context.Context, tx db.Executor, s foundation.Scope, id string) (domain.Task, error) {
	return scanTask(tx.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM calculation_tasks WHERE tenant_id=? AND app_id=? AND event_resource_id=?`, s.Tenant, s.App, id))
}
func (Tasks) BySource(ctx context.Context, tx db.Executor, s foundation.Scope, source, event string) (domain.Task, error) {
	return scanTask(tx.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM calculation_tasks WHERE tenant_id=? AND app_id=? AND source_id=? AND source_event_id=?`, s.Tenant, s.App, source, event))
}
func (r Tasks) Claim(ctx context.Context, s foundation.Scope, at time.Time, lease time.Duration) (domain.Task, bool, error) {
	var task domain.Task
	found := false
	err := r.Within(ctx, func(tx db.Executor) error {
		row := tx.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM calculation_tasks WHERE tenant_id=? AND app_id=? AND (status='QUEUED' OR (status='RETRY_WAIT' AND next_attempt_at<=?) OR (status='RUNNING' AND lease_until<=?)) ORDER BY created_at,task_id LIMIT 1 FOR UPDATE SKIP LOCKED`, s.Tenant, s.App, at.UTC(), at.UTC())
		var err error
		task, err = scanTask(row)
		if err != nil && err.Error() == "NOT_FOUND" {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		until := at.Add(lease).UTC()
		if _, err = tx.ExecContext(ctx, `UPDATE calculation_tasks SET status='RUNNING',fencing_token=fencing_token+1,revision=revision+1,lease_until=?,updated_at=? WHERE tenant_id=? AND app_id=? AND task_id=?`, until, at.UTC(), s.Tenant, s.App, task.ID); err != nil {
			return err
		}
		task.Fencing++
		task.Revision++
		task.Status = "RUNNING"
		task.Lease = &until
		return nil
	})
	return task, found, err
}
func (Tasks) SaveSnapshot(ctx context.Context, tx db.Executor, s foundation.Scope, t domain.Task, body []byte, hash string, at time.Time) error {
	result, err := tx.ExecContext(ctx, `UPDATE calculation_tasks SET snapshot=?,snapshot_hash=? WHERE tenant_id=? AND app_id=? AND task_id=? AND fencing_token=? AND status='RUNNING' AND lease_until>? AND snapshot IS NULL`, body, hash, s.Tenant, s.App, t.ID, t.Fencing, at.UTC())
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return errors.New("REVISION_CONFLICT")
	}
	return nil
}
func (Tasks) Finish(ctx context.Context, tx db.Executor, s foundation.Scope, t domain.Task, status string, reason *string, body []byte, at time.Time) error {
	result, err := tx.ExecContext(ctx, `UPDATE calculation_tasks SET status=?,reason_code=?,result=?,revision=revision+1,lease_until=NULL,updated_at=? WHERE tenant_id=? AND app_id=? AND task_id=? AND fencing_token=? AND status='RUNNING' AND lease_until>?`, status, reason, body, at.UTC(), s.Tenant, s.App, t.ID, t.Fencing, at.UTC())
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return errors.New("REVISION_CONFLICT")
	}
	return nil
}
func (r Tasks) Retry(ctx context.Context, s foundation.Scope, t domain.Task, at time.Time) error {
	delays := []time.Duration{time.Second, 5 * time.Second, 30 * time.Second, 120 * time.Second, 600 * time.Second}
	return r.Within(ctx, func(tx db.Executor) error {
		if t.Retries >= len(delays) {
			reason := "DEPENDENCY_UNAVAILABLE"
			return r.Finish(ctx, tx, s, t, "DEAD", &reason, nil, at)
		}
		result, err := tx.ExecContext(ctx, `UPDATE calculation_tasks SET status='RETRY_WAIT',reason_code='DEPENDENCY_UNAVAILABLE',retry_count=retry_count+1,next_attempt_at=?,lease_until=NULL,revision=revision+1,updated_at=? WHERE tenant_id=? AND app_id=? AND task_id=? AND fencing_token=? AND status='RUNNING' AND lease_until>?`, at.Add(delays[t.Retries]).UTC(), at.UTC(), s.Tenant, s.App, t.ID, t.Fencing, at.UTC())
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count != 1 {
			return errors.New("REVISION_CONFLICT")
		}
		return nil
	})
}
func (Tasks) Resume(ctx context.Context, tx db.Executor, s foundation.Scope, id string, revision uint64, evidence string, at time.Time) error {
	if evidence == "" {
		return foundation.ErrValidation
	}
	result, err := tx.ExecContext(ctx, `UPDATE calculation_tasks SET status='QUEUED',reason_code=NULL,retry_count=0,revision=revision+1,updated_at=? WHERE tenant_id=? AND app_id=? AND task_id=? AND revision=? AND status IN ('SUSPENDED','DEAD')`, at.UTC(), s.Tenant, s.App, id, revision)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return errors.New("REVISION_CONFLICT")
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO calculation_recoveries SELECT tenant_id,app_id,task_id,revision,?,? FROM calculation_tasks WHERE tenant_id=? AND app_id=? AND task_id=?`, evidence, at.UTC(), s.Tenant, s.App, id)
	return err
}
