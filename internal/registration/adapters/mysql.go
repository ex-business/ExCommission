package adapters

import (
	"context"
	"encoding/json"
	"errors"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	model "github.com/ex-business/ExCommission/internal/registration/domain"
	"time"
)

type Repository struct{}

func (Repository) Invitation(ctx context.Context, tx db.Executor, s foundation.Scope, source, code string) (model.Invitation, error) {
	i := model.Invitation{Code: code, SourceID: source}
	err := tx.QueryRowContext(ctx, `SELECT agent_id,created_at FROM registration_invitations WHERE tenant_id=? AND app_id=? AND source_id=? AND invitation_code=?`, s.Tenant, s.App, source, code).Scan(&i.AgentID, &i.CreatedAt)
	return i, err
}
func (Repository) SaveInvitation(ctx context.Context, tx db.Executor, s foundation.Scope, i model.Invitation, at time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO registration_invitations VALUES(?,?,?,?,?,?)`, s.Tenant, s.App, i.SourceID, i.Code, i.AgentID, at.UTC())
	return err
}
func (Repository) Receipt(ctx context.Context, tx db.Executor, s foundation.Scope, source, user string) (model.Receipt, string, error) {
	var body []byte
	var hash string
	var r model.Receipt
	err := tx.QueryRowContext(ctx, `SELECT receipt,facts_hash FROM registration_receipts WHERE tenant_id=? AND app_id=? AND source_id=? AND external_user_id=?`, s.Tenant, s.App, source, user).Scan(&body, &hash)
	if err != nil {
		return r, hash, err
	}
	err = json.Unmarshal(body, &r)
	return r, hash, err
}
func (Repository) SaveReceipt(ctx context.Context, tx db.Executor, s foundation.Scope, r model.Receipt, hash string) error {
	body, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO registration_receipts VALUES(?,?,?,?,?,?,?)`, s.Tenant, s.App, r.SourceID, r.ExternalUserID, r.EventID, hash, body)
	if err != nil {
		return err
	}
	// 注册完成事件和业务数据共用事务，后续消费者按 event_id 幂等；当前只接本地 outbox 投递。
	return mysql.AppendOutbox(ctx, tx, mysql.OutboxEvent{Scope: s, ID: "registration_" + r.AgentID, Topic: "registration.completed.v1", Payload: body, CreatedAt: r.RegisteredAt})
}

// ClaimEvent 将每个投递事件号绑定事实摘要；即使同一用户更换事件号重投，也保留所有别名防止跨用户复用。
func (Repository) ClaimEvent(ctx context.Context, tx db.Executor, s foundation.Scope, source, event, hash string) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO registration_events VALUES(?,?,?,?,?) ON DUPLICATE KEY UPDATE event_id=event_id`, s.Tenant, s.App, source, event, hash); err != nil {
		return err
	}
	var stored string
	if err := tx.QueryRowContext(ctx, `SELECT facts_hash FROM registration_events WHERE tenant_id=? AND app_id=? AND source_id=? AND event_id=? FOR UPDATE`, s.Tenant, s.App, source, event).Scan(&stored); err != nil {
		return err
	}
	if stored != hash {
		return errors.New("STATE_CONFLICT")
	}
	return nil
}
