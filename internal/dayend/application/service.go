// Package application 编排日终冻结，不改代理审批资格，不向其他模块表写入统计。
package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	agents "github.com/ex-business/ExCommission/internal/agents/ports"
	calc "github.com/ex-business/ExCommission/internal/calculation/domain"
	calculation "github.com/ex-business/ExCommission/internal/calculation/ports"
	model "github.com/ex-business/ExCommission/internal/dayend/domain"
	"github.com/ex-business/ExCommission/internal/dayend/ports"
	identity "github.com/ex-business/ExCommission/internal/identity/domain"
	event "github.com/ex-business/ExCommission/internal/integration/domain"
	plan "github.com/ex-business/ExCommission/internal/plans/domain"
	plans "github.com/ex-business/ExCommission/internal/plans/ports"
	response "github.com/ex-business/ExCommission/internal/platform/application"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	qmodel "github.com/ex-business/ExCommission/internal/qualification/domain"
	qualification "github.com/ex-business/ExCommission/internal/qualification/ports"
	"log/slog"
	"math/big"
	"strconv"
	"time"
)

type Service struct {
	Commands  db.Commands
	Repo      ports.Repository
	Plans     plans.Repository
	Tasks     ports.Tasks
	Resolver  calculation.SnapshotResolver
	Subjects  ports.Subjects
	Relations agents.Repository
	Members   ports.EffectiveMembers
	Policies  qualification.Repository
	Now       func() time.Time
}
type Request struct {
	PlanID    string `json:"plan_id"`
	VersionID string `json:"expected_plan_version_id"`
	Day       string `json:"business_day"`
	// Watermark 为来源确认已投递到日末的证据，不用服务器时间冒充来源完整性。
	Watermark string `json:"source_watermark"`
}

