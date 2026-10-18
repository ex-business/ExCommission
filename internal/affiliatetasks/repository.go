package affiliatetasks

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"time"
)

type Repository struct{}

func (Repository) Ensure(ctx context.Context, tx db.Executor, sc foundation.Scope, k Key, at time.Time) error {
	r := Record{Key: k, ID: k.ID(), Report: Report{Status: Ready, Phase: "DISCOVERED", Missing: []Missing{}}, Created: at, Updated: at, Checked: at}
	raw, _ := json.Marshal(r)
	_, e := tx.ExecContext(ctx, `INSERT IGNORE INTO affiliate_task_runs VALUES(?,?,?,?,?,?,?,FALSE,'',NULL,?,?)`, sc.Tenant, sc.App, r.ID, k.Program, k.Revision, k.Day, r.Status, at, raw)
	return e
}
func (Repository) Get(ctx context.Context, tx db.Executor, sc foundation.Scope, id string, lock bool) (Record, error) {
	var r Record
	var raw []byte
	var token string
	var until sql.NullTime
	query := `SELECT body,lease_token,lease_until FROM affiliate_task_runs WHERE tenant_id=? AND app_id=? AND task_id=?`
	if lock {
		query += " FOR UPDATE"
	}
	e := tx.QueryRowContext(ctx, query, sc.Tenant, sc.App, id).Scan(&raw, &token, &until)
	if errors.Is(e, sql.ErrNoRows) {
		return r, ErrMissing
	}
	if e != nil {
		return r, e
	}
	if e = json.Unmarshal(raw, &r); e != nil {
		return r, e
	}
	r.LeaseToken = token
	if until.Valid {
		r.LeaseUntil = &until.Time
	}
	return r, nil
}
func (Repository) Save(ctx context.Context, tx db.Executor, sc foundation.Scope, r Record) error {
	raw, e := json.Marshal(r)
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `UPDATE affiliate_task_runs SET status=?,retry_requested=?,lease_token=?,lease_until=?,updated_at=?,body=? WHERE tenant_id=? AND app_id=? AND task_id=?`, r.Status, r.RetryRequested, r.LeaseToken, r.LeaseUntil, r.Updated, raw, sc.Tenant, sc.App, r.ID)
	return e
}
func (Repository) Attempt(ctx context.Context, tx db.Executor, sc foundation.Scope, a Attempt) error {
	raw, e := json.Marshal(a)
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO affiliate_task_attempts VALUES(?,?,?,?,?) ON DUPLICATE KEY UPDATE body=VALUES(body)`, sc.Tenant, sc.App, a.TaskID, a.Number, raw)
	return e
}
func (Repository) Attempts(ctx context.Context, tx db.Executor, sc foundation.Scope, id string) ([]Attempt, error) {
	rows, e := tx.QueryContext(ctx, `SELECT body FROM affiliate_task_attempts WHERE tenant_id=? AND app_id=? AND task_id=? ORDER BY attempt DESC LIMIT 100`, sc.Tenant, sc.App, id)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Attempt{}
	for rows.Next() {
		var raw []byte
		var a Attempt
		if e = rows.Scan(&raw); e != nil {
			return nil, e
		}
		if e = json.Unmarshal(raw, &a); e != nil {
			return nil, e
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
func (Repository) List(ctx context.Context, tx db.Executor, sc foundation.Scope, status, program string) ([]Record, error) {
	query := `SELECT body FROM affiliate_task_runs WHERE tenant_id=? AND app_id=?`
	args := []any{sc.Tenant, sc.App}
	if status != "" {
		query += " AND status=?"
		args = append(args, status)
	}
	if program != "" {
		query += " AND program_id=?"
		args = append(args, program)
	}
	query += " ORDER BY business_day DESC,updated_at DESC,task_id LIMIT 200"
	rows, e := tx.QueryContext(ctx, query, args...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Record{}
	for rows.Next() {
		var raw []byte
		var r Record
		if e = rows.Scan(&raw); e != nil {
			return nil, e
		}
		if e = json.Unmarshal(raw, &r); e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func (Repository) Rules(ctx context.Context, tx db.Executor, sc foundation.Scope) (RuleVersion, error) {
	v := RuleVersion{Rules: DefaultRules()}
	var raw []byte
	e := tx.QueryRowContext(ctx, `SELECT body FROM affiliate_task_alert_rules WHERE tenant_id=? AND app_id=? ORDER BY revision DESC LIMIT 1`, sc.Tenant, sc.App).Scan(&raw)
	if errors.Is(e, sql.ErrNoRows) {
		return v, nil
	}
	if e != nil {
		return v, e
	}
	e = json.Unmarshal(raw, &v)
	return v, e
}
func (Repository) SaveRules(ctx context.Context, tx db.Executor, sc foundation.Scope, v RuleVersion) error {
	raw, e := json.Marshal(v)
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO affiliate_task_alert_rules VALUES(?,?,?,?)`, sc.Tenant, sc.App, v.Revision, raw)
	return e
}
func (Repository) Alert(ctx context.Context, tx db.Executor, sc foundation.Scope, task, kind string) (Alert, error) {
	var a Alert
	var raw []byte
	e := tx.QueryRowContext(ctx, `SELECT body FROM affiliate_task_alerts WHERE tenant_id=? AND app_id=? AND task_id=? AND kind=?`, sc.Tenant, sc.App, task, kind).Scan(&raw)
	if errors.Is(e, sql.ErrNoRows) {
		return a, ErrMissing
	}
	if e != nil {
		return a, e
	}
	e = json.Unmarshal(raw, &a)
	return a, e
}
func (Repository) SaveAlert(ctx context.Context, tx db.Executor, sc foundation.Scope, a Alert, event string, at time.Time) error {
	raw, e := json.Marshal(a)
	if e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO affiliate_task_alerts VALUES(?,?,?,?,?) ON DUPLICATE KEY UPDATE body=VALUES(body)`, sc.Tenant, sc.App, a.TaskID, a.Kind, raw); e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO affiliate_task_alert_events VALUES(?,?,?,?,?,?)`, sc.Tenant, sc.App, event, a.TaskID, at, raw)
	return e
}
func (Repository) Alerts(ctx context.Context, tx db.Executor, sc foundation.Scope) ([]Alert, error) {
	rows, e := tx.QueryContext(ctx, `SELECT body FROM affiliate_task_alert_events WHERE tenant_id=? AND app_id=? ORDER BY occurred_at DESC,event_id DESC LIMIT 300`, sc.Tenant, sc.App)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Alert{}
	for rows.Next() {
		var raw []byte
		var a Alert
		if e = rows.Scan(&raw); e != nil {
			return nil, e
		}
		if e = json.Unmarshal(raw, &a); e != nil {
			return nil, e
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Existing 轮转未完成记录，避免已停止方案从业务扫描中退出后留下永久运行中的假状态。
func (r Repository) Existing(ctx context.Context, tx db.Executor, sc foundation.Scope) ([]Record, error) {
	rows, e := tx.QueryContext(ctx, `SELECT body FROM affiliate_task_runs WHERE tenant_id=? AND app_id=? AND status<>'SUCCEEDED' ORDER BY updated_at,task_id LIMIT 200`, sc.Tenant, sc.App)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Record{}
	for rows.Next() {
		var raw []byte
		var record Record
		if e = rows.Scan(&raw); e != nil {
			return nil, e
		}
		if e = json.Unmarshal(raw, &record); e != nil {
			return nil, e
		}
		out = append(out, record)
	}
	return out, rows.Err()
}
