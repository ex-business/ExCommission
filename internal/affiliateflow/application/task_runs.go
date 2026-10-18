package application

import (
	"context"
	"database/sql"
	"errors"
	affiliate "github.com/ex-business/ExCommission/internal/affiliate/domain"
	tasks "github.com/ex-business/ExCommission/internal/affiliatetasks"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"sort"
	"time"
)

var ErrTaskStopped = errors.New("AFFILIATE_TASK_PLAN_STOPPED")

// LegacyTaskAllowed 在原资金事务的站点锁内复核开关，重试不能把停止前未结算日期当作新任务重新发放。
func (s Service) LegacyTaskAllowed(ctx context.Context, tx db.Executor, sc foundation.Scope, key tasks.Key) error {
	current, e := s.Configs.At(ctx, tx, sc, s.Now())
	if e != nil {
		return e
	}
	if !current.Config.Enabled {
		return ErrTaskStopped
	}
	if reader, ok := s.Configs.(interface {
		Version(context.Context, db.Executor, foundation.Scope, uint64) (affiliate.Version, error)
	}); ok {
		for revision := key.Revision; revision <= current.Revision; revision++ {
			v, e := reader.Version(ctx, tx, sc, revision)
			if e != nil {
				return e
			}
			if !v.Config.Enabled {
				return ErrTaskStopped
			}
		}
	}
	if s.CanReward != nil {
		if e = s.CanReward(ctx, tx, sc); e != nil {
			if e.Error() == "SITE_AGENT_DISABLED" {
				return ErrTaskStopped
			}
			return e
		}
	}
	return nil
}
func (s Service) LegacyTaskKey(ctx context.Context, tx db.Executor, sc foundation.Scope, day string) (tasks.Key, error) {
	current, e := s.Configs.At(ctx, tx, sc, s.Now())
	if e != nil {
		return tasks.Key{}, e
	}
	date, e := time.ParseInLocation("2006-01-02", day, current.Config.Location())
	if e != nil {
		return tasks.Key{}, foundation.ErrValidation
	}
	_, end := current.Config.Day(date)
	v, e := s.Configs.At(ctx, tx, sc, end.Add(-time.Microsecond))
	if e != nil {
		return tasks.Key{}, e
	}
	return tasks.Key{Program: "legacy", Revision: v.Revision, Day: day, Timezone: v.Config.Timezone, Due: tasks.DueAt(date)}, nil
}
func (s Service) InspectLegacyTask(ctx context.Context, sc foundation.Scope, key tasks.Key) (tasks.Report, error) {
	daily, e := s.DailyRepo.Daily(ctx, s.Query, sc, key.Day)
	exists := e == nil
	if e != nil && e.Error() != "NOT_FOUND" {
		return tasks.Report{}, e
	}
	if exists && daily.Status == "SETTLED" {
		return tasks.Report{Status: tasks.Succeeded, Phase: "SETTLED", Reason: "日结已落库，佣金按原流程审核", Completed: len(daily.Results), Total: len(daily.Results)}, nil
	}
	if e = s.LegacyTaskAllowed(ctx, s.Query, sc, key); e != nil {
		if errors.Is(e, ErrTaskStopped) {
			return tasks.Report{Status: tasks.Blocked, Phase: "STOPPED", Reason: "方案已停止，或该日期属于停止前的旧运行区间"}, nil
		}
		return tasks.Report{}, e
	}
	loc, e := time.LoadLocation(key.Timezone)
	if e != nil {
		return tasks.Report{}, e
	}
	date, e := time.ParseInLocation("2006-01-02", key.Day, loc)
	if e != nil {
		return tasks.Report{}, e
	}
	end := date.AddDate(0, 0, 1)
	now := s.Now()
	if now.Before(end) {
		return tasks.Report{Status: tasks.WaitingTime, Phase: "BUSINESS_DAY_OPEN", Reason: "业务日尚未结束"}, nil
	}
	missing := []tasks.Missing{}
	required := map[string]bool{}
	for id := range s.Sources {
		required[id] = true
	}
	if s.RequiredSources != nil {
		ids, e := s.RequiredSources(ctx, s.Query, sc, date.UTC(), end.UTC())
		if e != nil {
			return tasks.Report{}, e
		}
		for _, id := range ids {
			required[id] = true
		}
	}
	for source := range required {
		closed, e := s.DailyRepo.Closed(ctx, s.Query, sc, source, key.Day)
		if e != nil {
			return tasks.Report{}, e
		}
		if !closed {
			missing = append(missing, tasks.Missing{Source: source, Day: key.Day})
		}
	}
	sort.Slice(missing, func(i, j int) bool { return missing[i].Source < missing[j].Source })
	if len(required) == 0 || len(missing) > 0 {
		return tasks.Report{Status: tasks.WaitingSource, Phase: "CHECK_WATERMARKS", Reason: "等待来源业务日水位；补齐后继续原日结", Missing: missing}, nil
	}
	if exists && now.Before(key.Due) {
		return tasks.Report{Status: tasks.WaitingTime, Phase: "FROZEN", Reason: "业务数据已冻结，等待01:00日结", Completed: len(daily.Results), Total: len(daily.Results)}, nil
	}
	return tasks.Report{Status: tasks.Ready, Phase: "READY", Reason: "来源完整，可以继续原业务日"}, nil
}
func (s Service) ExecuteLegacyTask(ctx context.Context, sc foundation.Scope, key tasks.Key) error {
	tasks.Progress(ctx, "FREEZING", 0, 0)
	if e := s.freeze(ctx, sc, key.Day); e != nil {
		return e
	}
	tasks.Progress(ctx, "SETTLING", 0, 0)
	return s.settleDay(ctx, sc, key.Day)
}
func (s Service) runLegacyTasks(ctx context.Context, sc foundation.Scope) error {
	current, e := s.Configs.At(ctx, s.Query, sc, s.Now())
	if errors.Is(e, sql.ErrNoRows) {
		return nil
	}
	if e != nil {
		return e
	}
	days := map[string]bool{s.Now().In(current.Config.Location()).Format("2006-01-02"): true, current.Config.DueDate(s.Now()).In(current.Config.Location()).Format("2006-01-02"): true}
	if discovery, ok := s.Repo.(interface {
		TaskFactDays(context.Context, db.Executor, foundation.Scope, *time.Location) ([]string, error)
	}); ok {
		dates, e := discovery.TaskFactDays(ctx, s.Query, sc, current.Config.Location())
		if e != nil {
			return e
		}
		for _, day := range dates {
			days[day] = true
		}
	}
	pending, e := s.DailyRepo.PendingDays(ctx, s.Query, sc)
	if e != nil {
		return e
	}
	for _, day := range pending {
		days[day] = true
	}
	ordered := []string{}
	for day := range days {
		ordered = append(ordered, day)
	}
	sort.Strings(ordered)
	for _, day := range ordered {
		key, e := s.LegacyTaskKey(ctx, s.Query, sc, day)
		if errors.Is(e, sql.ErrNoRows) {
			continue
		}
		if e != nil {
			return e
		}
		if e = s.TaskRun(ctx, sc, key, func(c context.Context) (tasks.Report, error) { return s.InspectLegacyTask(c, sc, key) }, func(c context.Context) error { return s.ExecuteLegacyTask(c, sc, key) }); e != nil {
			return e
		}
	}
	return nil
}
