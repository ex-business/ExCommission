package mysql

import (
	"context"
	"github.com/ex-business/ExCommission/internal/platform/ports"
	"time"
)

type OutboxEvent struct {
	Scope     Scope
	ID, Topic string
	Payload   []byte
	CreatedAt time.Time
}

func AppendOutbox(ctx context.Context, tx ports.Executor, event OutboxEvent) error {
	if !event.Scope.Valid() || !validID(event.ID) || !validID(event.Topic) || event.CreatedAt.IsZero() {
		return ErrValidation
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO transaction_outbox(tenant_id,app_id,event_id,topic,payload,created_at) VALUES(?,?,?,?,?,?)`, event.Scope.Tenant, event.Scope.App, event.ID, event.Topic, event.Payload, event.CreatedAt.UTC())
	return err
}

// DeliverOutbox 接收方必须以event.ID幂等；投递后标记前崩溃会再次投递。
func (s *Store) DeliverOutbox(ctx context.Context, scope Scope, deliver func(OutboxEvent) error) error {
	if !scope.Valid() {
		return ErrValidation
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT event_id,topic,payload,created_at FROM transaction_outbox WHERE tenant_id=? AND app_id=? AND delivered_at IS NULL ORDER BY created_at,event_id LIMIT 100`, scope.Tenant, scope.App)
	if err != nil {
		return err
	}
	var events []OutboxEvent
	for rows.Next() {
		e := OutboxEvent{Scope: scope}
		if err = rows.Scan(&e.ID, &e.Topic, &e.Payload, &e.CreatedAt); err != nil {
			rows.Close()
			return err
		}
		events = append(events, e)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, event := range events {
		// 网络投递发生在数据库事务之外；接收方以作用域及事件ID防重。
		deliveryErr := deliver(event)
		_, err = s.DB.ExecContext(ctx, `UPDATE transaction_outbox SET attempts=attempts+1 WHERE tenant_id=? AND app_id=? AND event_id=?`, scope.Tenant, scope.App, event.ID)
		if err != nil {
			return err
		}
		if deliveryErr != nil {
			return deliveryErr
		}
		if _, err = s.DB.ExecContext(ctx, `UPDATE transaction_outbox SET delivered_at=UTC_TIMESTAMP(6) WHERE tenant_id=? AND app_id=? AND event_id=? AND delivered_at IS NULL`, scope.Tenant, scope.App, event.ID); err != nil {
			return err
		}
	}
	return nil
}
