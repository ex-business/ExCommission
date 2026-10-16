package adapters

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	model "github.com/ex-business/ExCommission/internal/referral/domain"
	"time"
)

type Repository struct{}

func (Repository) Rule(ctx context.Context, tx db.Executor, s foundation.Scope, id string, lock bool) (model.Rule, error) {
	q := `SELECT rule_json FROM referral_reward_rules WHERE tenant_id=? AND app_id=? AND rule_id=?`
	if lock {
		q += " FOR UPDATE"
	}
	var raw []byte
	var r model.Rule
	err := tx.QueryRowContext(ctx, q, s.Tenant, s.App, id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return r, errors.New("NOT_FOUND")
	}
	if err != nil {
		return r, err
	}
	err = json.Unmarshal(raw, &r)
	return r, err
}
func (Repository) Create(ctx context.Context, tx db.Executor, s foundation.Scope, r model.Rule) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO referral_reward_rules VALUES(?,?,?,?,?)`, s.Tenant, s.App, r.Definition.ID, r.Status, raw)
	return err
}
func (r Repository) Activate(ctx context.Context, tx db.Executor, s foundation.Scope, id, hash, status, reviewer, reason string, at time.Time) error {
	v, err := r.Rule(ctx, tx, s, id, true)
	if err != nil {
		return err
	}
	if v.Hash != hash || v.Status != "PENDING_APPROVAL" {
		return errors.New("STALE_APPROVAL")
	}
	v.Status = status
	v.Reviewer = reviewer
	v.ReviewReason = reason
	v.ReviewedAt = &at
	if status == "ACTIVE" {
		v.ActivatedAt = &at
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE referral_reward_rules SET status=?,rule_json=? WHERE tenant_id=? AND app_id=? AND rule_id=?`, status, raw, s.Tenant, s.App, id)
	return err
}
func (Repository) ActiveRules(ctx context.Context, tx db.Executor, s foundation.Scope) ([]model.Rule, error) {
	rows, err := tx.QueryContext(ctx, `SELECT rule_json FROM referral_reward_rules WHERE tenant_id=? AND app_id=? AND status='ACTIVE' ORDER BY rule_id`, s.Tenant, s.App)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Rule{}
	for rows.Next() {
		var raw []byte
		var r model.Rule
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// LockAgent 对同一规则和代理串行刷新及领取，跨周期仍使用同一行，保证每阶只发一次。
func (Repository) LockAgent(ctx context.Context, tx db.Executor, s foundation.Scope, rule, agent string) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO referral_reward_progress(tenant_id,app_id,rule_id,agent_id) VALUES(?,?,?,?) ON DUPLICATE KEY UPDATE agent_id=agent_id`, s.Tenant, s.App, rule, agent); err != nil {
		return err
	}
	var id string
	return tx.QueryRowContext(ctx, `SELECT agent_id FROM referral_reward_progress WHERE tenant_id=? AND app_id=? AND rule_id=? AND agent_id=? FOR UPDATE`, s.Tenant, s.App, rule, agent).Scan(&id)
}
func (Repository) Progress(ctx context.Context, tx db.Executor, s foundation.Scope, rule, agent string) (model.Progress, error) {
	var raw []byte
	var p model.Progress
	err := tx.QueryRowContext(ctx, `SELECT progress_json FROM referral_reward_progress WHERE tenant_id=? AND app_id=? AND rule_id=? AND agent_id=? FOR UPDATE`, s.Tenant, s.App, rule, agent).Scan(&raw)
	if err != nil {
		return p, err
	}
	if len(raw) == 0 {
		return p, sql.ErrNoRows
	}
	err = json.Unmarshal(raw, &p)
	return p, err
}
func (Repository) SaveProgress(ctx context.Context, tx db.Executor, s foundation.Scope, p model.Progress) error {
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE referral_reward_progress SET progress_json=? WHERE tenant_id=? AND app_id=? AND rule_id=? AND agent_id=?`, raw, s.Tenant, s.App, p.RuleID, p.AgentID)
	return err
}
