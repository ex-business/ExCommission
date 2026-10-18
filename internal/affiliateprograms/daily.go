package affiliateprograms

import (
	"context"
	"errors"
	affiliate "github.com/ex-business/ExCommission/internal/affiliate/domain"
	flow "github.com/ex-business/ExCommission/internal/affiliateflow/domain"
	tasks "github.com/ex-business/ExCommission/internal/affiliatetasks"
	response "github.com/ex-business/ExCommission/internal/platform/application"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"sort"
	"time"
)

// complete 按旧来源日历逐段证明新方案整个业务日已关闭，跨时区日不能仅凭同名日期提前结算。
func (s Service) complete(ctx context.Context, tx db.Executor, sc foundation.Scope, v Version, start, end time.Time) (bool, error) {
	missing, configured, e := s.MissingWatermarks(ctx, tx, sc, v, start, end)
	return configured && len(missing) == 0, e
}

func (s Service) calculate(ctx context.Context, tx db.Executor, sc foundation.Scope, v Version, date time.Time, frozen []flow.FrozenCommission) ([]flow.FrozenCommission, error) {
	_, end := v.Config.Day(date)
	facts, e := s.Repo.Facts(ctx, tx, sc, v, end)
	if e != nil {
		return nil, e
	}
	members, e := s.Flow.Members.List(ctx, tx, sc, end.Add(-time.Microsecond))
	if e != nil {
		return nil, e
	}
	regs := []affiliate.Registration{}
	for index, m := range members {
		if frozen == nil && index%50 == 0 {
			tasks.Progress(ctx, "CALCULATING", index, len(members))
		}
		regs = append(regs, affiliate.Registration{UserID: m.UserID, ParentID: m.ParentUser, At: m.RegisteredAt})
	}
	if s.Flow.Relations != nil {
		regs, e = s.Flow.Relations.Close(ctx, tx, sc, end.Add(-time.Microsecond))
		if e != nil {
			return nil, e
		}
	}
	var ownership []affiliate.Attribution
	if s.Flow.Relations != nil {
		if deep, ok := s.Flow.Relations.(interface {
			AttributionsAtDepth(context.Context, db.Executor, foundation.Scope, []affiliate.Fact, time.Time, time.Time, int) ([]affiliate.Attribution, error)
		}); ok {
			ownership, e = deep.AttributionsAtDepth(ctx, tx, sc, facts, time.Time{}, end, v.Config.DepthLimit())
		} else {
			ownership, e = s.Flow.Relations.Attributions(ctx, tx, sc, facts, time.Time{}, end)
		}
		if e != nil {
			return nil, e
		}
	}
	original := map[string]flow.FrozenCommission{}
	for _, f := range frozen {
		original[f.Result.UserID] = f
	}
	out := []flow.FrozenCommission{}
	for _, m := range members {
		gate, e := s.Flow.Repo.Gate(ctx, tx, sc, m.UserID, m.RegisteredAt)
		if e != nil {
			return nil, e
		}
		if gate.Since.Before(v.Effective) {
			gate.Since = v.Effective
		}
		if old, ok := original[m.UserID]; ok {
			gate.Since = old.Since
			gate.Enabled = old.Eligible
		} else if frozen != nil {
			continue
		}
		selected := []affiliate.Fact{}
		for _, f := range facts {
			if !f.At.Before(gate.Since) {
				selected = append(selected, f)
			}
		}
		result, e := affiliate.CalculateRiskDay(v.Config, m.UserID, date, regs, selected, ownership, facts)
		if e != nil {
			return nil, e
		}
		eligible := gate.Enabled
		if frozen == nil {
			if e = s.Flow.Members.Allowed(ctx, tx, sc, m.AgentID, s.Flow.Now()); e != nil {
				if !errors.Is(e, foundation.ErrConflict) {
					return nil, e
				}
				eligible = false
			}
			if s.Flow.CanReward != nil {
				if e = s.Flow.CanReward(ctx, tx, sc); e != nil {
					if e.Error() != "SITE_AGENT_DISABLED" {
						return nil, e
					}
					eligible = false
				}
			}
		}
		out = append(out, flow.FrozenCommission{Since: gate.Since, AgentID: m.AgentID, Eligible: eligible, Result: result})
	}
	return out, nil
}

