package affiliatetasks

import (
	"context"
	"encoding/json"
	"errors"
	principal "github.com/ex-business/ExCommission/internal/identity/domain"
	response "github.com/ex-business/ExCommission/internal/platform/application"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	approval "github.com/ex-business/ExCommission/internal/workflow/domain"
	workflow "github.com/ex-business/ExCommission/internal/workflow/ports"
	"log/slog"
	"time"
)

type Service struct {
	Repo      Repository
	Query     db.Executor
	UOW       db.UnitOfWork
	Commands  db.Commands
	Workflow  workflow.Repository
	Now       func() time.Time
	ID        func() string
	Hash      func([]byte) string
	Authorize func(context.Context, db.Executor, principal.Principal, string) error
	LockScope func(context.Context, db.Executor, foundation.Scope) error
	Resolve   func(context.Context, foundation.Scope, Key) (Inspector, Executor, error)
}

func sc(p principal.Principal) foundation.Scope {
	return foundation.Scope{Tenant: p.Tenant, App: p.App}
}
func (s Service) updateAlerts(ctx context.Context, tx db.Executor, scope foundation.Scope, r Record, rules RuleVersion, now time.Time) error {
	conditions := map[string]bool{
		"WATERMARK": r.Status == WaitingSource && r.WaitingSince != nil && !now.Before(r.WaitingSince.Add(time.Duration(rules.Rules.WatermarkMinutes)*time.Minute)),
		"FAILURE":   r.Failures >= rules.Rules.FailureCount && (r.Status == Failed || r.Status == Running),
		"OVERDUE":   r.Status != Succeeded && r.Status != Blocked && !now.Before(r.Due.Add(time.Duration(rules.Rules.OverdueMinutes)*time.Minute)),
	}
	for _, kind := range []string{"WATERMARK", "FAILURE", "OVERDUE"} {
		active := rules.Rules.Enabled && conditions[kind]
		old, e := s.Repo.Alert(ctx, tx, scope, r.ID, kind)
		if e != nil && !errors.Is(e, ErrMissing) {
			return e
		}
		if active && (errors.Is(e, ErrMissing) || old.State == "RESOLVED") {
			a := Alert{ID: s.ID(), TaskID: r.ID, Kind: kind, State: "TRIGGERED", Reason: r.Reason, Triggered: now, RuleRevision: rules.Revision}
			if a.Reason == "" {
				a.Reason = "任务尚未完成，请查看当前阶段与执行历史"
			}
			if e = s.Repo.SaveAlert(ctx, tx, scope, a, s.ID(), now); e != nil {
				return e
			}
		} else if !active && e == nil && old.State == "TRIGGERED" {
			old.State = "RESOLVED"
			old.Resolved = &now
			old.Reason = "当前状态已不再满足触发条件：" + r.Status
			if !rules.Rules.Enabled {
				old.Reason = "告警规则已停用，原触发记录保留"
			}
			if e = s.Repo.SaveAlert(ctx, tx, scope, old, s.ID(), now); e != nil {
				return e
			}
		}
	}
	return nil
}
func apply(r *Record, p Report, now time.Time) {
	if p.Missing == nil {
		p.Missing = []Missing{}
	}
	if p.Status == WaitingSource {
		if r.WaitingSince == nil || r.Status != WaitingSource {
			r.WaitingSince = &now
		}
	} else {
		r.WaitingSince = nil
	}
	r.Report = p
	r.Checked = now
	r.Updated = now
}

