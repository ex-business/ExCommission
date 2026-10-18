package affiliateprograms

import (
	"context"
	"encoding/json"
	"errors"
	affiliate "github.com/ex-business/ExCommission/internal/affiliate/domain"
	flowapp "github.com/ex-business/ExCommission/internal/affiliateflow/application"
	flow "github.com/ex-business/ExCommission/internal/affiliateflow/domain"
	identity "github.com/ex-business/ExCommission/internal/identity/domain"
	response "github.com/ex-business/ExCommission/internal/platform/application"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	approval "github.com/ex-business/ExCommission/internal/workflow/domain"
	workflow "github.com/ex-business/ExCommission/internal/workflow/ports"
	"log/slog"
	"time"
)

type Service struct {
	Flow           flowapp.Service
	Repo           Repository
	Workflow       workflow.Repository
	ID             func() string
	Hash           func([]byte) string
	ValidateConfig func(context.Context, db.Executor, foundation.Scope, affiliate.Config, time.Time) error
}

func scope(p identity.Principal) foundation.Scope {
	return foundation.Scope{Tenant: p.Tenant, App: p.App}
}

// validate 在申请和执行两次检查；站点事务锁与版本号防止两个审批覆盖彼此，未来版本禁止插队。
func (s Service) validate(ctx context.Context, tx db.Executor, sc foundation.Scope, in *Change, now time.Time) ([]Version, error) {
	if e := in.Validate(); e != nil {
		return nil, e
	}
	if in.Effective.Before(now) {
		return nil, errors.New("STATE_CONFLICT:EFFECTIVE_TIME_PASSED")
	}
	if e := s.ValidateConfig(ctx, tx, sc, in.Config, in.Effective); e != nil {
		return nil, e
	}
	all, e := s.Repo.Versions(ctx, tx, sc)
	if e != nil {
		return nil, e
	}
	var last *Version
	for i := range all {
		v := &all[i]
		if v.ID == in.ID {
			last = v
		}
	}
	if last == nil {
		if in.Expected != 0 {
			return nil, foundation.ErrConflict
		}
	} else {
		if in.Expected != last.Revision || !in.Effective.After(last.Effective) || last.Effective.After(now) {
			return nil, foundation.ErrConflict
		}
		// 同一方案的币种、日历和累计周期固定；不同口径新建独立方案，旧权益继续原币结清。
		if in.Config.Currency != last.Config.Currency || in.Config.CurrencyScale != last.Config.CurrencyScale || in.Config.Timezone != last.Config.Timezone || in.Config.GradePeriod != last.Config.GradePeriod {
			return nil, errors.New("STATE_CONFLICT:CREATE_INDEPENDENT_PROGRAM")
		}
	}
	candidate := Version{Change: *in, Revision: in.Expected + 1}
	future := append(append([]Version{}, all...), candidate)
	// 发布时保守检查人群、来源和事件交集；互斥重叠或优先级并列需先调整配置。
	boundaries := []time.Time{in.Effective}
	for _, v := range future {
		if v.Effective.After(in.Effective) {
			boundaries = append(boundaries, v.Effective)
		}
	}
	for _, at := range boundaries {
		active := Active(future, at)
		var own *Version
		for i := range active {
			if active[i].ID == in.ID {
				own = &active[i]
			}
		}
		if own == nil || own.Revision != candidate.Revision || own.State != "RUNNING" || own.Match.Mode == "STACK" {
			continue
		}
		for _, v := range active {
			if v.ID == in.ID || v.State != "RUNNING" || v.Config.Currency != in.Config.Currency || v.Match.Group != in.Match.Group {
				continue
			}
			if overlap(v.Match.Sources, in.Match.Sources) && overlap(v.Match.Kinds, in.Match.Kinds) && overlap(v.Match.Users, in.Match.Users) && (v.Match.Mode != in.Match.Mode || in.Match.Mode == "EXCLUSIVE" || v.Match.Priority == in.Match.Priority) {
				return nil, errors.New("STATE_CONFLICT:PROGRAM_MATCH_OVERLAP")
			}
		}
	}

	return all, nil
}
func overlap(a, b []string) bool {
	if len(a) == 0 || len(b) == 0 {
		return true
	}
	for _, x := range a {
		for _, y := range b {
			if x == y {
				return true
			}
		}
	}
	return false
}
func (s Service) Preview(ctx context.Context, sc foundation.Scope, in Change) (map[string]any, error) {
	_, e := s.validate(ctx, s.Flow.Query, sc, &in, s.Flow.Now().UTC())
	if e != nil {
		return nil, e
	}
	return map[string]any{"valid": true, "program_id": in.ID, "next_revision": in.Expected + 1, "state": in.State, "effective_from": in.Effective, "legacy_policy": "INDEPENDENT_STACK", "stopped_policy": "HISTORY_ONLY_NO_NEW_SETTLEMENT", "historical_recalculation": false}, nil
}
func (s Service) Request(ctx context.Context, p identity.Principal, in Change, key, rid string) (foundation.CommandResult, error) {
	if e := in.Validate(); e != nil {
		return foundation.CommandResult{}, e
	}
	ctx = foundation.WithRequest(ctx, rid, p.Subject)
	return s.Flow.Commands.Execute(ctx, scope(p), "affiliate-program-request", key, in, func(tx db.Executor) (foundation.CommandResult, error) {
		if e := s.Flow.Authorize(ctx, tx, p, "affiliate:configure"); e != nil {
			return foundation.CommandResult{}, e
		}
		if e := s.Flow.Repo.Lock(ctx, tx, scope(p)); e != nil {
			return foundation.CommandResult{}, e
		}
		if _, e := s.validate(ctx, tx, scope(p), &in, s.Flow.Now().UTC()); e != nil {
			return foundation.CommandResult{}, e
		}
		raw, _ := json.Marshal(in)
		a := approval.Request{ID: s.ID(), Type: "AFFILIATE_PROGRAM", Applicant: p.Subject, ObjectRef: in.ID, ObjectHash: s.Hash(raw), Payload: raw}
		if e := s.Workflow.Create(ctx, tx, scope(p), a, in.Reason, s.Flow.Now()); e != nil {
			return foundation.CommandResult{}, e
		}
		return response.Result(202, a.ID, rid, map[string]any{"approval_id": a.ID, "object_hash": a.ObjectHash, "revision": 0})
	})
}
func (s Service) Execute(ctx context.Context, tx db.Executor, sc foundation.Scope, a approval.Request, now time.Time) error {
	if a.Type != "AFFILIATE_PROGRAM" || s.Hash(a.Payload) != a.ObjectHash {
		return foundation.ErrValidation
	}
	var in Change
	if e := json.Unmarshal(a.Payload, &in); e != nil {
		return e
	}
	if in.ID != a.ObjectRef {
		return foundation.ErrValidation
	}
	if e := s.Flow.Repo.Lock(ctx, tx, sc); e != nil {
		return e
	}
	if _, e := s.validate(ctx, tx, sc, &in, now); e != nil {
		return e
	}
	if e := s.Repo.Save(ctx, tx, sc, Version{Change: in, Revision: in.Expected + 1, ApprovalID: a.ID, PublishedAt: now}); e != nil {
		return e
	}
	slog.InfoContext(ctx, "代理方案已审批发布，按生效时间控制启动和停止", "tenant_id", sc.Tenant, "app_id", sc.App, "program_id", in.ID, "revision", in.Expected+1, "state", in.State, "approval_id", a.ID)
	return nil
}

