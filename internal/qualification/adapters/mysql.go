package adapters

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	model "github.com/ex-business/ExCommission/internal/qualification/domain"
	"time"
)

type Repository struct{}

func (Repository) Policy(ctx context.Context, tx db.Executor, s foundation.Scope, id string) (model.Policy, error) {
	var raw []byte
	var p model.Policy
	err := tx.QueryRowContext(ctx, `SELECT configuration FROM qualification_policies WHERE tenant_id=? AND app_id=? AND policy_id=?`, s.Tenant, s.App, id).Scan(&raw)
	if err != nil {
		return p, err
	}
	err = json.Unmarshal(raw, &p)
	return p, err
}
func (Repository) SavePolicy(ctx context.Context, tx db.Executor, s foundation.Scope, p model.Policy, at time.Time) error {
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO qualification_policies VALUES(?,?,?,?,?)`, s.Tenant, s.App, p.ID, raw, at.UTC())
	return err
}

// LockMember 仅串行同一规则下同一会员的累计，不阻塞其他会员；不与账簿或代理关系共用可写状态。
func (Repository) LockMember(ctx context.Context, tx db.Executor, s foundation.Scope, policy, source, external string) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO qualification_states(tenant_id,app_id,policy_id,source_id,external_user_id) VALUES(?,?,?,?,?) ON DUPLICATE KEY UPDATE external_user_id=external_user_id`, s.Tenant, s.App, policy, source, external); err != nil {
		return err
	}
	var revision uint64
	return tx.QueryRowContext(ctx, `SELECT revision FROM qualification_states WHERE tenant_id=? AND app_id=? AND policy_id=? AND source_id=? AND external_user_id=? FOR UPDATE`, s.Tenant, s.App, policy, source, external).Scan(&revision)
}

// Status 使用当前读，避免事务在等待会员锁之前建立的快照遗漏已提交累计。
func (Repository) Status(ctx context.Context, tx db.Executor, s foundation.Scope, policy, source, external string) (model.Status, error) {
	var raw []byte
	var state model.Status
	err := tx.QueryRowContext(ctx, `SELECT state_json FROM qualification_states WHERE tenant_id=? AND app_id=? AND policy_id=? AND source_id=? AND external_user_id=? FOR UPDATE`, s.Tenant, s.App, policy, source, external).Scan(&raw)
	if err != nil {
		return state, err
	}
	if len(raw) == 0 {
		return state, errors.New("NOT_FOUND")
	}
	err = json.Unmarshal(raw, &state)
	return state, err
}

// ClaimFact 同时约束来源事件号与真实业务订单；事件别名必须与原事实一致，绝不重复累计金额。
func (Repository) ClaimFact(ctx context.Context, tx db.Executor, s foundation.Scope, f model.Fact, hash string) (bool, error) {
	if _, err := tx.ExecContext(ctx, `INSERT INTO qualification_event_keys VALUES(?,?,?,?,?) ON DUPLICATE KEY UPDATE event_id=event_id`, s.Tenant, s.App, f.Source, f.EventID, hash); err != nil {
		return false, err
	}
	var stored string
	if err := tx.QueryRowContext(ctx, `SELECT facts_hash FROM qualification_event_keys WHERE tenant_id=? AND app_id=? AND source_id=? AND event_id=? FOR UPDATE`, s.Tenant, s.App, f.Source, f.EventID).Scan(&stored); err != nil {
		return false, err
	}
	if stored != hash {
		return false, foundation.ErrConflict
	}
	// 唯一业务键竞争由数据库插入行锁串行；随后核验摘要，重复失败或篡改均回滚整个事实事务。
	raw, err := json.Marshal(f)
	if err != nil {
		return false, err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO qualification_facts VALUES(?,?,?,?,?,?,?,?,?,?,?) ON DUPLICATE KEY UPDATE facts_hash=facts_hash`, s.Tenant, s.App, f.PolicyID, f.Source, f.EventID, f.Type, f.BusinessID, f.ExternalUser, hash, raw, f.At.UTC())
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if err = tx.QueryRowContext(ctx, `SELECT facts_hash FROM qualification_facts WHERE tenant_id=? AND app_id=? AND policy_id=? AND source_id=? AND event_type=? AND business_id=? FOR UPDATE`, s.Tenant, s.App, f.PolicyID, f.Source, f.Type, f.BusinessID).Scan(&stored); err != nil {
		return false, err
	}
	if stored != hash {
		return false, foundation.ErrConflict
	}
	return n == 1, nil
}
func (Repository) Facts(ctx context.Context, tx db.Executor, s foundation.Scope, policy, source, external string) ([]model.Fact, error) {
	rows, err := tx.QueryContext(ctx, `SELECT fact_json FROM qualification_facts WHERE tenant_id=? AND app_id=? AND policy_id=? AND source_id=? AND external_user_id=? ORDER BY business_time,event_id FOR UPDATE`, s.Tenant, s.App, policy, source, external)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	facts := []model.Fact{}
	for rows.Next() {
		var raw []byte
		var f model.Fact
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &f); err != nil {
			return nil, err
		}
		facts = append(facts, f)
	}
	return facts, rows.Err()
}
func (Repository) SaveStatus(ctx context.Context, tx db.Executor, s foundation.Scope, state model.Status, at time.Time) error {
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE qualification_states SET state_json=?,revision=? WHERE tenant_id=? AND app_id=? AND policy_id=? AND source_id=? AND external_user_id=?`, raw, state.Revision, s.Tenant, s.App, state.PolicyID, state.Source, state.ExternalUser)
	if err != nil {
		return err
	}
	// 每次新事实保存完整修订快照供后续消费者使用；同事务outbox不直接发奖或改写代理等级。
	body, _ := json.Marshal(map[string]any{"schema_version": "1", "qualification": state})
	return mysql.AppendOutbox(ctx, tx, mysql.OutboxEvent{Scope: s, ID: qualificationEventID(state), Topic: "member.qualification.updated.v1", Payload: body, CreatedAt: at})
}

func qualificationEventID(state model.Status) string {
	identity, _ := json.Marshal([]any{state.PolicyID, state.Source, state.ExternalUser, state.Revision})
	sum := sha256.Sum256(identity)
	return "qualification_" + hex.EncodeToString(sum[:])
}
