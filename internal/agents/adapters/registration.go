package adapters

import (
	"context"
	"database/sql"
	"errors"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"time"
)

// LockRegistration 与迁移共用关系空间锁，先于用户写入取得，避免并发首次绑定与迁移形成环。
func (Repository) LockRegistration(ctx context.Context, tx db.Executor, s foundation.Scope) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_relation_spaces VALUES(?,?) ON DUPLICATE KEY UPDATE app_id=app_id`, s.Tenant, s.App); err != nil {
		return err
	}
	var locked int
	return tx.QueryRowContext(ctx, `SELECT 1 FROM agent_relation_spaces WHERE tenant_id=? AND app_id=? FOR UPDATE`, s.Tenant, s.App).Scan(&locked)
}

// Active 同时验证当前与注册业务时点；邀请人关闭后不可依靠旧链接或回填时间继续收取下线。
func (r Repository) Active(ctx context.Context, tx db.Executor, s foundation.Scope, id string, at time.Time) error {
	// 与状态变更共用账户行锁，停用先提交则拒绝注册，注册先取得锁则在停用前完成。
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM agent_accounts WHERE tenant_id=? AND app_id=? AND agent_id=? FOR UPDATE`, s.Tenant, s.App, id).Scan(&status); err != nil {
		return err
	}
	if status != "ACTIVE" {
		return foundation.ErrConflict
	}
	historical, err := r.AccountAt(ctx, tx, s, id, at)
	if err != nil {
		return err
	}
	if historical.Status != "ACTIVE" {
		return foundation.ErrConflict
	}
	return nil
}

// RegisterInitial 仅为新代理赋首次上级；历史代理必须走原审批迁移，不能借注册补绑或覆盖关系。
func (r Repository) RegisterInitial(ctx context.Context, tx db.Executor, s foundation.Scope, id, user, name string, parent *string, at time.Time) error {
	var existing string
	err := tx.QueryRowContext(ctx, `SELECT agent_id FROM agent_accounts WHERE tenant_id=? AND app_id=? AND user_id=? FOR UPDATE`, s.Tenant, s.App, user).Scan(&existing)
	if err == nil {
		return foundation.ErrConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if parent != nil {
		if *parent == id {
			return foundation.ErrConflict
		}
		if err = r.Active(ctx, tx, s, *parent, at); err != nil {
			return err
		}
	}
	if err = r.Create(ctx, tx, s, id, user, name, at); err != nil {
		return err
	}
	// 仅更新本事务刚创建的根关系，在注册时点直接生效，不制造先根后迁移的虚假历史。
	if parent != nil {
		_, err = tx.ExecContext(ctx, `UPDATE agent_relations SET parent_id=? WHERE tenant_id=? AND app_id=? AND agent_id=? AND effective_from=? AND parent_id IS NULL`, *parent, s.Tenant, s.App, id, at.UTC())
	}
	return err
}