func (s Service) Freeze(ctx context.Context, p identity.Principal, input Request, key, request string) (foundation.CommandResult, error) {
	scope := foundation.Scope{Tenant: p.Tenant, App: p.App}
	ctx = foundation.WithRequest(ctx, request, p.Subject)
	if parsed, err := time.Parse("2006-01-02", input.Day); err != nil || parsed.Format("2006-01-02") != input.Day {
		return foundation.CommandResult{}, foundation.ErrValidation
	}
	if !foundation.ValidID(input.PlanID) || !foundation.ValidID(input.VersionID) || !foundation.ValidID(input.Watermark) {
		return foundation.CommandResult{}, foundation.ErrValidation
	}
	result, err := s.Commands.Execute(ctx, scope, "dayend-freeze", key, input, func(tx db.Executor) (foundation.CommandResult, error) {
		if err := s.Repo.Lock(ctx, tx, scope, input.PlanID, input.Day); err != nil {
			return foundation.CommandResult{}, err
		}
		previous, err := s.Repo.Get(ctx, tx, scope, input.PlanID, input.Day)
		if err == nil {
			if previous.VersionID != input.VersionID || previous.Watermark != input.Watermark {
				return foundation.CommandResult{}, foundation.ErrConflict
			}
			return response.Result(200, input.PlanID, request, previous)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return foundation.CommandResult{}, err
		}
		// 按期望的已发布版本定位时区，避免UTC日期在跨时区边界命中其他版本。
		versions, err := s.Plans.Versions(ctx, tx, scope, input.PlanID)
		if err != nil {
			return foundation.CommandResult{}, err
		}
		var v plan.Version
		for _, candidate := range versions {
			if candidate.ID == input.VersionID {
				v = candidate
				break
			}
		}
		d := v.Config.AgentPolicy.DayEnd
		if v.ID != input.VersionID || v.Config.AgentPolicy.GradeWindow != "BUSINESS_DAY" || d == nil {
			return foundation.CommandResult{}, foundation.ErrConflict
		}
		start, end, err := model.Bounds(input.Day, d.Timezone)
		if err != nil {
			return foundation.CommandResult{}, err
		}
		now := s.Now()
		if end.After(now) || v.From.After(start) || (v.To != nil && v.To.Before(end)) {
			return foundation.CommandResult{}, foundation.ErrValidation
		}
		policy, err := s.Policies.Policy(ctx, tx, scope, d.QualificationPolicyID)
		if errors.Is(err, sql.ErrNoRows) {
			return foundation.CommandResult{}, errors.New("UNKNOWN_FACT")
		}
		if err != nil {
			return foundation.CommandResult{}, err
		}
		// 有限自然周期与业务日使用相同时区，防止日终跨进另一个会员统计周期。
		memberPolicy := qmodel.StatisticsFor(policy.Statistics)
		if memberPolicy.Range != qmodel.Forever && memberPolicy.Timezone != d.Timezone {
			return foundation.CommandResult{}, foundation.ErrValidation
		}
		if policy.Source != d.SourceID || policy.Currency != v.Config.Currency {
			return foundation.CommandResult{}, foundation.ErrValidation
		}
		frozen := model.Snapshot{PlanID: input.PlanID, VersionID: v.ID, Day: input.Day, Start: start, End: end, Cutoff: now, Policy: *d, Watermark: input.Watermark, TaskIDs: []string{}, Agents: map[string]model.Agent{}}
		at := end.Add(-time.Microsecond)
		subjects, err := s.Subjects.DayEndSubjects(ctx, tx, scope, at)
		if err != nil {
			return foundation.CommandResult{}, err
		}
		for _, id := range subjects {
			frozen.Agents[id] = model.Agent{Metrics: map[string]string{"TEAM_VALID_VOLUME": "0", "EFFECTIVE_MEMBER_COUNT": "0"}}
		}
		members, err := s.Members.EffectiveStatisticsAt(ctx, tx, scope, d.QualificationPolicyID, d.SourceID, end)
		if err != nil {
			return foundation.CommandResult{}, err
		}
		counts := map[string]int{}
		frozen.MemberStatistics = &members.Window
		for _, id := range members.AgentIDs {
			chain, _, err := s.Relations.Ancestors(ctx, tx, scope, id, at, 0, 3)
			if err != nil {
				return foundation.CommandResult{}, err
			}
			for _, a := range chain {
				if model.Includes(d.MemberDepths, a.Depth) {
					counts[a.AgentID]++
				}
			}
		}
		for id, count := range counts {
			a, ok := frozen.Agents[id]
			if !ok {
				return foundation.CommandResult{}, errors.New("UNKNOWN_FACT")
			}
			a.Metrics["EFFECTIVE_MEMBER_COUNT"] = strconv.Itoa(count)
		}
		tasks, err := s.Tasks.DayFacts(ctx, tx, scope, d.SourceID, start, end, now)
		if err != nil {
			return foundation.CommandResult{}, err
		}
		for _, task := range tasks {
			var e event.Event
			if err = json.Unmarshal(task.EventBody, &e); err != nil {
				return foundation.CommandResult{}, err
			}
			if e.Product != v.Config.Route.Product || e.Scene != v.Config.Route.Scene || e.Type != v.Config.Route.EventType {
				continue
			}
			snapshot, err := s.Resolver.Resolve(ctx, tx, scope, task)
			if err != nil {
				return foundation.CommandResult{}, err
			}
			if snapshot.PlanVersionID != v.ID || snapshot.Facts.Currency != v.Config.Currency || snapshot.Facts.Confirmed == nil || !*snapshot.Facts.Confirmed {
				return foundation.CommandResult{}, errors.New("UNKNOWN_FACT")
			}
			// 有效业务量与会员达标独立：所有已确认订单参与统计，不以是否有效会员过滤。
			for _, a := range snapshot.Ancestors {
				if model.Includes(d.VolumeDepths, a.Depth) {
					if err = add(frozen.Agents, a.ID, "TEAM_VALID_VOLUME", snapshot.Facts.Basis); err != nil {
						return foundation.CommandResult{}, err
					}
				}
			}
			frozen.TaskIDs = append(frozen.TaskIDs, task.ID)
		}
		// 当前扩展的定级指标只接受明确实现的有效会员数和团队业务量，不借用月度净额名称。
		for id, a := range frozen.Agents {
			grade, err := plan.SelectGrade(v.Config.Grades, a.Metrics, v.Config.Currency)
			if err != nil {
				return foundation.CommandResult{}, err
			}
			a.Grade = grade
			frozen.Agents[id] = a
		}
		if err = s.Repo.Save(ctx, tx, scope, frozen); err != nil {
			return foundation.CommandResult{}, err
		}
		if err = s.Tasks.ReleaseDayEnd(ctx, tx, scope, frozen.TaskIDs, now); err != nil {
			return foundation.CommandResult{}, err
		}
		return response.Result(200, input.PlanID, request, frozen)
	})
	// 提交成功后再记录完成日志，失败事务不对外宣称冻结成功。
	if err == nil && result.Status == 200 && !result.Replayed {
		slog.InfoContext(ctx, "日终快照冻结命令已完成", "request_id", request, "plan_id", input.PlanID, "business_day", input.Day, "result", "SUCCESS")
	}
	return result, err
}
func add(agents map[string]model.Agent, id, metric, value string) error {
	a, ok := agents[id]
	if !ok {
		return errors.New("UNKNOWN_FACT")
	}
	old, err := foundation.ParseDecimal(a.Metrics[metric])
	if err != nil {
		return err
	}
	n, err := foundation.ParseDecimal(value)
	if err != nil {
		return err
	}
	total, err := foundation.DecimalFromAtoms(new(big.Int).Add(old.Atoms(), n.Atoms()))
	if err != nil {
		return err
	}
	a.Metrics[metric] = total.String()
	return nil
}