// RunOnce 只为当前启动版本新增结算；停止或被替换版本保留事实和历史，恢复后不自动补结算旧区间。
func (s Service) RunOnce(ctx context.Context, sc foundation.Scope) error {
	all, e := s.Repo.Versions(ctx, s.Flow.Query, sc)
	if e != nil {
		return e
	}
	now := s.Flow.Now().UTC()
	for _, v := range all {
		// 已停止版本也发现历史任务用于只读追踪；是否允许执行始终由实时检查和原事务决定。
		if v.State != "RUNNING" || (s.Flow.TaskRun == nil && !CanProcess(all, v, now)) {
			continue
		}
		days, e := s.TaskDays(ctx, sc, v, all, now)
		if e != nil {
			return e
		}
		for _, day := range days {
			date, e := time.ParseInLocation("2006-01-02", day, v.Config.Location())
			if e != nil {
				return e
			}
			if s.Flow.TaskRun != nil {
				key := TaskKey(v, date)
				if e = s.Flow.TaskRun(ctx, sc, key, func(c context.Context) (tasks.Report, error) { return s.InspectTaskDay(c, sc, v, day) }, func(c context.Context) error { return s.ExecuteTaskDay(c, sc, v, day) }); e != nil {
					return e
				}
			} else if !date.After(v.Config.DueDate(now)) {
				if e = s.ExecuteTaskDay(ctx, sc, v, day); e != nil {
					return e
				}
			}
		}
	}
	return nil
}
func (s Service) ExecuteTaskDay(ctx context.Context, sc foundation.Scope, v Version, day string) error {
	date, e := time.ParseInLocation("2006-01-02", day, v.Config.Location())
	if e != nil {
		return e
	}
	now := s.Flow.Now().UTC()
	key := flow.ID(v.ID, stepString(int(v.Revision)), day)
	commandCtx := foundation.WithRequest(ctx, key, "system-affiliate-program-dayend")
	out, e := s.Flow.Commands.Execute(commandCtx, sc, "affiliate-program-day", key, map[string]any{"program_id": v.ID, "revision": v.Revision, "day": day}, func(tx db.Executor) (foundation.CommandResult, error) {
		if e := s.Flow.Repo.Lock(ctx, tx, sc); e != nil {
			return foundation.CommandResult{}, e
		}
		if date.After(v.Config.DueDate(s.Flow.Now())) {
			return foundation.CommandResult{}, errNotDue
		}
		latest, e := s.Repo.Versions(ctx, tx, sc)
		if e != nil {
			return foundation.CommandResult{}, e
		}
		if !CanProcess(latest, v, s.Flow.Now()) {
			return foundation.CommandResult{}, errNotRunning
		}
		start, end := v.Config.Day(date)
		ok, e := s.complete(ctx, tx, sc, v, start, end)
		if e != nil {
			return foundation.CommandResult{}, e
		}
		if !ok {
			return foundation.CommandResult{}, errIncomplete
		}
		if s.Flow.TaskRun != nil && s.Flow.CanReward != nil {
			if e := s.Flow.CanReward(ctx, tx, sc); e != nil {
				return foundation.CommandResult{}, e
			}
		}
		tasks.Progress(ctx, "CALCULATING", 0, 0)
		results, e := s.calculate(ctx, tx, sc, v, date, nil)
		if e != nil {
			return foundation.CommandResult{}, e
		}
		snapshot := ConfigVersion(v)
		d := Day{v.ID, v.Revision, flow.Daily{Day: day, Config: snapshot, Results: results, Status: "SETTLED", FrozenAt: now, SettledAt: &now}}
		for _, f := range results {
			if !f.Eligible || f.Result.Amount == "0" {
				continue
			}
			m, e := s.Flow.Members.Own(ctx, tx, sc, f.Result.UserID)
			if e != nil {
				return foundation.CommandResult{}, e
			}
			r := flow.Reward{ProgramID: v.ID, ProgramConfig: &snapshot, Since: f.Since, ID: flow.ID("program-day", v.ID, stepString(int(v.Revision)), m.UserID, day), UserID: m.UserID, AgentID: m.AgentID, Kind: flow.Commission, Day: day, Currency: v.Config.Currency, Amount: f.Result.Amount, Original: f.Result.Amount, ConfigRevision: v.Revision, CreatedAt: now, UpdatedAt: now, Status: "PENDING_REVIEW", UnlockedAt: now, Revision: 1, Members: []string{}, Qualifications: []flow.Qualified{}}
			if e = s.Flow.Repo.SaveReward(ctx, tx, sc, r); e != nil {
				return foundation.CommandResult{}, e
			}
		}
		if e = s.Repo.SaveDay(ctx, tx, sc, d); e != nil {
			return foundation.CommandResult{}, e
		}
		return response.Result(200, key, "program-day", d)
	})
	if errors.Is(e, errIncomplete) || errors.Is(e, errNotRunning) || errors.Is(e, errNotDue) {
		return nil
	}
	if e != nil {
		return e
	}
	if out.Status >= 400 {
		return errors.New("代理独立方案日结失败，保留原业务键重试：" + string(out.Body))
	}
	return nil
}

var errIncomplete = errors.New("PROGRAM_SOURCES_INCOMPLETE")
var errNotRunning = errors.New("PROGRAM_NOT_RUNNING")
var errNotDue = errors.New("PROGRAM_DAY_NOT_DUE")

// Summary 只读历史不受运行开关影响，停止方案仍可查版本、命中与结算依据。
func (s Service) Summary(ctx context.Context, sc foundation.Scope) (map[string]any, error) {
	versions, e := s.Repo.Versions(ctx, s.Flow.Query, sc)
	if e != nil {
		return nil, e
	}
	days, e := s.Repo.Days(ctx, s.Flow.Query, sc)
	if e != nil {
		return nil, e
	}
	receipts, e := s.Repo.Receipts(ctx, s.Flow.Query, sc)
	if e != nil {
		return nil, e
	}
	members, e := s.Flow.Members.List(ctx, s.Flow.Query, sc, s.Flow.Now())
	if e != nil {
		return nil, e
	}
	rewards := []flow.Reward{}
	for _, member := range members {
		rows, e := s.Flow.Repo.Rewards(ctx, s.Flow.Query, sc, member.UserID)
		if e != nil {
			return nil, e
		}
		for _, r := range rows {
			if r.ProgramID != "" {
				rewards = append(rewards, r)
			}
		}
	}
	sort.Slice(rewards, func(i, j int) bool {
		if rewards[i].UnlockedAt.Equal(rewards[j].UnlockedAt) {
			return rewards[i].ID < rewards[j].ID
		}
		return rewards[i].UnlockedAt.After(rewards[j].UnlockedAt)
	})
	total := len(rewards)
	if total > 1000 {
		rewards = rewards[:1000]
	}
	return map[string]any{"versions": versions, "current": Active(versions, s.Flow.Now()), "days": days, "receipts": receipts, "rewards": rewards, "reward_count": total, "rewards_truncated": total > 1000, "legacy_policy": "INDEPENDENT_STACK", "stopped_policy": "HISTORY_ONLY_NO_NEW_SETTLEMENT"}, nil
}
