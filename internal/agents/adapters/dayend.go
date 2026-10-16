package adapters

import (
	"context"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"time"
)

// DayEndSubjects 保留日末已停用代理：当天早些时候的合法业务仍需按其历史状态计算，不能整日漏算。
func (Repository) DayEndSubjects(ctx context.Context, tx db.Executor, s foundation.Scope, at time.Time) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT agent_id FROM agent_accounts WHERE tenant_id=? AND app_id=? AND created_at<=? ORDER BY agent_id`, s.Tenant, s.App, at)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		result = append(result, id)
	}
	return result, rows.Err()
}
