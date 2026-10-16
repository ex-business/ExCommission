package adapters

import (
	"context"
	"errors"
	model "github.com/ex-business/ExCommission/internal/calculation/domain"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"time"
)

// DayFacts 使用计佣已验签、按订单去重的事实；明确水位以内的数据进入本次日终。
// 上限超出即拒绝冻结，避免把截断数据当完整统计。大规模来源可替换此读取端口。
func (Tasks) DayFacts(ctx context.Context, tx db.Executor, s foundation.Scope, source string, start, end, cutoff time.Time) ([]model.Task, error) {
	rows, err := tx.QueryContext(ctx, `SELECT `+taskColumns+` FROM calculation_tasks WHERE tenant_id=? AND app_id=? AND source_id=? AND business_time>=? AND business_time<? AND created_at<=? ORDER BY task_id LIMIT 10001`, s.Tenant, s.App, source, start, end, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tasks := []model.Task{}
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(tasks) > 10000 {
		return nil, errors.New("UNSUPPORTED_CAPABILITY")
	}
	return tasks, nil
}

// ReleaseDayEnd 只恢复因等待日终挂起的任务；其他风控、失败和已完成任务不被自动恢复。
func (Tasks) ReleaseDayEnd(ctx context.Context, tx db.Executor, s foundation.Scope, ids []string, at time.Time) error {
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `UPDATE calculation_tasks SET status='QUEUED',reason_code=NULL,revision=revision+1,updated_at=? WHERE tenant_id=? AND app_id=? AND task_id=? AND status='SUSPENDED' AND reason_code='DAYEND_PENDING' AND snapshot IS NULL`, at, s.Tenant, s.App, id); err != nil {
			return err
		}
	}
	return nil
}
