// Package application 编排独立推荐奖励规则、进度、解锁及原子领取，不授予代理资格。
package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	agents "github.com/ex-business/ExCommission/internal/agents/ports"
	identity "github.com/ex-business/ExCommission/internal/identity/domain"
	money "github.com/ex-business/ExCommission/internal/ledger/domain"
	plan "github.com/ex-business/ExCommission/internal/plans/domain"
	plans "github.com/ex-business/ExCommission/internal/plans/ports"
	response "github.com/ex-business/ExCommission/internal/platform/application"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	qmodel "github.com/ex-business/ExCommission/internal/qualification/domain"
	qualification "github.com/ex-business/ExCommission/internal/qualification/ports"
	model "github.com/ex-business/ExCommission/internal/referral/domain"
	"github.com/ex-business/ExCommission/internal/referral/ports"
	"log/slog"
	"time"
)

type Service struct {
	Inbox       ports.Inbox
	Commands    db.Commands
	Query       db.Executor
	Repo        ports.Repository
	Members     ports.Members
	Policies    qualification.Repository
	Plans       plans.Repository
	Accounts    agents.HistoricalAccounts
	Subjects    agents.GradeSubjects
	Eligibility ports.Eligibility
	Contracts   agents.Contracts
	Ledger      ports.Ledger
	Catalog     plans.CurrencyCatalog
	Now         func() time.Time
}

func scope(p identity.Principal) foundation.Scope {
	return foundation.Scope{Tenant: p.Tenant, App: p.App}
}
func (s Service) validate(ctx context.Context, tx db.Executor, sc foundation.Scope, d model.Definition, at time.Time) (plan.Version, error) {
	policy, err := s.Policies.Policy(ctx, tx, sc, d.PolicyID)
	if errors.Is(err, sql.ErrNoRows) {
		return plan.Version{}, errors.New("UNKNOWN_FACT")
	}
	if err != nil {
		return plan.Version{}, err
	}
	if policy.Source != d.Source || policy.Currency != d.Currency {
		return plan.Version{}, foundation.ErrValidation
	}
	v, err := s.Plans.AtPlan(ctx, tx, sc, d.PlanID, at)
	if err != nil {
		return v, err
	}
	if v.Config.Currency != d.Currency {
		return v, foundation.ErrValidation
	}
	if s.Catalog != nil {
		if _, _, err = s.Catalog.CurrencyAt(ctx, tx, sc, d.Currency, at); err != nil {
			return v, err
		}
	}
	return v, nil
}
func (s Service) Create(ctx context.Context, p identity.Principal, input model.Definition, key, request string) (foundation.CommandResult, error) {
	if err := model.Normalize(&input); err != nil {
		return foundation.CommandResult{}, err
	}
	ctx = foundation.WithRequest(ctx, request, p.Subject)
	sc := scope(p)
	created := false
	result, err := s.Commands.Execute(ctx, sc, "referral-rule-create", key, input, func(tx db.Executor) (foundation.CommandResult, error) {
		old, err := s.Repo.Rule(ctx, tx, sc, input.ID, true)
		if err == nil {
			if old.Hash != model.Hash(input) {
				return foundation.CommandResult{}, foundation.ErrConflict
			}
			return response.Result(200, input.ID, request, old)
		}
		if err.Error() != "NOT_FOUND" {
			return foundation.CommandResult{}, err
		}
		if _, err = s.validate(ctx, tx, sc, input, s.Now()); err != nil {
			return foundation.CommandResult{}, err
		}
		rule := model.Rule{Definition: input, Hash: model.Hash(input), Creator: p.Subject, Status: "PENDING_APPROVAL", CreatedAt: s.Now()}
		if err = s.Repo.Create(ctx, tx, sc, rule); err != nil {
			return foundation.CommandResult{}, err
		}
		created = true
		return response.Result(200, input.ID, request, rule)
	})
	if err == nil && result.Status == 200 && created && !result.Replayed {
		slog.InfoContext(ctx, "推荐奖励规则已创建，等待独立复核", "request_id", request, "rule_id", input.ID, "plan_id", input.PlanID, "status", "PENDING_APPROVAL")
	}
	return result, err
}

