package domain

import (
	plan "github.com/ex-business/ExCommission/internal/plans/domain"
	"time"
)

// RewardCredit 为独立奖励的资金接入口；调用方须保存奖励规则审批与领取证据，并使用真实发布方案版本。
type RewardCredit struct {
	ClaimID, RuleID, TierID, AgentID, SourceID, PlanVersionID, Currency, Amount string
	Settlement                                                                  plan.SettlementPolicy
	BusinessTime                                                                time.Time
}
