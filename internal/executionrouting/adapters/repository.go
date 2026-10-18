// Package adapters 在原业务事务中登记执行归属，任何后续错误都随业务事务回滚。
package adapters

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"sort"
	"time"

	model "github.com/ex-business/ExCommission/internal/executionrouting/domain"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
)

type Repository struct{}

// Ensure 仅初始化独立模块的表，必须在启动阶段调用，不能在领取或接收事务内执行DDL。
func (Repository) Ensure(ctx context.Context, q db.Executor) error {
	for _, query := range []string{
		`CREATE TABLE IF NOT EXISTS execution_ownership (tenant_id VARCHAR(128) NOT NULL,app_id VARCHAR(128) NOT NULL,identity_hash CHAR(64) NOT NULL,source_id VARCHAR(128) NOT NULL,event_id VARCHAR(128) NOT NULL,business_id VARCHAR(128) NOT NULL,event_kind VARCHAR(128) NOT NULL,engine VARCHAR(16) NOT NULL,created_at DATETIME(6) NOT NULL,PRIMARY KEY(tenant_id,app_id,identity_hash)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_bin`,
		`CREATE TABLE IF NOT EXISTS execution_route_heads (tenant_id VARCHAR(128) NOT NULL,app_id VARCHAR(128) NOT NULL,revision BIGINT UNSIGNED NOT NULL,config JSON NOT NULL,PRIMARY KEY(tenant_id,app_id)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_bin`,
		`CREATE TABLE IF NOT EXISTS execution_route_versions (tenant_id VARCHAR(128) NOT NULL,app_id VARCHAR(128) NOT NULL,revision BIGINT UNSIGNED NOT NULL,config JSON NOT NULL,approval_id VARCHAR(128) NOT NULL,published_at DATETIME(6) NOT NULL,PRIMARY KEY(tenant_id,app_id,revision)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_bin`,
		`CREATE TABLE IF NOT EXISTS execution_route_receipts (tenant_id VARCHAR(128) NOT NULL,app_id VARCHAR(128) NOT NULL,identity_hash CHAR(64) NOT NULL,route_id VARCHAR(128) NOT NULL,config_revision BIGINT UNSIGNED NOT NULL,business_time DATETIME(6) NOT NULL,PRIMARY KEY(tenant_id,app_id,identity_hash)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_bin`,
	} {
		if _, err := q.ExecContext(ctx, query); err != nil {
			return err
		}
	}
	return nil
}

// Acquire 要求tx为调用方业务事务。唯一键串行化并发受理，归属不按重试时间或新配置改写。
// 同引擎正文冲突继续由原接入防重检查；此处不解析金额，不生成权益，不直接执行资金操作。
func (r Repository) Acquire(ctx context.Context, tx db.Executor, sc foundation.Scope, c model.Claim, now time.Time) error {
	if !sc.Valid() || now.IsZero() || c.At.IsZero() {
		return foundation.ErrValidation
	}
	if err := c.Validate(); err != nil {
		return err
	}
	state, err := r.Head(ctx, tx, sc, true)
	if err != nil {
		return err
	}
	keys := c.Keys()
	ordered := keys[:]
	sort.Strings(ordered)
	existing := false
	for _, key := range ordered {
		var engine, source, business, kind string
		err = tx.QueryRowContext(ctx, `SELECT engine,source_id,business_id,event_kind FROM execution_ownership WHERE tenant_id=? AND app_id=? AND identity_hash=? FOR UPDATE`, sc.Tenant, sc.App, key).Scan(&engine, &source, &business, &kind)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		existing = true
		if engine != c.Engine {
			return ownershipConflict(ctx, sc, c)
		}
		if source != c.Source || business != c.BusinessID || kind != c.Kind {
			return foundation.ErrConflict
		}
	}
	// 兼容升级前数据；双引擎历史冲突拒绝新受理，不自动撤销或重算既有权益。
	general := `SELECT 1 FROM integration_event_keys WHERE tenant_id=? AND app_id=? AND source_id=? AND (event_id=? OR (business_id=? AND event_type=?)) LIMIT 1 FOR UPDATE`
	affiliate := `SELECT 1 FROM affiliate_business_facts WHERE tenant_id=? AND app_id=? AND source_id=? AND (event_id=? OR (business_id=? AND kind=?)) LIMIT 1 FOR UPDATE`
	own, other := general, affiliate
	if c.Engine == model.Affiliate {
		own, other = affiliate, general
	}
	lookup := func(query string) (bool, error) {
		var n int
		e := tx.QueryRowContext(ctx, query, sc.Tenant, sc.App, c.Source, c.EventID, c.BusinessID, c.Kind).Scan(&n)
		if errors.Is(e, sql.ErrNoRows) {
			return false, nil
		}
		return e == nil, e
	}
	if found, e := lookup(other); e != nil {
		return e
	} else if found {
		return ownershipConflict(ctx, sc, c)
	}
	legacy, e := lookup(own)
	if e != nil {
		return e
	}
	routeID := "LEGACY"
	revision := uint64(0)
	// 已成功受理或升级前已存在的业务维持原引擎；只对新业务按发生时间选择发布路由。
	if !existing && !legacy {
		if route, ok := state.Config.Select(c.Source, c.Kind, c.At); ok {
			if route.Engine != c.Engine {
				return ownershipConflict(ctx, sc, c)
			}
			routeID = route.ID
			revision = state.Revision
		}
	}
	for _, key := range ordered {
		if _, err = tx.ExecContext(ctx, `INSERT INTO execution_ownership VALUES(?,?,?,?,?,?,?,?,?) ON DUPLICATE KEY UPDATE identity_hash=identity_hash`, sc.Tenant, sc.App, key, c.Source, c.EventID, c.BusinessID, c.Kind, c.Engine, now.UTC()); err != nil {
			return err
		}
		// 已有业务使用新event_id重放时复制业务键的原路由，不能写入当前配置版本冒充首次决策。
		_, err = tx.ExecContext(ctx, `INSERT IGNORE INTO execution_route_receipts SELECT ?,?,?,route_id,config_revision,business_time FROM execution_route_receipts WHERE tenant_id=? AND app_id=? AND identity_hash=?`, sc.Tenant, sc.App, key, sc.Tenant, sc.App, c.Keys()[1])
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT IGNORE INTO execution_route_receipts VALUES(?,?,?,?,?,?)`, sc.Tenant, sc.App, key, routeID, revision, c.At.UTC()); err != nil {
			return err
		}
	}
	return nil
}

// 冲突只记录定位信息，不保存签名、凭据、事件正文或会员敏感资料。
func ownershipConflict(ctx context.Context, sc foundation.Scope, c model.Claim) error {
	slog.WarnContext(ctx, "拒绝跨引擎重复受理业务", "request_id", foundation.RequestFrom(ctx).RequestID, "tenant_id", sc.Tenant, "app_id", sc.App, "source_id", c.Source, "business_id", c.BusinessID, "engine", c.Engine, "reason", "EXECUTION_ENGINE_CONFLICT")
	return model.ErrOwned
}
