package affiliateprograms

import (
	"context"
	"database/sql"
	"errors"
	tasks "github.com/ex-business/ExCommission/internal/affiliatetasks"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"sort"
	"time"
)

func TaskKey(v Version, date time.Time) tasks.Key {
	return tasks.Key{Program: v.ID, Revision: v.Revision, Day: date.In(v.Config.Location()).Format("2006-01-02"), Timezone: v.Config.Timezone, Due: tasks.DueAt(date)}
}

// TaskDays 依据原始命中事实和已启动区间发现任务；没有任何水位到达时也能显示最近应结日，避免缺水位任务凭空消失。
func (s Service) TaskDays(ctx context.Context, sc foundation.Scope, v Version, all []Version, now time.Time) ([]string, error) {
	rows, e := s.Flow.Query.QueryContext(ctx, `SELECT DISTINCT business_time FROM affiliate_program_facts WHERE tenant_id=? AND app_id=? AND program_id=? AND revision=? ORDER BY business_time`, sc.Tenant, sc.App, v.ID, v.Revision)
	if e != nil {
		return nil, e
	}
	days := map[string]bool{}
	for rows.Next() {
		var at time.Time
		if e = rows.Scan(&at); e != nil {
			rows.Close()
			return nil, e
		}
		days[at.In(v.Config.Location()).Format("2006-01-02")] = true
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	end := time.Time{}
	for _, next := range all {
		if next.ID == v.ID && next.Revision > v.Revision && (end.IsZero() || next.Effective.Before(end)) {
			end = next.Effective
		}
	}
	for _, date := range []time.Time{now.In(v.Config.Location()), v.Config.DueDate(now).In(v.Config.Location())} {
		start, finish := v.Config.Day(date)
		if !finish.After(v.Effective) || (!end.IsZero() && !start.Before(end)) {
			continue
		}
		days[start.In(v.Config.Location()).Format("2006-01-02")] = true
	}
	ordered := []string{}
	for day := range days {
		ordered = append(ordered, day)
	}
	sort.Strings(ordered)
	return ordered, nil
}
func (s Service) InspectTaskDay(ctx context.Context, sc foundation.Scope, v Version, day string) (tasks.Report, error) {
	daily, e := s.Repo.Day(ctx, s.Flow.Query, sc, v.ID, v.Revision, day)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return tasks.Report{}, e
	}
	if e == nil && daily.Daily.Status == "SETTLED" {
		n := len(daily.Daily.Results)
		return tasks.Report{Status: tasks.Succeeded, Phase: "SETTLED", Reason: "日结已落库，待审核权益仍按原流程处理", Completed: n, Total: n}, nil
	}
	all, e := s.Repo.Versions(ctx, s.Flow.Query, sc)
	if e != nil {
		return tasks.Report{}, e
	}
	if !CanProcess(all, v, s.Flow.Now()) {
		return tasks.Report{Status: tasks.Blocked, Phase: "STOPPED", Reason: "方案未启动、已停止，或该任务属于恢复前的旧运行区间"}, nil
	}
	if s.Flow.CanReward != nil {
		if e = s.Flow.CanReward(ctx, s.Flow.Query, sc); e != nil {
			if e.Error() == "SITE_AGENT_DISABLED" {
				return tasks.Report{Status: tasks.Blocked, Phase: "SITE_DISABLED", Reason: "站点代理业务已暂停"}, nil
			}
			return tasks.Report{}, e
		}
	}
	date, e := time.ParseInLocation("2006-01-02", day, v.Config.Location())
	if e != nil {
		return tasks.Report{}, e
	}
	start, end := v.Config.Day(date)
	missing, configured, e := s.MissingWatermarks(ctx, s.Flow.Query, sc, v, start, end)
	if e != nil {
		return tasks.Report{}, e
	}
	if s.Flow.Now().Before(tasks.DueAt(date)) {
		return tasks.Report{Status: tasks.WaitingTime, Phase: "WAITING_DUE", Reason: "等待本方案业务日结束及01:00日结时间", Missing: missing}, nil
	}
	if !configured || len(missing) > 0 {
		return tasks.Report{Status: tasks.WaitingSource, Phase: "CHECK_WATERMARKS", Reason: "等待完整来源水位；跨时区需覆盖相邻来源业务日", Missing: missing}, nil
	}
	return tasks.Report{Status: tasks.Ready, Phase: "READY", Reason: "运行状态与来源依赖均满足"}, nil
}

// MissingWatermarks 与正式结算共用依赖计算，不凭猜测显示来源，也不因只收到一个来源就误报水位完整。
func (s Service) MissingWatermarks(ctx context.Context, tx db.Executor, sc foundation.Scope, v Version, start, end time.Time) ([]tasks.Missing, bool, error) {
	rows, e := tx.QueryContext(ctx, `SELECT DISTINCT source_id FROM affiliate_program_facts WHERE tenant_id=? AND app_id=? AND program_id=? AND revision=? AND business_time<?`, sc.Tenant, sc.App, v.ID, v.Revision, end)
	if e != nil {
		return nil, false, e
	}
	required := map[string]bool{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return nil, false, e
		}
		required[id] = true
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, false, e
	}
	for id := range s.Flow.Sources {
		if contains(v.Match.Sources, id) {
			required[id] = true
		}
	}
	if s.Flow.RequiredSources != nil {
		more, e := s.Flow.RequiredSources(ctx, tx, sc, start, end)
		if e != nil {
			return nil, false, e
		}
		for _, id := range more {
			if contains(v.Match.Sources, id) {
				required[id] = true
			}
		}
	}
	for _, id := range v.Match.Sources {
		required[id] = true
	}
	sources := []string{}
	for id := range required {
		sources = append(sources, id)
	}
	sort.Strings(sources)
	out := []tasks.Missing{}
	for at := start; at.Before(end); {
		cfg, e := s.Flow.Configs.At(ctx, tx, sc, at)
		if errors.Is(e, sql.ErrNoRows) {
			cfg, e = s.Flow.Configs.At(ctx, tx, sc, s.Flow.Now())
		}
		if e != nil {
			return nil, false, e
		}
		_, next := cfg.Config.Day(at)
		if !next.After(at) {
			return nil, false, foundation.ErrValidation
		}
		day := at.In(cfg.Config.Location()).Format("2006-01-02")
		for _, source := range sources {
			closed, e := s.Flow.DailyRepo.Closed(ctx, tx, sc, source, day)
			if e != nil {
				return nil, false, e
			}
			if !closed {
				out = append(out, tasks.Missing{Source: source, Day: day})
			}
		}
		at = next
	}
	return out, len(sources) > 0, nil
}