type Activation struct {
	Hash     string `json:"expected_config_hash"`
	Decision string `json:"decision"`
	Reason   string `json:"reason"`
}

func (s Service) Activate(ctx context.Context, p identity.Principal, ruleID string, input Activation, key, request string) (foundation.CommandResult, error) {
	if !foundation.ValidID(ruleID) || !foundation.ValidID(input.Hash) || !foundation.ValidID(input.Reason) || (input.Decision != "APPROVE" && input.Decision != "REJECT") {
		return foundation.CommandResult{}, foundation.ErrValidation
	}
	sc := scope(p)
	ctx = foundation.WithRequest(ctx, request, p.Subject)
	payload := struct {
		Rule     string
		Decision Activation
	}{ruleID, input}
	result, err := s.Commands.Execute(ctx, sc, "referral-rule-activate", key, payload, func(tx db.Executor) (foundation.CommandResult, error) {
		rule, err := s.Repo.Rule(ctx, tx, sc, ruleID, true)
		if err != nil {
			return foundation.CommandResult{}, err
		}
		if rule.Creator == p.Subject {
			return foundation.CommandResult{}, errors.New("SELF_APPROVAL_FORBIDDEN")
		}
		status := "REJECTED"
		if input.Decision == "APPROVE" {
			if _, err = s.validate(ctx, tx, sc, rule.Definition, s.Now()); err != nil {
				return foundation.CommandResult{}, err
			}
			status = "ACTIVE"
		}
		if err = s.Repo.Activate(ctx, tx, sc, ruleID, input.Hash, status, p.Subject, input.Reason, s.Now()); err != nil {
			return foundation.CommandResult{}, err
		}
		rule, err = s.Repo.Rule(ctx, tx, sc, ruleID, true)
		if err != nil {
			return foundation.CommandResult{}, err
		}
		return response.Result(200, ruleID, request, rule)
	})
	if err == nil && result.Status == 200 && !result.Replayed {
		slog.InfoContext(ctx, "推荐奖励规则复核已完成", "request_id", request, "rule_id", ruleID, "decision", input.Decision, "reviewer", p.Subject, "result", "SUCCESS")
	}
	return result, err
}
func (s Service) GetRule(ctx context.Context, p identity.Principal, id string) (model.Rule, error) {
	if !foundation.ValidID(id) {
		return model.Rule{}, foundation.ErrValidation
	}
	return s.Repo.Rule(ctx, s.Query, scope(p), id, false)
}
func (s Service) View(ctx context.Context, p identity.Principal, ruleID, agentID string) (model.Progress, error) {
	if !foundation.ValidID(ruleID) || !foundation.ValidID(agentID) {
		return model.Progress{}, foundation.ErrValidation
	}
	sc := scope(p)
	rule, err := s.Repo.Rule(ctx, s.Query, sc, ruleID, false)
	if err != nil {
		return model.Progress{}, err
	}
	if _, err = s.Accounts.AccountAt(ctx, s.Query, sc, agentID, s.Now()); err != nil {
		return model.Progress{}, err
	}
	progress, err := s.Repo.Progress(ctx, s.Query, sc, ruleID, agentID)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Empty(rule, agentID), nil
	}
	return progress, err
}
func (s Service) refreshLocked(ctx context.Context, tx db.Executor, sc foundation.Scope, r model.Rule, agent string, at time.Time) (model.Progress, error) {
	if r.Status != "ACTIVE" || r.ActivatedAt == nil || at.Before(*r.ActivatedAt) {
		return model.Progress{}, errors.New("STATE_CONFLICT")
	}
	if _, err := s.Accounts.AccountAt(ctx, tx, sc, agent, at); err != nil {
		return model.Progress{}, err
	}
	old, err := s.Repo.Progress(ctx, tx, sc, r.Definition.ID, agent)
	if errors.Is(err, sql.ErrNoRows) {
		old = model.Empty(r, agent)
	} else if err != nil {
		return old, err
	}
	// 内部截止点前推一个存储精度单位，包含恰在当前时点完成达标的事实；周期边界仍按事实所在当地日定位。
	stats, err := s.Members.DirectStatisticsAt(ctx, tx, sc, r.Definition.PolicyID, r.Definition.Source, agent, at.Add(time.Microsecond))
	if err != nil {
		return old, err
	}
	current := model.Advance(r, old, stats, at)
	if err = s.Repo.SaveProgress(ctx, tx, sc, current); err != nil {
		return current, err
	}
	return current, nil
}
func (s Service) Refresh(ctx context.Context, p identity.Principal, ruleID, agent, key, request string) (foundation.CommandResult, error) {
	return s.refresh(ctx, p, ruleID, agent, s.Now(), key, request)
}
func (s Service) refresh(ctx context.Context, p identity.Principal, ruleID, agent string, at time.Time, key, request string) (foundation.CommandResult, error) {
	if !foundation.ValidID(ruleID) || !foundation.ValidID(agent) {
		return foundation.CommandResult{}, foundation.ErrValidation
	}
	sc := scope(p)
	ctx = foundation.WithRequest(ctx, request, p.Subject)
	// 将观察时点冻结在操作内容中；系统事件重投使用固定事件时间，公共调用同键由Lookup先返回原响应。
	input := struct{ RuleID, AgentID string }{ruleID, agent}
	return s.Commands.Execute(ctx, sc, "referral-progress-refresh", key, input, func(tx db.Executor) (foundation.CommandResult, error) {
		rule, err := s.Repo.Rule(ctx, tx, sc, ruleID, false)
		if err != nil {
			return foundation.CommandResult{}, err
		}
		if err = s.Repo.LockAgent(ctx, tx, sc, ruleID, agent); err != nil {
			return foundation.CommandResult{}, err
		}
		progress, err := s.refreshLocked(ctx, tx, sc, rule, agent, at)
		if err != nil {
			return foundation.CommandResult{}, err
		}
		return response.Result(200, agent, request, progress)
	})
}

