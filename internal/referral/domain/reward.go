// Package domain 定义推荐奖励阶梯；与有效业务量返佣分开，不读取数据库或使用浮点金额。
package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	plan "github.com/ex-business/ExCommission/internal/plans/domain"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	qualification "github.com/ex-business/ExCommission/internal/qualification/domain"
	"sort"
	"strconv"
	"time"
)

type Tier struct {
	ID       string `json:"tier_id"`
	Required int    `json:"required_direct_members"`
	Amount   string `json:"amount_minor"`
}
type Definition struct {
	ID       string `json:"rule_id"`
	Name     string `json:"name"`
	PlanID   string `json:"plan_id"`
	Source   string `json:"source_id"`
	PolicyID string `json:"qualification_policy_id"`
	Currency string `json:"currency"`
	Tiers    []Tier `json:"tiers"`
}
type Rule struct {
	Definition   Definition `json:"definition"`
	Hash         string     `json:"config_hash"`
	Creator      string     `json:"created_by"`
	Status       string     `json:"status"`
	CreatedAt    time.Time  `json:"created_at"`
	ActivatedAt  *time.Time `json:"activated_at"`
	Reviewer     string     `json:"reviewed_by,omitempty"`
	ReviewReason string     `json:"review_reason,omitempty"`
	ReviewedAt   *time.Time `json:"reviewed_at"`
}

func Normalize(d *Definition) error {
	if !foundation.ValidID(d.ID) || !foundation.ValidID(d.Name) || !foundation.ValidID(d.PlanID) || !foundation.ValidID(d.Source) || !foundation.ValidID(d.PolicyID) || !foundation.ValidID(d.Currency) || len(d.Tiers) == 0 || len(d.Tiers) > 64 {
		return foundation.ErrValidation
	}
	seen := map[string]bool{}
	thresholds := map[int]bool{}
	for _, t := range d.Tiers {
		n, err := plan.Minor(t.Amount)
		if err != nil || n.Sign() <= 0 || !foundation.ValidID(t.ID) || seen[t.ID] || thresholds[t.Required] || t.Required < 1 || t.Required > 1000000000 {
			return foundation.ErrValidation
		}
		if n.String() != t.Amount {
			return foundation.ErrValidation
		}
		seen[t.ID] = true
		thresholds[t.Required] = true
	}
	sort.Slice(d.Tiers, func(i, j int) bool { return d.Tiers[i].Required < d.Tiers[j].Required })
	return nil
}
func ID(parts ...string) string {
	b, _ := json.Marshal(parts)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func Hash(d Definition) string { b, _ := json.Marshal(d); return ID(string(b)) }

// UnlockEvidence 冻结人数、周期及规范化会员集合摘要，避免每个阶梯重复存储完整大集合。
type UnlockEvidence struct {
	Window      qualification.StatisticsWindow `json:"window"`
	Count       int                            `json:"count"`
	MembersHash string                         `json:"member_set_hash"`
}
type TierState struct {
	Tier          Tier            `json:"tier"`
	Status        string          `json:"status"`
	UnlockedAt    *time.Time      `json:"unlocked_at"`
	Evidence      *UnlockEvidence `json:"unlock_evidence"`
	ClaimID       string          `json:"claim_id,omitempty"`
	EntitlementID string          `json:"entitlement_id,omitempty"`
	ClaimedAt     *time.Time      `json:"claimed_at"`
}
type Progress struct {
	RuleID      string                          `json:"rule_id"`
	AgentID     string                          `json:"agent_id"`
	DirectCount int                             `json:"direct_effective_count"`
	Statistics  *qualification.StatisticsWindow `json:"statistics,omitempty"`
	RefreshedAt *time.Time                      `json:"refreshed_at"`
	Tiers       []TierState                     `json:"tiers"`
}

// Advance 只解锁不回锁；周期重置或迟到更正不擦除解锁证据及领取记录，每阶奖励独立领取。
func Advance(r Rule, old Progress, statistics qualification.StatisticsResult, at time.Time) Progress {
	states := map[string]TierState{}
	for _, v := range old.Tiers {
		states[v.Tier.ID] = v
	}
	p := Progress{RuleID: r.Definition.ID, AgentID: old.AgentID, DirectCount: statistics.Count, Statistics: &statistics.Window, RefreshedAt: &at, Tiers: []TierState{}}
	for _, tier := range r.Definition.Tiers {
		state, ok := states[tier.ID]
		if !ok {
			state = TierState{Tier: tier, Status: "LOCKED"}
		}
		if state.UnlockedAt == nil && statistics.Count >= tier.Required {
			state.UnlockedAt = &at
			state.Status = "CLAIMABLE"
			ids := append([]string(nil), statistics.AgentIDs...)
			sort.Strings(ids)
			state.Evidence = &UnlockEvidence{Window: statistics.Window, Count: statistics.Count, MembersHash: ID(ids...)}
		}
		p.Tiers = append(p.Tiers, state)
	}
	if old.RefreshedAt != nil && old.RefreshedAt.After(at) {
		p.DirectCount = old.DirectCount
		p.Statistics = old.Statistics
		p.RefreshedAt = old.RefreshedAt
	}
	return p
}
func Empty(r Rule, agent string) Progress {
	return Advance(r, Progress{AgentID: agent}, qualification.StatisticsResult{}, time.Time{}).withoutRefresh()
}
func (p Progress) withoutRefresh() Progress   { p.RefreshedAt = nil; p.Statistics = nil; return p }
func ClaimID(rule, agent, tier string) string { return "referral_" + ID(rule, agent, tier) }
func RefreshKey(rule, agent string, at time.Time, evidence string) string {
	return ID(rule, agent, strconv.FormatInt(at.UnixMicro(), 10), evidence)
}

type Event struct {
	ID       string
	Body     []byte
	At       time.Time
	Attempts int
}

type Receipt struct {
	RuleID        string    `json:"rule_id"`
	AgentID       string    `json:"agent_id"`
	Currency      string    `json:"currency"`
	PlanVersionID string    `json:"plan_version_id"`
	ConfigHash    string    `json:"config_hash"`
	Reward        TierState `json:"reward"`
}
