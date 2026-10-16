package adapters

import (
	"context"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	model "github.com/ex-business/ExCommission/internal/referral/domain"
	"time"
)

// Enqueue 仅可靠接收事件，业务重试由推荐奖励自己的队列处理，不阻塞原审计/通知投递。
func (Repository) Enqueue(ctx context.Context, tx db.Executor, s foundation.Scope, e model.Event) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO referral_reward_inbox(tenant_id,app_id,event_id,body,observed_at,next_attempt_at) VALUES(?,?,?,?,?,?) ON DUPLICATE KEY UPDATE event_id=event_id`, s.Tenant, s.App, e.ID, e.Body, e.At, e.At)
	return err
}
func (Repository) Pending(ctx context.Context, tx db.Executor, s foundation.Scope, at time.Time) ([]model.Event, error) {
	rows, err := tx.QueryContext(ctx, `SELECT event_id,body,observed_at,attempts FROM referral_reward_inbox WHERE tenant_id=? AND app_id=? AND status='PENDING' AND next_attempt_at<=? ORDER BY observed_at,event_id LIMIT 100`, s.Tenant, s.App, at)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Event{}
	for rows.Next() {
		var e model.Event
		if err = rows.Scan(&e.ID, &e.Body, &e.At, &e.Attempts); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
func (Repository) Complete(ctx context.Context, tx db.Executor, s foundation.Scope, id string) error {
	_, err := tx.ExecContext(ctx, `UPDATE referral_reward_inbox SET status='DONE',last_error=NULL WHERE tenant_id=? AND app_id=? AND event_id=?`, s.Tenant, s.App, id)
	return err
}
func (Repository) Failed(ctx context.Context, tx db.Executor, s foundation.Scope, e model.Event, at time.Time, reason string) error {
	seconds := 1
	for i := 0; i < e.Attempts && seconds < 60; i++ {
		seconds *= 2
	}
	if seconds > 60 {
		seconds = 60
	}
	if len(reason) > 128 {
		reason = reason[:128]
	}
	_, err := tx.ExecContext(ctx, `UPDATE referral_reward_inbox SET attempts=attempts+1,next_attempt_at=?,last_error=? WHERE tenant_id=? AND app_id=? AND event_id=? AND status='PENDING'`, at.Add(time.Duration(seconds)*time.Second), reason, s.Tenant, s.App, e.ID)
	return err
}