type Claim struct {
	RuleID  string `json:"rule_id"`
	AgentID string `json:"agent_id"`
	TierID  string `json:"tier_id"`
}

func (s Service) Claim(ctx context.Context, p identity.Principal, input Claim, key, request string) (foundation.CommandResult, error) {
	if !foundation.ValidID(input.RuleID) || !foundation.ValidID(input.AgentID) || !foundation.ValidID(input.TierID) {
		return foundation.CommandResult{}, foundation.ErrValidation
	}
	sc := scope(p)
	ctx = foundation.WithRequest(ctx, request, p.Subject)
	posted := false
	result, err := s.Commands.Execute(ctx, sc, "referral-reward-claim", key, input, func(tx db.Executor) (foundation.CommandResult, error) {
		rule, err := s.Repo.Rule(ctx, tx, sc, input.RuleID, false)
		if err != nil {
			return foundation.CommandResult{}, err
		}
		if err = s.Repo.LockAgent(ctx, tx, sc, input.RuleID, input.AgentID); err != nil {
			return foundation.CommandResult{}, err
		}
		// 相同业务阶梯换投递键或跨周期也返回原凭证，不依赖可刷新的进度缓存判定是否已领。
		prior, err := s.Repo.Claim(ctx, tx, sc, input.RuleID, input.AgentID, input.TierID)
		if err == nil {
			return response.Result(200, prior.Reward.ClaimID, request, prior.Reward)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return foundation.CommandResult{}, err
		}
		tierExists := false
		for _, tier := range rule.Definition.Tiers {
			if tier.ID == input.TierID {
				tierExists = true
			}
		}
		if !tierExists {
			return foundation.CommandResult{}, errors.New("NOT_FOUND")
		}
		now := s.Now()
		version, err := s.validate(ctx, tx, sc, rule.Definition, now)
		if err != nil {
			return foundation.CommandResult{}, err
		}
		included := version.Config.Scope.Type == "ALL_ACTIVE_AGENTS"
		for _, id := range version.Config.Scope.AgentIDs {
			if id == input.AgentID {
				included = true
			}
		}
		if !included {
			return foundation.CommandResult{}, errors.New("PERMISSION_DENIED")
		}
		if err = s.Eligibility.RewardEligible(ctx, tx, sc, input.AgentID, rule.Definition.PlanID, now); err != nil {
			return foundation.CommandResult{}, err
		}
		if version.Config.AgentPolicy.RequireContract {
			if _, err = s.Contracts.ContractAt(ctx, tx, sc, input.AgentID, rule.Definition.PlanID, now); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					err = errors.New("EVIDENCE_NOT_CONFIRMED")
				}
				return foundation.CommandResult{}, err
			}
		}
		progress, err := s.refreshLocked(ctx, tx, sc, rule, input.AgentID, now)
		if err != nil {
			return foundation.CommandResult{}, err
		}
		for i := range progress.Tiers {
			tier := &progress.Tiers[i]
			if tier.Tier.ID != input.TierID {
				continue
			}
			if tier.UnlockedAt == nil {
				return foundation.CommandResult{}, errors.New("EVIDENCE_NOT_CONFIRMED")
			}
			claimID := model.ClaimID(input.RuleID, input.AgentID, input.TierID)
			id, err := s.Ledger.CreditReward(ctx, tx, sc, money.RewardCredit{ClaimID: claimID, RuleID: input.RuleID, TierID: input.TierID, AgentID: input.AgentID, SourceID: rule.Definition.Source, PlanVersionID: version.ID, Currency: rule.Definition.Currency, Amount: tier.Tier.Amount, Settlement: version.Config.Settlement, BusinessTime: now}, now)
			if err != nil {
				return foundation.CommandResult{}, err
			}
			tier.Status = "CLAIMED"
			tier.ClaimID = claimID
			tier.EntitlementID = id
			tier.ClaimedAt = &now
			// 账簿、领取标识与进度处于同一事务；任何写入失败全部回滚，不产生已领未到账或重复发奖。
			receipt := model.Receipt{RuleID: input.RuleID, AgentID: input.AgentID, Currency: rule.Definition.Currency, PlanVersionID: version.ID, ConfigHash: rule.Hash, Reward: *tier}
			if err = s.Repo.SaveClaim(ctx, tx, sc, receipt, now); err != nil {
				return foundation.CommandResult{}, err
			}
			if err = s.Repo.SaveProgress(ctx, tx, sc, progress); err != nil {
				return foundation.CommandResult{}, err
			}
			posted = true
			return response.Result(200, claimID, request, *tier)
		}
		return foundation.CommandResult{}, errors.New("NOT_FOUND")
	})
	if err == nil && result.Status == 200 && posted && !result.Replayed {
		slog.InfoContext(ctx, "推荐奖励领取已入待结算账簿", "request_id", request, "rule_id", input.RuleID, "agent_id", input.AgentID, "tier_id", input.TierID, "claim_id", result.ResourceID, "result", "SUCCESS")
	}
	return result, err
}

