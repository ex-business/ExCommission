package adapters

import (
	"context"
	calc "github.com/ex-business/ExCommission/internal/calculation/domain"
	model "github.com/ex-business/ExCommission/internal/ledger/domain"
	plan "github.com/ex-business/ExCommission/internal/plans/domain"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"time"
)

// CreditReward 复用唯一账簿、平衡分录、支付策略和outbox，不从奖励模块直接写钱或调用付款。
// event_type区分推荐奖励，真实plan_version_id保留财务报表链路；task_id在此引用领取资源。
func (r Repository) CreditReward(ctx context.Context, tx db.Executor, s foundation.Scope, c model.RewardCredit, now time.Time) (string, error) {
	for _, v := range []string{c.ClaimID, c.RuleID, c.TierID, c.AgentID, c.SourceID, c.PlanVersionID, c.Currency} {
		if !foundation.ValidID(v) {
			return "", foundation.ErrValidation
		}
	}
	if !validAmount(c.Amount) || c.BusinessTime.IsZero() || c.BusinessTime.After(now) {
		return "", foundation.ErrValidation
	}
	rule := stableID("referral-tier", c.RuleID, c.TierID)
	task := calc.Task{ID: c.ClaimID, Source: c.SourceID, BusinessID: c.ClaimID, EventType: "REFERRAL_REWARD", BusinessTime: c.BusinessTime}
	input := calc.Input{TaskID: task.ID, PlanVersionID: c.PlanVersionID, Plan: plan.Config{Currency: c.Currency, Settlement: c.Settlement}, Facts: calc.Facts{BusinessTime: c.BusinessTime}}
	result := calc.Result{TaskID: task.ID, Status: "SUCCEEDED", Items: []calc.Item{{RuleID: rule, BeneficiaryID: c.AgentID, Depth: 0, Currency: c.Currency, Minor: c.Amount, Algorithm: "REFERRAL_FIXED", Version: "1"}}}
	if err := r.Commit(ctx, tx, s, task, input, result, now); err != nil {
		return "", err
	}
	return stableID(task.Source, task.BusinessID, task.EventType, input.PlanVersionID, rule, c.AgentID, c.Currency), nil
}