// Resolver 仅在日终策略下替换计算等级；保留原资格、状态、合同和业务时点关系证据。
type Resolver struct {
	Enabled bool
	Base    calculation.SnapshotResolver
	Repo    ports.Repository
	Plans   plans.Repository
	Tasks   calculation.Tasks
}

func (r Resolver) Resolve(ctx context.Context, tx db.Executor, s foundation.Scope, t calc.Task) (calc.Input, error) {
	in, err := r.Base.Resolve(ctx, tx, s, t)
	if err != nil {
		return in, err
	}
	if in.Plan.AgentPolicy.GradeWindow != "BUSINESS_DAY" {
		return in, nil
	}
	if !r.Enabled {
		return in, errors.New("UNSUPPORTED_CAPABILITY")
	}
	d := in.Plan.AgentPolicy.DayEnd
	if d == nil || d.SourceID != t.Source {
		return in, errors.New("UNSUPPORTED_CAPABILITY")
	}
	loc, err := time.LoadLocation(d.Timezone)
	if err != nil {
		return in, err
	}
	day := t.BusinessTime.In(loc).Format("2006-01-02")
	frozen, err := r.Repo.Get(ctx, tx, s, in.Plan.PlanID, day)
	if errors.Is(err, sql.ErrNoRows) {
		return in, errors.New("DAYEND_PENDING")
	}
	if err != nil {
		return in, err
	}
	if frozen.VersionID != in.PlanVersionID {
		return in, errors.New("SNAPSHOT_INTEGRITY_FAILED")
	}
	in.DayEnd = &calc.DayEndEvidence{BusinessDay: day, PlanVersionID: frozen.VersionID, Cutoff: frozen.Cutoff, Watermark: frozen.Watermark, Grades: map[string]string{}}
	known := false
	for _, id := range frozen.TaskIDs {
		if id == t.ID {
			known = true
			break
		}
	}
	// 迟到订单不静默改变已冻结等级：挂起等待后续核验处理，避免少付或重复支付。
	if !known {
		return in, errors.New("DAYEND_LATE_FACT")
	}
	for i := range in.Ancestors {
		a := &in.Ancestors[i]
		grade, ok := frozen.Agents[a.ID]
		if !ok {
			return in, errors.New("UNKNOWN_FACT")
		}
		a.Grade = grade.Grade
		in.DayEnd.Grades[a.ID] = grade.Grade
	}
	return in, nil
}

// WaitPending 与冻结共用日锁并在同一事务完成挂起，消除冻结与worker挂起之间的遗漏唤醒。
func (r Resolver) WaitPending(ctx context.Context, tx db.Executor, s foundation.Scope, t calc.Task, now time.Time) error {
	var e event.Event
	if err := json.Unmarshal(t.EventBody, &e); err != nil {
		return err
	}
	v, err := r.Plans.Select(ctx, tx, s, plan.Route{Product: e.Product, EventType: e.Type, Scene: e.Scene}, e.Time)
	if err != nil {
		return err
	}
	d := v.Config.AgentPolicy.DayEnd
	if d == nil {
		return errors.New("UNKNOWN_FACT")
	}
	loc, err := time.LoadLocation(d.Timezone)
	if err != nil {
		return err
	}
	day := t.BusinessTime.In(loc).Format("2006-01-02")
	if err = r.Repo.Lock(ctx, tx, s, v.Config.PlanID, day); err != nil {
		return err
	}
	_, err = r.Repo.Get(ctx, tx, s, v.Config.PlanID, day)
	if errors.Is(err, sql.ErrNoRows) {
		reason := "DAYEND_PENDING"
		return r.Tasks.Finish(ctx, tx, s, t, "SUSPENDED", &reason, nil, now)
	}
	if err != nil {
		return err
	}
	return r.Tasks.Finish(ctx, tx, s, t, "QUEUED", nil, nil, now)
}