// Run 运行记录在独立事务中落库；租约只协调调度，原日结事务的锁和业务幂等才是资金防重的最终保障。
func (s Service) Run(ctx context.Context, scope foundation.Scope, key Key, inspect Inspector, execute Executor) error {
	if !scope.Valid() || key.Validate() != nil {
		return foundation.ErrValidation
	}
	report, inspectionErr := inspect(ctx)
	if inspectionErr != nil {
		report = Report{Status: Ready, Phase: "CHECK_DEPENDENCIES"}
	}
	now := s.Now().UTC().Truncate(time.Microsecond)
	var lease string
	var attempt Attempt
	e := s.UOW.Within(ctx, func(tx db.Executor) error {
		if e := s.Repo.Ensure(ctx, tx, scope, key, now); e != nil {
			return e
		}
		r, e := s.Repo.Get(ctx, tx, scope, key.ID(), true)
		if e != nil {
			return e
		}
		rules, e := s.Repo.Rules(ctx, tx, scope)
		if e != nil {
			return e
		}
		if r.LeaseToken != "" && r.LeaseUntil != nil && r.LeaseUntil.After(now) {
			return nil
		}
		if r.Status == Succeeded {
			return s.updateAlerts(ctx, tx, scope, r, rules, now)
		}
		if r.LeaseToken != "" {
			// 原进程可能在提交日结后退出；先查原结果。只有无法确认成功的租约才登记中断并重试原业务键。
			state := "INTERRUPTED"
			if report.Status == Succeeded {
				state = Succeeded
			}
			end := now
			// 恢复只补齐原尝试的结果，保留最初申请人、原因和追踪编号，避免审计归属被覆盖。
			history, err := s.Repo.Attempts(ctx, tx, scope, r.ID)
			if err != nil {
				return err
			}
			if len(history) == 0 || history[0].Number != r.Attempts {
				return errors.New("任务租约缺少原执行历史")
			}
			a := history[0]
			a.Status, a.Finished, a.Error = state, &end, "上次租约到期，已重新核对原日结结果"
			if e = s.Repo.Attempt(ctx, tx, scope, a); e != nil {
				return e
			}
			r.LeaseToken = ""
			r.LeaseUntil = nil
		}
		manual := r.RetryRequested
		if report.Status != Ready && inspectionErr == nil {
			if manual {
				r.Attempts++
				a := Attempt{TaskID: r.ID, Number: r.Attempts, Status: report.Status, Trigger: "MANUAL", Actor: r.RequestedBy, Reason: r.RetryReason, RequestID: r.RequestID, Started: now, Finished: &now}
				if e = s.Repo.Attempt(ctx, tx, scope, a); e != nil {
					return e
				}
			}
			r.RetryRequested = false
			apply(&r, report, now)
			if report.Status == Succeeded {
				r.Finished = &now
				r.Failures = 0
				r.NextRetry = nil
				r.LastError = ""
			}
			if e = s.Repo.Save(ctx, tx, scope, r); e != nil {
				return e
			}
			return s.updateAlerts(ctx, tx, scope, r, rules, now)
		}
		if !manual && r.Status == Failed && (!rules.Rules.AutoRetry || r.AutoAttempts >= rules.Rules.MaxAttempts || (r.NextRetry != nil && r.NextRetry.After(now))) {
			r.Checked = now
			r.Updated = now
			// 自动预算只统计自动执行，人工检查不会消耗预算；耗尽或停用后不再展示虚假的下次重试时间。
			if !rules.Rules.AutoRetry {
				r.NextRetry = nil
				r.Reason = "失败自动重试已停用，可申请安全重试"
			} else if r.AutoAttempts >= rules.Rules.MaxAttempts {
				r.NextRetry = nil
				r.Reason = "自动执行次数已达上限，需人工检查后申请安全重试"
			}
			if e = s.Repo.Save(ctx, tx, scope, r); e != nil {
				return e
			}
			return s.updateAlerts(ctx, tx, scope, r, rules, now)
		}
		lease = s.ID()
		until := now.Add(2 * time.Minute)
		r.LeaseToken = lease
		r.LeaseUntil = &until
		r.Attempts++
		r.Started = &now
		r.Finished = nil
		r.NextRetry = nil
		r.Checked = now
		r.Updated = now
		r.Status = Running
		r.Missing = []Missing{}
		r.WaitingSince = nil
		r.Phase = "EXECUTING"
		r.Completed = 0
		r.Total = 0
		r.Reason = "正在执行原业务日，请等待事务确认"
		r.RetryRequested = false
		r.RequestID = s.ID()
		trigger, actor, reason := "AUTO", "system-worker", "自动处理原业务日"
		if !manual {
			r.AutoAttempts++
		}
		if manual {
			trigger, actor, reason = "MANUAL", r.RequestedBy, r.RetryReason
		}
		attempt = Attempt{TaskID: r.ID, Number: r.Attempts, Status: Running, Trigger: trigger, Actor: actor, Reason: reason, RequestID: r.RequestID, Started: now}
		if e = s.Repo.Save(ctx, tx, scope, r); e != nil {
			return e
		}
		return s.Repo.Attempt(ctx, tx, scope, attempt)
	})
	if e != nil || lease == "" {
		return e
	}
	runCtx := foundation.WithRequest(ctx, attempt.RequestID, "system-affiliate-task")
	runCtx = context.WithValue(runCtx, progressKey{}, progressFn(func(phase string, done, total int) {
		pctx, cancel := context.WithTimeout(context.WithoutCancel(runCtx), 2*time.Second)
		defer cancel()
		err := s.UOW.Within(pctx, func(tx db.Executor) error {
			r, e := s.Repo.Get(pctx, tx, scope, key.ID(), true)
			if e != nil {
				return e
			}
			if r.LeaseToken != lease {
				return nil
			}
			r.Phase = phase
			r.Completed = done
			r.Total = total
			r.Updated = s.Now().UTC()
			return s.Repo.Save(pctx, tx, scope, r)
		})
		if err != nil {
			slog.WarnContext(runCtx, "代理任务进度写入失败，最终结果仍以日结事务为准", "task_id", key.ID(), "request_id", attempt.RequestID, "error", err)
		}
	}))
	runErr := inspectionErr
	if runErr == nil {
		runErr = execute(runCtx)
	}
	// 调用超时后仍用短暂独立上下文写运行结果，避免任务一直显示运行中；不延长原资金事务。
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	final, checkErr := inspect(finishCtx)
	if checkErr == nil && final.Status == Succeeded {
		runErr = nil
	} else if runErr == nil && checkErr != nil {
		runErr = checkErr
	} else if runErr == nil && final.Status == Ready {
		runErr = errors.New("原日结结果尚未确认，保留原任务重试")
	}
	finishErr := s.UOW.Within(finishCtx, func(tx db.Executor) error {
		r, e := s.Repo.Get(finishCtx, tx, scope, key.ID(), true)
		if e != nil {
			return e
		}
		if r.LeaseToken != lease {
			return nil
		}
		at := s.Now().UTC().Truncate(time.Microsecond)
		rules, e := s.Repo.Rules(finishCtx, tx, scope)
		if e != nil {
			return e
		}
		r.LeaseToken = ""
		r.LeaseUntil = nil
		r.Finished = &at
		attempt.Finished = &at
		if runErr != nil {
			r.Status = Failed
			r.Phase = "FAILED"
			r.Reason = "执行失败，原资金事务未确认完成"
			r.LastError = runErr.Error()
			if chars := []rune(r.LastError); len(chars) > 2000 {
				r.LastError = string(chars[:2000])
			}
			r.Failures++
			r.Completed = 0
			r.Total = 0
			r.Updated = at
			r.Checked = at
			delay := rules.Rules.BaseSeconds
			for i := 1; i < r.Failures && delay < rules.Rules.MaxSeconds; i++ {
				delay *= 2
			}
			if delay > rules.Rules.MaxSeconds {
				delay = rules.Rules.MaxSeconds
			}
			next := at.Add(time.Duration(delay) * time.Second)
			r.NextRetry = &next
			attempt.Status = Failed
			attempt.Error = r.LastError
		} else {
			apply(&r, final, at)
			r.LastError = ""
			attempt.Status = final.Status
			if final.Status == Succeeded {
				r.Failures = 0
				r.NextRetry = nil
			}
		}
		if e = s.Repo.Save(finishCtx, tx, scope, r); e != nil {
			return e
		}
		if e = s.Repo.Attempt(finishCtx, tx, scope, attempt); e != nil {
			return e
		}
		return s.updateAlerts(finishCtx, tx, scope, r, rules, at)
	})
	if finishErr != nil {
		return finishErr
	}
	if runErr != nil {
		slog.ErrorContext(finishCtx, "代理日结任务执行失败，已保留原任务及重试依据", "task_id", key.ID(), "request_id", attempt.RequestID, "program_id", key.Program, "business_day", key.Day, "error", runErr)
	}
	return nil
}
func (s Service) Retry(ctx context.Context, p principal.Principal, id, reason, key, rid string) (foundation.CommandResult, error) {
	if !foundation.ValidID(id) || !foundation.ValidID(reason) {
		return foundation.CommandResult{}, foundation.ErrValidation
	}
	ctx = foundation.WithRequest(ctx, rid, p.Subject)
	return s.Commands.Execute(ctx, sc(p), "affiliate-task-retry", key, map[string]string{"task_id": id, "reason": reason}, func(tx db.Executor) (foundation.CommandResult, error) {
		if e := s.Authorize(ctx, tx, p, "tasks:recover"); e != nil {
			return foundation.CommandResult{}, e
		}
		r, e := s.Repo.Get(ctx, tx, sc(p), id, true)
		if e != nil {
			return foundation.CommandResult{}, e
		}
		now := s.Now().UTC()
		if r.Status == Succeeded || r.RetryRequested || (r.LeaseUntil != nil && r.LeaseUntil.After(now)) {
			return foundation.CommandResult{}, ErrBusy
		}
		inspect, _, e := s.Resolve(ctx, sc(p), r.Key)
		if e != nil {
			return foundation.CommandResult{}, e
		}
		report, e := inspect(ctx)
		if e != nil {
			return foundation.CommandResult{}, e
		}
		if report.Status != Ready && report.Status != WaitingSource {
			return foundation.CommandResult{}, errors.New("STATE_CONFLICT:TASK_NOT_RETRYABLE")
		}
		r.RetryRequested = true
		r.RequestedBy = p.Subject
		r.RetryReason = reason
		r.RequestedAt = &now
		r.RequestID = rid
		r.Updated = now
		if e = s.Repo.Save(ctx, tx, sc(p), r); e != nil {
			return foundation.CommandResult{}, e
		}
		return response.Result(202, id, rid, map[string]any{"task_id": id, "status": "RETRY_QUEUED"})
	})
}
func (s Service) RequestRules(ctx context.Context, p principal.Principal, in RuleChange, key, rid string) (foundation.CommandResult, error) {
	if in.Rules.Validate() != nil || !foundation.ValidID(in.Reason) {
		return foundation.CommandResult{}, foundation.ErrValidation
	}
	ctx = foundation.WithRequest(ctx, rid, p.Subject)
	return s.Commands.Execute(ctx, sc(p), "affiliate-task-alert-request", key, in, func(tx db.Executor) (foundation.CommandResult, error) {
		if e := s.Authorize(ctx, tx, p, "affiliate:configure"); e != nil {
			return foundation.CommandResult{}, e
		}
		if e := s.LockScope(ctx, tx, sc(p)); e != nil {
			return foundation.CommandResult{}, e
		}
		old, e := s.Repo.Rules(ctx, tx, sc(p))
		if e != nil {
			return foundation.CommandResult{}, e
		}
		if old.Revision != in.Revision {
			return foundation.CommandResult{}, foundation.ErrConflict
		}
		raw, _ := json.Marshal(in)
		a := approval.Request{ID: s.ID(), Type: "AFFILIATE_TASK_ALERT", Applicant: p.Subject, ObjectRef: "affiliate-task-alerts", ObjectHash: s.Hash(raw), Payload: raw}
		if e = s.Workflow.Create(ctx, tx, sc(p), a, in.Reason, s.Now()); e != nil {
			return foundation.CommandResult{}, e
		}
		return response.Result(202, a.ID, rid, map[string]any{"approval_id": a.ID, "object_hash": a.ObjectHash, "revision": 0})
	})
}
func (s Service) ExecuteRules(ctx context.Context, tx db.Executor, scope foundation.Scope, a approval.Request, at time.Time) error {
	if a.Type != "AFFILIATE_TASK_ALERT" || a.ObjectRef != "affiliate-task-alerts" || s.Hash(a.Payload) != a.ObjectHash {
		return foundation.ErrValidation
	}
	var in RuleChange
	if json.Unmarshal(a.Payload, &in) != nil || in.Rules.Validate() != nil {
		return foundation.ErrValidation
	}
	if e := s.LockScope(ctx, tx, scope); e != nil {
		return e
	}
	old, e := s.Repo.Rules(ctx, tx, scope)
	if e != nil {
		return e
	}
	if old.Revision != in.Revision {
		return foundation.ErrConflict
	}
	return s.Repo.SaveRules(ctx, tx, scope, RuleVersion{old.Revision + 1, in.Rules, a.ID, at})
}
