package adapters

import (
	"context"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"time"
)

// 读取原始事实日期而非净额，已全额冲正日期仍需在任务历史中保留，不因净额归零消失。
func (Repository) TaskFactDays(ctx context.Context, tx db.Executor, sc foundation.Scope, loc *time.Location) ([]string, error) {
	rows, e := tx.QueryContext(ctx, `SELECT DISTINCT business_time FROM affiliate_business_facts WHERE tenant_id=? AND app_id=? ORDER BY business_time`, sc.Tenant, sc.App)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	seen := map[string]bool{}
	out := []string{}
	for rows.Next() {
		var at time.Time
		if e = rows.Scan(&at); e != nil {
			return nil, e
		}
		day := at.In(loc).Format("2006-01-02")
		if !seen[day] {
			out = append(out, day)
			seen[day] = true
		}
	}
	return out, rows.Err()
}
