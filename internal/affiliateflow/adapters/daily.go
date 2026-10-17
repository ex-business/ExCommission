package adapters

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	model "github.com/ex-business/ExCommission/internal/affiliateflow/domain"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"sort"
	"time"
)

func (Repository) Closed(ctx context.Context, tx db.Executor, s foundation.Scope, source, day string) (bool, error) {
	var one int
	e := tx.QueryRowContext(ctx, `SELECT 1 FROM affiliate_source_watermarks WHERE tenant_id=? AND app_id=? AND source_id=? AND business_day=?`, s.Tenant, s.App, source, day).Scan(&one)
	if errors.Is(e, sql.ErrNoRows) {
		return false, nil
	}
	return e == nil, e
}
func (Repository) Manifest(ctx context.Context, tx db.Executor, s foundation.Scope, source string, start, end time.Time) (uint64, string, error) {
	rows, e := tx.QueryContext(ctx, `SELECT kind,business_id,facts_hash FROM affiliate_business_facts WHERE tenant_id=? AND app_id=? AND source_id=? AND business_time>=? AND business_time<?`, s.Tenant, s.App, source, start.UTC(), end.UTC())
	if e != nil {
		return 0, "", e
	}
	defer rows.Close()
	parts := []string{}
	for rows.Next() {
		var kind, id, hash string
		if e = rows.Scan(&kind, &id, &hash); e != nil {
			return 0, "", e
		}
		parts = append(parts, kind+"\n"+id+"\n"+hash)
	}
	if e = rows.Err(); e != nil {
		return 0, "", e
	}
	sort.Strings(parts)
	return uint64(len(parts)), model.ID(parts...), nil
}
func (Repository) SaveWatermark(ctx context.Context, tx db.Executor, s foundation.Scope, m model.Watermark, at time.Time) error {
	var old []byte
	b, e := json.Marshal(m)
	if e != nil {
		return e
	}
	e = tx.QueryRowContext(ctx, `SELECT body FROM affiliate_source_watermarks WHERE tenant_id=? AND app_id=? AND source_id=? AND business_day=?`, s.Tenant, s.App, m.Source, m.Day).Scan(&old)
	if e == nil {
		if string(old) != string(b) {
			var prior model.Watermark
			if json.Unmarshal(old, &prior) != nil || prior != m {
				return foundation.ErrConflict
			}
		}
		return nil
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO affiliate_source_watermarks VALUES(?,?,?,?,?,?)`, s.Tenant, s.App, m.Source, m.Day, b, at.UTC())
	return e
}
func (Repository) Watermarks(ctx context.Context, tx db.Executor, s foundation.Scope, day string) ([]model.Watermark, error) {
	rows, e := tx.QueryContext(ctx, `SELECT body FROM affiliate_source_watermarks WHERE tenant_id=? AND app_id=? AND business_day=? ORDER BY source_id`, s.Tenant, s.App, day)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []model.Watermark{}
	for rows.Next() {
		var b []byte
		var m model.Watermark
		if e = rows.Scan(&b); e != nil {
			return nil, e
		}
		if e = json.Unmarshal(b, &m); e != nil {
			return nil, e
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
func (Repository) PendingDays(ctx context.Context, tx db.Executor, s foundation.Scope) ([]string, error) {
	rows, e := tx.QueryContext(ctx, `SELECT business_day FROM affiliate_source_watermarks WHERE tenant_id=? AND app_id=? AND business_day NOT IN(SELECT business_day FROM affiliate_daily_runs WHERE tenant_id=? AND app_id=? AND status='SETTLED') GROUP BY business_day ORDER BY business_day LIMIT 31`, s.Tenant, s.App, s.Tenant, s.App)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	days := []string{}
	for rows.Next() {
		var d string
		if e = rows.Scan(&d); e != nil {
			return nil, e
		}
		days = append(days, d)
	}
	return days, rows.Err()
}
func (Repository) Daily(ctx context.Context, tx db.Executor, s foundation.Scope, day string) (model.Daily, error) {
	var out model.Daily
	var b []byte
	e := tx.QueryRowContext(ctx, `SELECT body FROM affiliate_daily_runs WHERE tenant_id=? AND app_id=? AND business_day=?`, s.Tenant, s.App, day).Scan(&b)
	if errors.Is(e, sql.ErrNoRows) {
		return out, errors.New("NOT_FOUND")
	}
	if e == nil {
		e = json.Unmarshal(b, &out)
	}
	return out, e
}
func (Repository) SaveDaily(ctx context.Context, tx db.Executor, s foundation.Scope, d model.Daily, at time.Time) error {
	b, e := json.Marshal(d)
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO affiliate_daily_runs VALUES(?,?,?,?,?,?) ON DUPLICATE KEY UPDATE status=VALUES(status),body=VALUES(body),updated_at=VALUES(updated_at)`, s.Tenant, s.App, d.Day, d.Status, b, at.UTC())
	return e
}
