package runtime

import (
	"context"
	"errors"
	tasks "github.com/ex-business/ExCommission/internal/affiliatetasks"
	principal "github.com/ex-business/ExCommission/internal/identity/domain"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// 采用真实 MySQL 事务与原审批接口验证调度边界；测试任务执行器只注入故障，不写佣金结果。
func affiliateTasksAcceptance(t *testing.T, ctx context.Context, store *mysql.Store, scope foundation.Scope, base time.Time, clock *atomic.Int64, op, checker string, call func(string, string, any, int, string, map[string]string) map[string]any) {
	t.Helper()
	manager := affiliateTaskManager(store)
	key := tasks.Key{Program: "test-task", Revision: 1, Day: base.Format("2006-01-02"), Timezone: "UTC", Due: base.Add(-time.Hour)}
	report := tasks.Report{Status: tasks.WaitingSource, Phase: "CHECK_WATERMARKS", Missing: []tasks.Missing{{Source: "sandbox", Day: key.Day}}, Reason: "缺少来源水位"}
	inspect := func(context.Context) (tasks.Report, error) { return report, nil }
	var executions atomic.Int64
	execute := func(context.Context) error { executions.Add(1); return errors.New("测试瞬时故障") }
	run := func() {
		t.Helper()
		if e := manager.Run(ctx, scope, key, inspect, execute); e != nil {
			t.Fatal(e)
		}
	}
	get := func() tasks.Record {
		t.Helper()
		r, e := manager.Repo.Get(ctx, store.DB, scope, key.ID(), false)
		if e != nil {
			t.Fatal(e)
		}
		return r
	}
	manager.Resolve = func(context.Context, foundation.Scope, tasks.Key) (tasks.Inspector, tasks.Executor, error) {
		return inspect, execute, nil
	}
	p := principal.Principal{Tenant: scope.Tenant, App: scope.App, Subject: "local-operator"}
	run()
	run()
	if r := get(); r.Status != tasks.WaitingSource || r.Attempts != 0 || len(r.Missing) != 1 {
		t.Fatal("缺水位不应尝试资金处理", r)
	}
	// 告警配置必须由另一人审批，既有申请人不能批准自己的申请。
	rules := tasks.DefaultRules()
	rules.WatermarkMinutes = 1
	rules.FailureCount = 1
	rules.MaxAttempts = 1
	change := tasks.RuleChange{Revision: 0, Rules: rules, Reason: "验证任务告警"}
	pending := call("/v1/affiliate/task-alert-config-requests", op, change, 202, "", nil)
	body := map[string]any{"decision": "APPROVE", "expected_revision": 0, "object_hash": pending["object_hash"], "reason": "独立复核"}
	path := "/v1/approvals/" + pending["approval_id"].(string) + "/decisions"
	call(path, op, body, 403, "", nil)
	call(path, checker, body, 200, "", nil)
	call("/v1/affiliate/task-alert-config-requests", checker, change, 403, "", nil)
	clock.Store(clockUTC().Add(2 * time.Minute).UnixNano())
	run()
	run()
	alerts, e := manager.Repo.Alerts(ctx, store.DB, scope)
	if e != nil {
		t.Fatal(e)
	}
	counts := map[string]int{}
	for _, a := range alerts {
		counts[a.Kind+":"+a.State]++
	}
	if counts["WATERMARK:TRIGGERED"] != 1 {
		t.Fatal("水位告警未去重", alerts)
	}
	// 人工请求只排队原任务，依赖未齐时保留一次检查历史。
	out, e := manager.Retry(ctx, p, key.ID(), "来源补发后检查", "retry-wait", "task-request")
	if e != nil || out.Status != 202 {
		t.Fatal(out, e)
	}
	run()
	if r := get(); r.RetryRequested || r.Attempts != 1 || r.Status != tasks.WaitingSource || executions.Load() != 0 {
		t.Fatal("缺水位重试绕过校验", r)
	}
	report = tasks.Report{Status: tasks.Ready, Phase: "READY"}
	run()
	if r := get(); r.Status != tasks.Failed || r.Failures != 1 || r.NextRetry == nil {
		t.Fatal("失败未留痕", r)
	}
	before := executions.Load()
	run()
	clock.Store(clockUTC().Add(time.Hour).UnixNano())
	run()
	if executions.Load() != before {
		t.Fatal("预算或退避未拦截自动重试")
	}
	// 达到自动预算后人工重试仍需重新检查状态，不能绕过停止方案。
	report.Status = tasks.Blocked
	if denied, err := manager.Retry(ctx, p, key.ID(), "不可强制执行", "retry-blocked", "request-blocked"); err == nil && denied.Status < 400 {
		t.Fatal("停止状态允许重试")
	}
	report.Status = tasks.Ready
	out, e = manager.Retry(ctx, p, key.ID(), "人工恢复原任务", "retry-failed", "request-manual")
	if e != nil || out.Status != 202 {
		t.Fatal(out, e)
	}
	execute = func(c context.Context) error {
		executions.Add(1)
		tasks.Progress(c, "CALCULATING", 2, 3)
		report = tasks.Report{Status: tasks.Succeeded, Phase: "SETTLED", Completed: 3, Total: 3}
		return errors.New("提交成功后的连接中断")
	}
	run()
	run()
	if r := get(); r.Status != tasks.Succeeded || r.Attempts != 3 || r.Failures != 0 || r.Total != 3 {
		t.Fatal("原日结已成功仍重复执行", r)
	}
	history, e := manager.Repo.Attempts(ctx, store.DB, scope, key.ID())
	if e != nil || history[0].Trigger != "MANUAL" || history[0].Actor != p.Subject {
		t.Fatal("人工操作审计丢失", history, e)
	}
	alerts, _ = manager.Repo.Alerts(ctx, store.DB, scope)
	counts = map[string]int{}
	for _, a := range alerts {
		counts[a.Kind+":"+a.State]++
	}
	if counts["WATERMARK:RESOLVED"] != 1 || counts["FAILURE:RESOLVED"] != 1 {
		t.Fatal("告警恢复缺失", alerts)
	}
	// API 不返回租约凭据，作用域由会话决定；只读/审批角色不得提交重试。
	handler, e := businessHandler(ctx, store)
	if e != nil {
		t.Fatal(e)
	}
	req := httptest.NewRequest("GET", "/v1/affiliate/task-runs/"+key.ID(), nil)
	req.Header.Set("Authorization", "Bearer "+op)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != 200 || strings.Contains(w.Body.String(), "lease_token") {
		t.Fatal("详情或凭据边界错误", w.Code, w.Body)
	}
	call("/v1/affiliate/task-runs/"+key.ID()+"/retry-requests", checker, map[string]string{"reason": "审批角色不能重试"}, 403, "", nil)
	viewer := principal.Principal{Tenant: scope.Tenant, App: scope.App, Subject: "task-readonly"}
	if err := bootstrapFeature(ctx, store, scope.Tenant, scope.App, viewer.Subject, "task-viewer", []string{"tasks:read"}); err != nil {
		t.Fatal(err)
	}
	if denied, err := manager.Retry(ctx, viewer, key.ID(), "只读账号不可重试", "viewer-retry", "viewer-request"); err == nil && denied.Status != 403 {
		t.Fatal("只读账号重试未拒绝", denied)
	}
	if denied, err := manager.RequestRules(ctx, viewer, change, "viewer-config", "viewer-config-request"); err == nil && denied.Status != 403 {
		t.Fatal("只读账号配置未拒绝", denied)
	}
	other := scope
	other.Tenant += "-other"
	if _, e := manager.Repo.Get(ctx, store.DB, other, key.ID(), false); !errors.Is(e, tasks.ErrMissing) {
		t.Fatal("跨站查询泄露", e)
	}

	t.Run("并发租约", func(t *testing.T) {
		k := key
		k.Program = "concurrent-task"
		var committed atomic.Bool
		var calls atomic.Int64
		check := func(context.Context) (tasks.Report, error) {
			if committed.Load() {
				return tasks.Report{Status: tasks.Succeeded}, nil
			}
			return tasks.Report{Status: tasks.Ready}, nil
		}
		started, release, finished := make(chan struct{}), make(chan struct{}), make(chan error, 1)
		exec := func(context.Context) error {
			calls.Add(1)
			close(started)
			<-release
			committed.Store(true)
			return nil
		}
		go func() { finished <- manager.Run(ctx, scope, k, check, exec) }()
		<-started
		if e := manager.Run(ctx, scope, k, check, exec); e != nil {
			t.Fatal(e)
		}
		close(release)
		if e := <-finished; e != nil {
			t.Fatal(e)
		}
		if calls.Load() != 1 {
			t.Fatal("并发重复执行")
		}
	})
	t.Run("取消后仍记录失败", func(t *testing.T) {
		k := key
		k.Program = "cancelled-task"
		cancelled, cancel := context.WithCancel(ctx)
		defer cancel()
		e := manager.Run(cancelled, scope, k, func(context.Context) (tasks.Report, error) { return tasks.Report{Status: tasks.Ready}, nil }, func(c context.Context) error { cancel(); return c.Err() })
		if e != nil {
			t.Fatal(e)
		}
		r, e := manager.Repo.Get(ctx, store.DB, scope, k.ID(), false)
		if e != nil || r.Status != tasks.Failed || r.LeaseToken != "" || r.LastError == "" {
			t.Fatal("取消后运行记录未收口", r, e)
		}
	})
	t.Run("旧租约不能覆盖恢复结果", func(t *testing.T) {
		k := key
		k.Program = "fenced-task"
		var settled atomic.Bool
		inspect := func(context.Context) (tasks.Report, error) {
			if settled.Load() {
				return tasks.Report{Status: tasks.Succeeded}, nil
			}
			return tasks.Report{Status: tasks.Ready}, nil
		}
		entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
		go func() {
			done <- manager.Run(ctx, scope, k, inspect, func(context.Context) error { close(entered); <-release; return errors.New("旧执行器延迟返回") })
		}()
		<-entered
		clock.Store(clockUTC().Add(3 * time.Minute).UnixNano())
		e := manager.Run(ctx, scope, k, inspect, func(context.Context) error { settled.Store(true); return nil })
		close(release)
		if e != nil {
			t.Fatal(e)
		}
		if e = <-done; e != nil {
			t.Fatal(e)
		}
		r, e := manager.Repo.Get(ctx, store.DB, scope, k.ID(), false)
		if e != nil || r.Status != tasks.Succeeded || r.Attempts != 2 {
			t.Fatal("旧令牌覆盖新结果", r, e)
		}
		history, e := manager.Repo.Attempts(ctx, store.DB, scope, k.ID())
		if e != nil || len(history) != 2 || history[0].Status != tasks.Succeeded || history[1].Status != "INTERRUPTED" {
			t.Fatal("租约历史丢失", history, e)
		}
	})

	t.Run("过期租约先确认原结果", func(t *testing.T) {
		k := key
		k.Program = "interrupted-task"
		e := store.Within(ctx, func(tx db.Executor) error {
			if e := manager.Repo.Ensure(ctx, tx, scope, k, clockUTC()); e != nil {
				return e
			}
			r, e := manager.Repo.Get(ctx, tx, scope, k.ID(), true)
			if e != nil {
				return e
			}
			expired := clockUTC().Add(-time.Minute)
			r.LeaseToken = "private-lease"
			r.LeaseUntil = &expired
			r.Status = tasks.Running
			r.Attempts = 1
			if e = manager.Repo.Save(ctx, tx, scope, r); e != nil {
				return e
			}
			return manager.Repo.Attempt(ctx, tx, scope, tasks.Attempt{TaskID: k.ID(), Number: 1, Status: tasks.Running, Trigger: "MANUAL", Actor: "original-operator", Reason: "原始恢复原因", Started: expired})
		})
		if e != nil {
			t.Fatal(e)
		}
		e = manager.Run(ctx, scope, k, func(context.Context) (tasks.Report, error) { return tasks.Report{Status: tasks.Succeeded}, nil }, func(context.Context) error { t.Error("已提交日结重复执行"); return nil })
		if e != nil {
			t.Fatal(e)
		}
		a, e := manager.Repo.Attempts(ctx, store.DB, scope, k.ID())
		if e != nil || len(a) != 1 || a[0].Status != tasks.Succeeded || a[0].Actor != "original-operator" || a[0].Reason != "原始恢复原因" {
			t.Fatal("中断恢复覆盖原审计", a, e)
		}
	})
}
