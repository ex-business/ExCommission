// Package application 编排有效会员事实与累计状态；不修改资金账簿、现有计佣或等级审批。
package application

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	principal "github.com/ex-business/ExCommission/internal/identity/domain"
	response "github.com/ex-business/ExCommission/internal/platform/application"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	model "github.com/ex-business/ExCommission/internal/qualification/domain"
	"github.com/ex-business/ExCommission/internal/qualification/ports"
	"log/slog"
	"sort"
	"time"
)

type Service struct {
	Commands   db.Commands
	Query      db.Executor
	Repo       ports.Repository
	Members    ports.Members
	Sources    map[string]ports.Source
	Currencies map[string]int
	Now        func() time.Time
}

func (s Service) CreatePolicy(ctx context.Context, p principal.Principal, input model.Policy, key, request string) (foundation.CommandResult, error) {
	ctx = foundation.WithRequest(ctx, request, p.Subject)
	if !foundation.ValidID(input.ID) || s.Sources[input.Source] == nil {
		return foundation.CommandResult{}, foundation.ErrValidation
	}
	if _, ok := s.Currencies[input.Currency]; !ok {
		return foundation.CommandResult{}, foundation.ErrValidation
	}
	if err := model.ValidatePolicy(input); err != nil {
		return foundation.CommandResult{}, err
	}
	input.Venues = append([]string(nil), input.Venues...)
	sort.Strings(input.Venues)
	scope := foundation.Scope{Tenant: p.Tenant, App: p.App}
	return s.Commands.Execute(ctx, scope, "qualification-policy-create", key, input, func(tx db.Executor) (foundation.CommandResult, error) {
		// 同业务编号同配置可重投；改变门槛或场馆须创建新编号，禁止重写历史达标口径。
		existing, err := s.Repo.Policy(ctx, tx, scope, input.ID)
		if err == nil {
			a, _ := json.Marshal(existing)
			b, _ := json.Marshal(input)
			if string(a) != string(b) {
				return foundation.CommandResult{}, foundation.ErrConflict
			}
			return response.Result(200, input.ID, request, existing)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return foundation.CommandResult{}, err
		}
		if err = s.Repo.SavePolicy(ctx, tx, scope, input, s.Now()); err != nil {
			return foundation.CommandResult{}, err
		}
		return response.Result(200, input.ID, request, input)
	})
}
func (s Service) Get(ctx context.Context, p principal.Principal, policy, source, external string) (model.Status, error) {
	if !foundation.ValidID(policy) || !foundation.ValidID(source) || !foundation.ValidID(external) {
		return model.Status{}, foundation.ErrValidation
	}
	scope := foundation.Scope{Tenant: p.Tenant, App: p.App}
	rules, err := s.Repo.Policy(ctx, s.Query, scope, policy)
	if err != nil {
		return model.Status{}, err
	}
	if rules.Source != source {
		return model.Status{}, foundation.ErrValidation
	}
	member, err := s.Members.Registered(ctx, s.Query, scope, source, external)
	if err != nil {
		return model.Status{}, err
	}
	state, err := s.Repo.Status(ctx, s.Query, scope, policy, source, external)
	if errors.Is(err, sql.ErrNoRows) || err != nil && err.Error() == "NOT_FOUND" {
		state, err = model.Evaluate(rules, member, nil)
		state.ExternalUser = external
	}
	return state, err
}
func (s Service) Receive(ctx context.Context, p principal.Principal, raw []byte, signature, key, request string) (foundation.CommandResult, error) {
	ctx = foundation.WithRequest(ctx, request, p.Subject)
	scope := foundation.Scope{Tenant: p.Tenant, App: p.App}
	var selector struct {
		Source string `json:"source_id"`
	}
	if json.Unmarshal(raw, &selector) != nil {
		return foundation.CommandResult{}, foundation.ErrValidation
	}
	source := s.Sources[selector.Source]
	if source == nil {
		return foundation.CommandResult{}, errors.New("PERMISSION_DENIED")
	}
	fact, err := source.Verify(ctx, scope, raw, signature)
	if err != nil {
		return foundation.CommandResult{}, err
	}
	if fact.Schema != "1" || fact.Source != selector.Source || !foundation.ValidID(fact.PolicyID) || !foundation.ValidID(fact.EventID) || !foundation.ValidID(fact.BusinessID) || !foundation.ValidID(fact.ExternalUser) || fact.At.Year() < 1000 || fact.At.After(s.Now()) || fact.At.Nanosecond()%1000 != 0 {
		return foundation.CommandResult{}, foundation.ErrValidation
	}
	if _, err = model.Amount(fact.Amount); err != nil {
		return foundation.CommandResult{}, err
	}
	fact.At = fact.At.UTC()
	identity := fact
	identity.EventID = ""
	body, _ := json.Marshal(identity)
	sum := sha256.Sum256(body)
	hash := hex.EncodeToString(sum[:])
	inserted := false
	becameEffective := false
	result, err := s.Commands.Execute(ctx, scope, "qualification-fact-receive", key, fact, func(tx db.Executor) (foundation.CommandResult, error) {
		policy, err := s.Repo.Policy(ctx, tx, scope, fact.PolicyID)
		if err != nil {
			return foundation.CommandResult{}, err
		}
		if policy.Source != fact.Source || policy.Currency != fact.Currency {
			return foundation.CommandResult{}, foundation.ErrValidation
		}
		member, err := s.Members.Registered(ctx, tx, scope, fact.Source, fact.ExternalUser)
		if err != nil {
			return foundation.CommandResult{}, err
		}
		// 注册历史不存在时明确失败；不制造会员、不猜测邀请归属。后续来源适配器负责依赖顺序。
		if fact.At.Before(member.RegisteredAt) {
			return foundation.CommandResult{}, foundation.ErrValidation
		}
		if err = s.Repo.LockMember(ctx, tx, scope, fact.PolicyID, fact.Source, fact.ExternalUser); err != nil {
			return foundation.CommandResult{}, err
		}
		previous, err := s.Repo.Status(ctx, tx, scope, fact.PolicyID, fact.Source, fact.ExternalUser)
		if err != nil && !errors.Is(err, sql.ErrNoRows) && err.Error() != "NOT_FOUND" {
			return foundation.CommandResult{}, err
		}
		inserted, err = s.Repo.ClaimFact(ctx, tx, scope, fact, hash)
		if err != nil {
			return foundation.CommandResult{}, err
		}
		if !inserted {
			return response.Result(200, member.AgentID, request, previous)
		}
		facts, err := s.Repo.Facts(ctx, tx, scope, fact.PolicyID, fact.Source, fact.ExternalUser)
		if err != nil {
			return foundation.CommandResult{}, err
		}
		current, err := model.Evaluate(policy, member, facts)
		if err != nil {
			return foundation.CommandResult{}, err
		}
		becameEffective = current.Effective && !previous.Effective
		current.ExternalUser = fact.ExternalUser
		current.Revision = previous.Revision + 1
		current.FirstObservedAt = previous.FirstObservedAt
		now := s.Now().UTC().Truncate(time.Microsecond)
		if current.Effective && current.FirstObservedAt == nil {
			current.FirstObservedAt = &now
		}
		// 新事实、累计、达标时点、修订及outbox同事务提交；重复订单不推进修订或重复发布。
		if err = s.Repo.SaveStatus(ctx, tx, scope, current, now); err != nil {
			return foundation.CommandResult{}, err
		}
		return response.Result(200, member.AgentID, request, current)
	})
	if err == nil && result.Status == 200 && !result.Replayed && inserted {
		slog.Debug("有效会员事实累计完成", "request_id", request, "source_id", fact.Source, "event_id", fact.EventID, "business_id", fact.BusinessID, "agent_id", result.ResourceID, "result", "SUCCEEDED")
	}
	if err == nil && result.Status == 200 && !result.Replayed && becameEffective {
		slog.Info("受邀会员首次达标", "request_id", request, "policy_id", fact.PolicyID, "source_id", fact.Source, "business_id", fact.BusinessID, "agent_id", result.ResourceID, "result", "EFFECTIVE")
	}
	return result, err
}