// Observe 复用有效会员事务outbox；即使跨周期后才投递，也按事件处理时点解锁，不漏掉上一周期已达标阶梯。
func (s Service) Observe(ctx context.Context, sc foundation.Scope, eventID string, raw []byte, at time.Time) error {
	var body struct {
		Qualification qmodel.Status `json:"qualification"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return err
	}
	state := body.Qualification
	if !state.Effective || state.ParentID == nil {
		return nil
	}
	rules, err := s.Repo.ActiveRules(ctx, s.Query, sc)
	if err != nil {
		return err
	}
	p := identity.Principal{Tenant: sc.Tenant, App: sc.App, Subject: "system-referral"}
	for _, r := range rules {
		if r.Definition.PolicyID != state.PolicyID || r.Definition.Source != state.Source || r.ActivatedAt == nil || r.ActivatedAt.After(at) {
			continue
		}
		result, err := s.refresh(ctx, p, r.Definition.ID, *state.ParentID, at, model.RefreshKey(r.Definition.ID, *state.ParentID, at, eventID), eventID)
		if err != nil {
			return err
		}
		if result.Status >= 400 {
			return errors.New("DEPENDENCY_UNAVAILABLE")
		}
	}
	return nil
}

// RefreshAll 捕获启用前已有会员及当前进度；领取与显式刷新也会即时重算，后台延迟不授予额外金额。
func (s Service) RefreshAll(ctx context.Context, sc foundation.Scope) error {
	now := s.Now()
	rules, err := s.Repo.ActiveRules(ctx, s.Query, sc)
	if err != nil {
		return err
	}
	ids, err := s.Subjects.SubjectsAt(ctx, s.Query, sc, now)
	if err != nil {
		return err
	}
	p := identity.Principal{Tenant: sc.Tenant, App: sc.App, Subject: "system-referral"}
	for _, r := range rules {
		for _, agent := range ids {
			times := []time.Time{now}
			if _, e := s.Accounts.AccountAt(ctx, s.Query, sc, agent, *r.ActivatedAt); e == nil {
				times = append([]time.Time{*r.ActivatedAt}, times...)
			} else if e.Error() != "RELATION_HISTORY_MISSING" && e.Error() != "NOT_FOUND" {
				return e
			}
			for _, at := range times {
				// 同一分钟的进度轮询复用操作键；首次启用时点单独回补，避免日界线附近启用漏解锁。
				bucket := at.Truncate(time.Minute)
				if at.Equal(*r.ActivatedAt) {
					bucket = at
				}
				result, err := s.refresh(ctx, p, r.Definition.ID, agent, at, model.RefreshKey(r.Definition.ID, agent, bucket, "timer"), "referral-timer")
				if err != nil {
					return err
				}
				if result.Status >= 400 && result.Status != 404 {
					return errors.New("DEPENDENCY_UNAVAILABLE")
				}
			}
		}
	}
	return nil
}

// Drain 对每个事件独立防重与退避；坏事件留在本模块重试，不拖住其他业务outbox。
func (s Service) Drain(ctx context.Context, sc foundation.Scope) error {
	events, err := s.Inbox.Pending(ctx, s.Query, sc, s.Now())
	if err != nil {
		return err
	}
	for _, event := range events {
		if err = s.Observe(ctx, sc, event.ID, event.Body, event.At); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if failure := s.Inbox.Failed(ctx, s.Query, sc, event, s.Now(), err.Error()); failure != nil {
				return failure
			}
			if event.Attempts == 0 {
				slog.ErrorContext(ctx, "推荐奖励事件处理失败，已保留重试", "event_id", event.ID, "tenant_id", sc.Tenant, "app_id", sc.App, "reason", err.Error())
			}
			continue
		}
		if err = s.Inbox.Complete(ctx, s.Query, sc, event.ID); err != nil {
			return err
		}
	}
	return nil
}

func (s Service) GetClaim(ctx context.Context, p identity.Principal, id string) (model.Receipt, error) {
	if !foundation.ValidID(id) {
		return model.Receipt{}, foundation.ErrValidation
	}
	out, err := s.Repo.GetClaim(ctx, s.Query, scope(p), id)
	if errors.Is(err, sql.ErrNoRows) {
		err = errors.New("NOT_FOUND")
	}
	return out, err
}
