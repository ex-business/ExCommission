// Package adapters 持久化本站接入配置与不可变发布版本，不保存认证密钥或任意执行代码。
package adapters

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	model "github.com/ex-business/ExCommission/internal/ingestion/domain"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"time"
)

type Repository struct{}
type State struct {
	Revision    uint64       `json:"revision"`
	Config      model.Config `json:"config"`
	PublishedAt *time.Time   `json:"published_at,omitempty"`
}

// Ensure 仅创建本模块拥有的表；必须在业务事务开始前初始化，避免DDL隐式提交业务事务。
func (Repository) Ensure(ctx context.Context, tx db.Executor) error {
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS ingestion_heads(tenant_id VARCHAR(128) NOT NULL,app_id VARCHAR(128) NOT NULL,revision BIGINT UNSIGNED NOT NULL,config JSON NOT NULL,published_at DATETIME(6) NULL,PRIMARY KEY(tenant_id,app_id)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_bin`,
		`CREATE TABLE IF NOT EXISTS ingestion_versions(tenant_id VARCHAR(128) NOT NULL,app_id VARCHAR(128) NOT NULL,revision BIGINT UNSIGNED NOT NULL,config JSON NOT NULL,published_at DATETIME(6) NOT NULL,approval_id VARCHAR(128) NOT NULL,PRIMARY KEY(tenant_id,app_id,revision),KEY ingestion_versions_time(tenant_id,app_id,published_at)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_bin`,
		`CREATE TABLE IF NOT EXISTS ingestion_receipts(tenant_id VARCHAR(128) NOT NULL,app_id VARCHAR(128) NOT NULL,source_id VARCHAR(64) NOT NULL,event_id VARCHAR(128) NOT NULL,business_id VARCHAR(128) NOT NULL,event_type VARCHAR(64) NOT NULL,schema_version VARCHAR(64) NOT NULL,config_revision BIGINT UNSIGNED NOT NULL,body_hash CHAR(64) NOT NULL,status VARCHAR(32) NOT NULL,received_at DATETIME(6) NOT NULL,PRIMARY KEY(tenant_id,app_id,source_id,event_id),KEY ingestion_receipts_time(tenant_id,app_id,received_at)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_bin`,
	} {
		if _, e := tx.ExecContext(ctx, q); e != nil {
			return e
		}
	}
	return nil
}
func (Repository) Head(ctx context.Context, tx db.Executor, sc foundation.Scope, lock bool) (State, error) {
	out := State{Config: model.Config{Sources: []model.Source{}}}
	if lock {
		if _, e := tx.ExecContext(ctx, `INSERT IGNORE INTO ingestion_heads VALUES(?,?,0,?,NULL)`, sc.Tenant, sc.App, `{"sources":[]}`); e != nil {
			return out, e
		}
	}
	q := `SELECT revision,config,published_at FROM ingestion_heads WHERE tenant_id=? AND app_id=?`
	if lock {
		q += " FOR UPDATE"
	}
	var raw []byte
	var at sql.NullTime
	e := tx.QueryRowContext(ctx, q, sc.Tenant, sc.App).Scan(&out.Revision, &raw, &at)
	if errors.Is(e, sql.ErrNoRows) {
		return out, nil
	}
	if e != nil {
		return out, e
	}
	e = json.Unmarshal(raw, &out.Config)
	if at.Valid {
		out.PublishedAt = &at.Time
	}
	return out, e
}

// Apply 在原独立审批事务内执行，审批依据过时必须拒绝，不覆盖已生效版本。
func (r Repository) Apply(ctx context.Context, tx db.Executor, sc foundation.Scope, revision uint64, c model.Config, approval string, at time.Time) error {
	old, e := r.Head(ctx, tx, sc, true)
	if e != nil {
		return e
	}
	if old.Revision != revision {
		return errors.New("REVISION_CONFLICT")
	}
	if e = model.ValidateChange(old.Config, c); e != nil {
		return e
	}
	raw, _ := json.Marshal(c)
	if _, e = tx.ExecContext(ctx, `INSERT INTO ingestion_versions VALUES(?,?,?,?,?,?)`, sc.Tenant, sc.App, revision+1, raw, at, approval); e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `UPDATE ingestion_heads SET revision=?,config=?,published_at=? WHERE tenant_id=? AND app_id=?`, revision+1, raw, at, sc.Tenant, sc.App)
	return e
}

// RequiredSources 取业务日内曾启用来源的并集；中途停用不能让当日提前绕过来源关账。
func (r Repository) RequiredSources(ctx context.Context, tx db.Executor, sc foundation.Scope, start, end time.Time) ([]string, error) {
	rows, e := tx.QueryContext(ctx, `SELECT config FROM ingestion_versions WHERE tenant_id=? AND app_id=? AND (published_at>=? AND published_at<? OR revision=(SELECT MAX(v.revision) FROM ingestion_versions v WHERE v.tenant_id=? AND v.app_id=? AND v.published_at<?)) ORDER BY revision`, sc.Tenant, sc.App, start, end, sc.Tenant, sc.App, start)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	seen := map[string]bool{}
	out := []string{}
	for rows.Next() {
		var raw []byte
		var c model.Config
		if e = rows.Scan(&raw); e != nil {
			return nil, e
		}
		if e = json.Unmarshal(raw, &c); e != nil {
			return nil, e
		}
		for _, s := range c.Sources {
			if s.Enabled && !seen[s.ID] {
				seen[s.ID] = true
				out = append(out, s.ID)
			}
		}
	}
	return out, rows.Err()
}

type Receipt struct {
	Source     string    `json:"source_id"`
	EventID    string    `json:"event_id"`
	BusinessID string    `json:"business_id"`
	EventType  string    `json:"event_type"`
	Version    string    `json:"schema_version"`
	Revision   uint64    `json:"config_revision"`
	Status     string    `json:"status"`
	ReceivedAt time.Time `json:"received_at"`
}

func (Repository) Records(ctx context.Context, tx db.Executor, sc foundation.Scope) ([]Receipt, error) {
	rows, e := tx.QueryContext(ctx, `SELECT source_id,event_id,business_id,event_type,schema_version,config_revision,status,received_at FROM ingestion_receipts WHERE tenant_id=? AND app_id=? ORDER BY received_at DESC,event_id LIMIT 100`, sc.Tenant, sc.App)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Receipt{}
	for rows.Next() {
		var r Receipt
		if e = rows.Scan(&r.Source, &r.EventID, &r.BusinessID, &r.EventType, &r.Version, &r.Revision, &r.Status, &r.ReceivedAt); e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
