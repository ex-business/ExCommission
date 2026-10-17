package adapters

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	affiliate "github.com/ex-business/ExCommission/internal/affiliate/domain"
	model "github.com/ex-business/ExCommission/internal/affiliateflow/domain"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"time"
)

type Repository struct{}

// Lock 是本版本站点编排串行点，事实落库、进度与发奖共用事务，避免不同会员同时推进同一推荐人的阶梯。
// 领域计算与端口不依赖此锁；大站可替换为来源水位锁和按受益人分片的持久化实现。
func (Repository) Lock(ctx context.Context, tx db.Executor, s foundation.Scope) error {
	if _, e := tx.ExecContext(ctx, `INSERT INTO affiliate_flow_spaces VALUES(?,?) ON DUPLICATE KEY UPDATE app_id=app_id`, s.Tenant, s.App); e != nil {
		return e
	}
	var one int
	return tx.QueryRowContext(ctx, `SELECT 1 FROM affiliate_flow_spaces WHERE tenant_id=? AND app_id=? FOR UPDATE`, s.Tenant, s.App).Scan(&one)
}
func (Repository) SaveFact(ctx context.Context, tx db.Executor, s foundation.Scope, event model.Event, user string, at time.Time, late bool, eligible bool) (bool, error) {
	hash := event.BusinessHash()
	var e error
	// 业务订单号才是资金事实身份；投递事件可重试换号，但同事件号不能指向另一个订单。
	var alias string
	e = tx.QueryRowContext(ctx, `SELECT facts_hash FROM affiliate_business_events WHERE tenant_id=? AND app_id=? AND source_id=? AND event_id=?`, s.Tenant, s.App, event.Source, event.EventID).Scan(&alias)
	if e == nil && alias != hash {
		return false, foundation.ErrConflict
	}
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return false, e
	}
	if errors.Is(e, sql.ErrNoRows) {
		if _, e = tx.ExecContext(ctx, `INSERT INTO affiliate_business_events VALUES(?,?,?,?,?)`, s.Tenant, s.App, event.Source, event.EventID, hash); e != nil {
			return false, e
		}
	}
	var old string
	e = tx.QueryRowContext(ctx, `SELECT facts_hash FROM affiliate_business_facts WHERE tenant_id=? AND app_id=? AND source_id=? AND kind=? AND business_id=? UNION ALL SELECT facts_hash FROM affiliate_late_business_facts WHERE tenant_id=? AND app_id=? AND source_id=? AND kind=? AND business_id=? LIMIT 1`, s.Tenant, s.App, event.Source, event.Kind, event.BusinessID, s.Tenant, s.App, event.Source, event.Kind, event.BusinessID).Scan(&old)
	if e == nil {
		if old != hash {
			return false, foundation.ErrConflict
		}
		return false, nil
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return false, e
	}
	raw, e := json.Marshal(event)
	if e != nil {
		return false, e
	}
	table := "affiliate_business_facts"
	if late {
		table = "affiliate_late_business_facts"
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO `+table+`(tenant_id,app_id,source_id,event_id,business_id,kind,user_id,facts_hash,body,business_time,received_at,reward_eligible) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, s.Tenant, s.App, event.Source, event.EventID, event.BusinessID, event.Kind, user, hash, raw, event.At.UTC(), at.UTC(), eligible)
	return e == nil, e
}
func (Repository) Facts(ctx context.Context, tx db.Executor, s foundation.Scope, before time.Time) ([]affiliate.Fact, error) {
	rows, e := tx.QueryContext(ctx, `SELECT user_id,body FROM affiliate_business_facts WHERE tenant_id=? AND app_id=? AND reward_eligible=TRUE AND business_time<? ORDER BY business_time,source_id,event_id`, s.Tenant, s.App, before.UTC())
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []affiliate.Fact{}
	for rows.Next() {
		var user string
		var b []byte
		if e = rows.Scan(&user, &b); e != nil {
			return nil, e
		}
		var event model.Event
		if e = json.Unmarshal(b, &event); e != nil {
			return nil, e
		}
		out = append(out, event.Fact(user))
	}
	return out, rows.Err()
}
func (Repository) Gate(ctx context.Context, tx db.Executor, s foundation.Scope, user string, registered time.Time) (model.Gate, error) {
	out := model.Gate{Enabled: true, Since: registered, Revision: 0, Updated: registered}
	var b []byte
	e := tx.QueryRowContext(ctx, `SELECT body FROM affiliate_agent_gates WHERE tenant_id=? AND app_id=? AND user_id=?`, s.Tenant, s.App, user).Scan(&b)
	if errors.Is(e, sql.ErrNoRows) {
		return out, nil
	}
	if e == nil {
		e = json.Unmarshal(b, &out)
	}
	return out, e
}
func (Repository) SaveGate(ctx context.Context, tx db.Executor, s foundation.Scope, user string, g model.Gate) error {
	b, e := json.Marshal(g)
	if e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO affiliate_gate_history VALUES(?,?,?,?,?,?)`, s.Tenant, s.App, user, g.Revision, b, g.Updated.UTC()); e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO affiliate_agent_gates VALUES(?,?,?,?,?) ON DUPLICATE KEY UPDATE revision=VALUES(revision),body=VALUES(body)`, s.Tenant, s.App, user, g.Revision, b)
	return e
}
func (Repository) Rewards(ctx context.Context, tx db.Executor, s foundation.Scope, user string) ([]model.Reward, error) {
	rows, e := tx.QueryContext(ctx, `SELECT body FROM affiliate_business_rewards WHERE tenant_id=? AND app_id=? AND user_id=? ORDER BY unlocked_at,reward_id`, s.Tenant, s.App, user)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []model.Reward{}
	for rows.Next() {
		var b []byte
		var r model.Reward
		if e = rows.Scan(&b); e != nil {
			return nil, e
		}
		if e = json.Unmarshal(b, &r); e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func (Repository) Reward(ctx context.Context, tx db.Executor, s foundation.Scope, id string) (model.Reward, error) {
	var b []byte
	var r model.Reward
	e := tx.QueryRowContext(ctx, `SELECT body FROM affiliate_business_rewards WHERE tenant_id=? AND app_id=? AND reward_id=?`, s.Tenant, s.App, id).Scan(&b)
	if errors.Is(e, sql.ErrNoRows) {
		return r, errors.New("NOT_FOUND")
	}
	if e == nil {
		e = json.Unmarshal(b, &r)
	}
	return r, e
}
func (Repository) SaveReward(ctx context.Context, tx db.Executor, s foundation.Scope, r model.Reward) error {
	if _, e := affiliate.Money(r.Amount, true); e != nil {
		return e
	}
	b, e := json.Marshal(r)
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO affiliate_business_rewards VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?) ON DUPLICATE KEY UPDATE status=VALUES(status),amount=VALUES(amount),claimed_at=VALUES(claimed_at),revision=VALUES(revision),body=VALUES(body)`, s.Tenant, s.App, r.ID, r.UserID, r.AgentID, r.Kind, r.Status, r.Currency, r.Amount, r.UnlockedAt.UTC(), r.ClaimedAt, r.Revision, b)
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO affiliate_reward_history(tenant_id,app_id,reward_id,revision,recorded_at,body) VALUES(?,?,?,?,?,?)`, s.Tenant, s.App, r.ID, r.Revision, r.UpdatedAt.UTC(), b)
	return e
}
func (Repository) SaveProgress(ctx context.Context, tx db.Executor, s foundation.Scope, user string, p model.Progress, at time.Time) error {
	b, e := json.Marshal(p)
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO affiliate_reward_progress VALUES(?,?,?,?,?) ON DUPLICATE KEY UPDATE body=VALUES(body),updated_at=VALUES(updated_at)`, s.Tenant, s.App, user, b, at.UTC())
	return e
}
func (Repository) Progress(ctx context.Context, tx db.Executor, s foundation.Scope, user string) (model.Progress, error) {
	var p model.Progress
	var b []byte
	e := tx.QueryRowContext(ctx, `SELECT body FROM affiliate_reward_progress WHERE tenant_id=? AND app_id=? AND user_id=?`, s.Tenant, s.App, user).Scan(&b)
	if errors.Is(e, sql.ErrNoRows) {
		return p, errors.New("NOT_FOUND")
	}
	if e == nil {
		e = json.Unmarshal(b, &p)
	}
	return p, e
}

// GateAt 仅用于历史报表，资金操作始终读取当前Gate；未发生过开关操作时按注册起点开启。
func (Repository) GateAt(ctx context.Context, tx db.Executor, s foundation.Scope, user string, registered, at time.Time) (model.Gate, error) {
	g := model.Gate{Enabled: true, Since: registered, Updated: registered}
	var b []byte
	e := tx.QueryRowContext(ctx, `SELECT body FROM affiliate_gate_history WHERE tenant_id=? AND app_id=? AND user_id=? AND occurred_at<=? ORDER BY revision DESC LIMIT 1`, s.Tenant, s.App, user, at.UTC()).Scan(&b)
	if errors.Is(e, sql.ErrNoRows) {
		return g, nil
	}
	if e == nil {
		e = json.Unmarshal(b, &g)
	}
	return g, e
}