// Accept 只接收已经验签、通过原单幂等的事实；命中清单与资金产出同事务提交，重试不重新挑选版本。
func (s Service) Accept(ctx context.Context, tx db.Executor, sc foundation.Scope, ev flow.Event, user string, now time.Time) error {
	versions, e := s.Repo.Versions(ctx, tx, sc)
	if e != nil {
		return e
	}
	selected, decisions, e := Select(Active(versions, ev.At), ev, user)
	if e != nil {
		return e
	}
	live := []Version{}
	for _, v := range selected {
		if !CanProcess(versions, v, now) {
			for i := range decisions {
				if decisions[i].ProgramID == v.ID {
					decisions[i].Selected = false
					decisions[i].Reason = "STOPPED_OR_SUPERSEDED_NO_BACKFILL"
				}
			}
			continue
		}
		live = append(live, v)
	}
	if e = s.Repo.Record(ctx, tx, sc, ev, user, live, decisions, now); e != nil {
		return e
	}
	for _, v := range live {
		if e = s.refresh(ctx, tx, sc, v, now); e != nil {
			return e
		}
	}
	return nil
}
func (s Service) refresh(ctx context.Context, tx db.Executor, sc foundation.Scope, v Version, now time.Time) error {
	facts, e := s.Repo.Facts(ctx, tx, sc, v, now.Add(time.Microsecond))
	if e != nil {
		return e
	}
	members, e := s.Flow.Members.List(ctx, tx, sc, now)
	if e != nil {
		return e
	}
	for _, m := range members {
		gate, e := s.Flow.Repo.Gate(ctx, tx, sc, m.UserID, m.RegisteredAt)
		if e != nil {
			return e
		}
		if gate.Since.Before(v.Effective) {
			gate.Since = v.Effective
		}
		prior, e := s.Flow.Repo.Rewards(ctx, tx, sc, m.UserID)
		if e != nil {
			return e
		}
		_, created, e := flow.Evaluate(ConfigVersion(v), m, members, facts, gate, prior, now)
		if e != nil {
			return e
		}
		if !gate.Enabled {
			continue
		}
		if e = s.Flow.Members.Allowed(ctx, tx, sc, m.AgentID, now); e != nil {
			if errors.Is(e, foundation.ErrConflict) {
				continue
			}
			return e
		}
		if s.Flow.CanReward != nil {
			if e = s.Flow.CanReward(ctx, tx, sc); e != nil {
				if e.Error() == "SITE_AGENT_DISABLED" {
					continue
				}
				return e
			}
		}
		for _, r := range created {
			snapshot := ConfigVersion(v)
			r.ProgramID = v.ID
			r.ProgramConfig = &snapshot
			r.EntitlementID, e = s.Flow.Funds.Unlock(ctx, tx, sc, r, now)
			if e != nil {
				return e
			}
			if e = s.Flow.Repo.SaveReward(ctx, tx, sc, r); e != nil {
				return e
			}
		}
	}
	return nil
}

// ValidateEvent 仅在旧方案币种校验不适用时检查已运行的新方案；不跳过事实字段、金额或实际业务时间验证。
func (s Service) ValidateEvent(ctx context.Context, tx db.Executor, sc foundation.Scope, ev flow.Event, now time.Time) error {
	all, e := s.Repo.Versions(ctx, tx, sc)
	if e != nil {
		return e
	}
	for _, v := range Active(all, ev.At) {
		if v.State == "RUNNING" && ev.Currency == v.Config.Currency {
			if e = ev.Validate(v.Config, now); e == nil {
				return nil
			}
		}
	}
	return foundation.ErrValidation
}
