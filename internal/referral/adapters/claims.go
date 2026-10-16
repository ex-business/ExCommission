package adapters

import (
	"context"
	"encoding/json"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	model "github.com/ex-business/ExCommission/internal/referral/domain"
	"time"
)

func (r Repository) Claim(ctx context.Context, tx db.Executor, s foundation.Scope, rule, agent, tier string) (model.Receipt, error) {
	return r.GetClaim(ctx, tx, s, model.ClaimID(rule, agent, tier))
}
func (Repository) GetClaim(ctx context.Context, tx db.Executor, s foundation.Scope, id string) (model.Receipt, error) {
	var raw []byte
	var out model.Receipt
	err := tx.QueryRowContext(ctx, `SELECT receipt_json FROM referral_reward_claims WHERE tenant_id=? AND app_id=? AND claim_id=?`, s.Tenant, s.App, id).Scan(&raw)
	if err != nil {
		return out, err
	}
	err = json.Unmarshal(raw, &out)
	return out, err
}

// SaveClaim 的业务唯一键是数据库第二道防重，不能靠新投递键、新周期或新方案版本重复奖励。
func (Repository) SaveClaim(ctx context.Context, tx db.Executor, s foundation.Scope, r model.Receipt, at time.Time) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO referral_reward_claims VALUES(?,?,?,?,?,?,?)`, s.Tenant, s.App, r.Reward.ClaimID, r.RuleID, r.AgentID, r.Reward.Tier.ID, raw)
	if err != nil {
		return err
	}
	return mysql.AppendOutbox(ctx, tx, mysql.OutboxEvent{Scope: s, ID: r.Reward.ClaimID, Topic: "referral.reward.claimed.v1", Payload: raw, CreatedAt: at})
}
