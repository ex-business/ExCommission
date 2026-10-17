package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	audit "github.com/ex-business/ExCommission/internal/audit/adapters"
	calc "github.com/ex-business/ExCommission/internal/calculation/domain"
	identity "github.com/ex-business/ExCommission/internal/identity/domain"
	ledger "github.com/ex-business/ExCommission/internal/ledger/adapters"
	catalog "github.com/ex-business/ExCommission/internal/platform/adapters/catalog"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	repo "github.com/ex-business/ExCommission/internal/settlement/adapters"
	model "github.com/ex-business/ExCommission/internal/settlement/domain"
	workflowrepo "github.com/ex-business/ExCommission/internal/workflow/adapters"
	workflow "github.com/ex-business/ExCommission/internal/workflow/application"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestDailyScheduleAtomicApprovalsConcurrencyAndCatchup(t *testing.T) {
	if os.Getenv("TEST_MYSQL_ADDRESS") == "" {
		t.Skip("Docker MySQL required")
	}
	ctx := context.Background()
	store, err := mysql.Open(ctx, os.Getenv("TEST_MYSQL_ADDRESS"), "excommission", os.Getenv("MYSQL_PASSWORD"), "excommission")
	if err != nil {
		t.Fatal(err)
	}
	defer store.DB.Close()
	if err = store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	scope := foundation.Scope{Tenant: fmt.Sprint("test-schedule-", time.Now().UnixNano()), App: "app"}
	defer func() {
		rows, e := store.DB.Query("SELECT TABLE_NAME FROM information_schema.columns WHERE TABLE_SCHEMA=DATABASE() AND COLUMN_NAME='tenant_id'")
		if e != nil {
			t.Error(e)
			return
		}
		var tables []string
		for rows.Next() {
			var table string
			_ = rows.Scan(&table)
			tables = append(tables, table)
		}
		rows.Close()
		for _, table := range tables {
			if _, e = store.DB.Exec("DELETE FROM "+table+" WHERE tenant_id=?", scope.Tenant); e != nil {
				t.Error(e)
			}
		}
	}()
	if err = store.Within(ctx, func(tx db.Executor) error { return (catalog.Repository{}).Bootstrap(ctx, tx, scope) }); err != nil {
		t.Fatal(err)
	}
	var clock atomic.Int64
	at := time.Date(2026, 10, 3, 0, 59, 59, 0, time.UTC)
	clock.Store(at.UnixNano())
	now := func() time.Time { return time.Unix(0, clock.Load()).UTC() }
	var serial atomic.Int64
	id := func() string { return fmt.Sprint("schedule-id-", serial.Add(1)) }
	service := Service{Commands: store, Query: store.DB, Repo: repo.Repository{}, Ledger: ledger.Repository{}, Workflow: workflowrepo.Repository{}, Calendar: catalog.Repository{}, Now: now, ID: id}
	failing := false
	scheduler := Scheduler{UOW: store, Service: service, Repo: repo.Repository{}, History: workflowrepo.Repository{}, Calendars: catalog.Repository{}, Now: now, ID: id, Audit: func(ctx context.Context, tx db.Executor, s foundation.Scope, action, ref, code string) error {
		if failing {
			return errors.New("audit offline")
		}
		metadata := foundation.RequestFrom(ctx)
		return audit.Append(ctx, tx, audit.Entry{Tenant: s.Tenant, App: s.App, ID: id(), Subject: metadata.Subject, RequestID: metadata.RequestID, Type: "OPERATION", Action: action, Resource: ref, Summary: map[string]string{"result_code": code}, At: now()})
	}}
	post := func(taskID, agent, status string) {
		t.Helper()
		task := calc.Task{ID: taskID, Source: "source", BusinessID: taskID, EventType: "FEE_CONFIRMED"}
		input := calc.Input{TaskID: taskID, PlanVersionID: "pv", Facts: calc.Facts{BusinessTime: time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)}, Ancestors: []calc.Actor{{ID: agent, Status: status}}}
		input.Plan.Currency = "USD"
		result := calc.Result{TaskID: taskID, Status: "SUCCEEDED", Items: []calc.Item{{RuleID: "rule", BeneficiaryID: agent, Currency: "USD", Minor: "100"}}}
		if e := store.Within(ctx, func(tx db.Executor) error {
			return (ledger.Repository{}).Commit(ctx, tx, scope, task, input, result, now())
		}); e != nil {
			t.Fatal(e)
		}
	}
	count := func(table string) int {
		t.Helper()
		var n int
		if e := store.DB.QueryRow("SELECT COUNT(*) FROM "+table+" WHERE tenant_id=? AND app_id=?", scope.Tenant, scope.App).Scan(&n); e != nil {
			t.Fatal(e)
		}
		return n
	}
	post("earned", "agent-a", "ACTIVE")
	post("frozen", "agent-frozen", "FROZEN")
	if done, e := scheduler.RunOnce(ctx, scope); e != nil || done {
		t.Fatal("early execution", done, e)
	}
	if count("settlement_runs") != 0 {
		t.Fatal("generated before 01:00")
	}
	clock.Store(at.Add(time.Second).UnixNano())
	var wg sync.WaitGroup
	var completed atomic.Int64
	for range 100 {
		wg.Go(func() {
			done, e := scheduler.RunOnce(ctx, scope)
			if e != nil {
				t.Error(e)
			}
			if done {
				completed.Add(1)
			}
		})
	}
	wg.Wait()
	if completed.Load() != 1 || count("settlement_runs") != 1 || count("workflow_requests") != 1 {
		t.Fatal("duplicate period/approval", completed.Load())
	}
	state, e := (repo.Repository{}).Schedule(ctx, store.DB, scope)
	if e != nil || state.NextPeriod != "2026-10-03" || len(state.Last.ApprovalIDs) != 1 {
		t.Fatal(state, e)
	}
	a, e := (workflowrepo.Repository{}).Get(ctx, store.DB, scope, state.Last.ApprovalIDs[0], false)
	if e != nil || a.Applicant != "system-settlement" || a.Status != "PENDING" {
		t.Fatal(a, e)
	}
	var pending, available string
	if e = store.DB.QueryRow("SELECT CAST(SUM(pending) AS CHAR),CAST(SUM(available) AS CHAR) FROM ledger_entitlements WHERE tenant_id=?", scope.Tenant).Scan(&pending, &available); e != nil || pending != "200" || available != "0" {
		t.Fatal("auto approved money", pending, available, e)
	}
	wf := workflow.Service{Commands: store, Query: store.DB, Repo: workflowrepo.Repository{}, Now: now, Executors: map[string]workflow.Executor{"STATEMENT": service.Execute}, Rejections: map[string]workflow.Executor{"STATEMENT": service.Reject}}
	decision := workflow.Decision{Decision: "APPROVE", Revision: a.Revision, ObjectHash: a.ObjectHash, Reason: "复核昨日佣金"}
	self, e := wf.Decide(ctx, identity.Principal{Tenant: scope.Tenant, App: scope.App, Subject: "system-settlement"}, a.ID, decision, id(), id())
	if e != nil || self.Status != 403 {
		t.Fatal("self approval allowed", self.Status, e)
	}
	checked, e := wf.Decide(ctx, identity.Principal{Tenant: scope.Tenant, App: scope.App, Subject: "checker"}, a.ID, decision, id(), id())
	if e != nil || checked.Status != 200 {
		t.Fatal(checked.Status, e)
	}
	// Restart at a later date; an audit failure rolls back generation, claims, approval and cursor.
	clock.Store(time.Date(2026, 10, 4, 1, 0, 0, 0, time.UTC).UnixNano())
	post("late-earned", "agent-late", "ACTIVE")
	failing = true
	if done, e := scheduler.RunOnce(ctx, scope); e == nil || done {
		t.Fatal("audit failure committed", done, e)
	}
	state, _ = (repo.Repository{}).Schedule(ctx, store.DB, scope)
	if state.NextPeriod != "2026-10-03" || count("settlement_runs") != 1 || count("workflow_requests") != 1 || count("settlement_claims") != 1 {
		t.Fatal("partial scheduled write", state)
	}
	failing = false
	restarted := scheduler
	if done, e := restarted.RunOnce(ctx, scope); e != nil || !done {
		t.Fatal(done, e)
	}
	state, _ = (repo.Repository{}).Schedule(ctx, store.DB, scope)
	if len(state.Last.ApprovalIDs) != 1 {
		t.Fatal(state)
	}
	lateApproval, e := (workflowrepo.Repository{}).Get(ctx, store.DB, scope, state.Last.ApprovalIDs[0], false)
	if e != nil {
		t.Fatal(e)
	}
	decision.ObjectHash = lateApproval.ObjectHash
	decision.Revision = lateApproval.Revision
	if result, e := wf.Decide(ctx, identity.Principal{Tenant: scope.Tenant, App: scope.App, Subject: "checker"}, lateApproval.ID, decision, id(), id()); e != nil || result.Status != 200 {
		t.Fatal(result.Status, e)
	}
	// Process every missed empty day, without manufacturing zero-amount approvals.
	clock.Store(time.Date(2026, 10, 7, 1, 0, 0, 0, time.UTC).UnixNano())
	for range 3 {
		if done, e := restarted.RunOnce(ctx, scope); e != nil || !done {
			t.Fatal(done, e)
		}
	}
	if done, e := restarted.RunOnce(ctx, scope); e != nil || done {
		t.Fatal("repeated last day", done, e)
	}
	if count("settlement_runs") != 5 || count("workflow_requests") != 2 || count("audit_entries") != 5 {
		t.Fatal("missed empty period or extra approvals")
	}
	// Reuse a manual full run; historical rejection remains a single review for that statement.
	clock.Store(time.Date(2026, 10, 8, 1, 0, 0, 0, time.UTC).UnixNano())
	post("manual-earned", "agent-manual", "ACTIVE")
	maker := identity.Principal{Tenant: scope.Tenant, App: scope.App, Subject: "maker"}
	run, e := service.Run(ctx, maker, RunInput{Period: "2026-10-07", Calendar: "UTC-v1"}, id(), id())
	if e != nil || run.Status != 202 {
		t.Fatal(run.Status, e)
	}
	var envelope struct {
		Data struct {
			Statements []model.Statement `json:"statements"`
		} `json:"data"`
	}
	_ = json.Unmarshal(run.Body, &envelope)
	manual := envelope.Data.Statements[0]
	submitted, e := service.Submit(ctx, maker, manual.ID, Submission{Revision: manual.Revision, Hash: manual.Hash}, id(), id())
	if e != nil || submitted.Status != 202 {
		t.Fatal(submitted.Status, e)
	}
	request, e := (workflowrepo.Repository{}).Get(ctx, store.DB, scope, submitted.ResourceID, false)
	if e != nil {
		t.Fatal(e)
	}
	rejected, e := wf.Decide(ctx, identity.Principal{Tenant: scope.Tenant, App: scope.App, Subject: "checker"}, request.ID, workflow.Decision{Decision: "REJECT", Revision: request.Revision, ObjectHash: request.ObjectHash, Reason: "人工退回"}, id(), id())
	if e != nil || rejected.Status != 200 {
		t.Fatal(e, rejected.Status)
	}
	before := count("workflow_requests")
	if done, e := scheduler.RunOnce(ctx, scope); e != nil || !done {
		t.Fatal(done, e)
	}
	if count("workflow_requests") != before {
		t.Fatal("reopened rejected statement")
	}
	state, _ = (repo.Repository{}).Schedule(ctx, store.DB, scope)
	if len(state.Last.ApprovalIDs) != 0 {
		t.Fatal(state)
	}
	// An unrelated scope without a registered calendar fails closed, without touching this scope.
	other := scope
	other.App = "other"
	if done, e := scheduler.RunOnce(ctx, other); e == nil || done {
		t.Fatal("missing calendar accepted")
	}

	failedState, e := (repo.Repository{}).Schedule(ctx, store.DB, other)
	if e != nil || failedState.NextPeriod != "2026-10-07" {
		t.Fatal("failed first registration lost cursor", failedState, e)
	}
	clock.Store(time.Date(2026, 10, 9, 1, 0, 0, 0, time.UTC).UnixNano())
	if done, e := scheduler.RunOnce(ctx, other); e == nil || done {
		t.Fatal("missing calendar unexpectedly recovered")
	}
	failedState, _ = (repo.Repository{}).Schedule(ctx, store.DB, other)
	if failedState.NextPeriod != "2026-10-07" {
		t.Fatal("failed period skipped overnight", failedState)
	}
	if e = store.Within(ctx, func(tx db.Executor) error { return (catalog.Repository{}).Bootstrap(ctx, tx, other) }); e != nil {
		t.Fatal(e)
	}
	if done, e := scheduler.RunOnce(ctx, other); e != nil || !done {
		t.Fatal(done, e)
	}
	failedState, _ = (repo.Repository{}).Schedule(ctx, store.DB, other)
	if failedState.Last.Period != "2026-10-07" {
		t.Fatal("did not catch up first failed day", failedState)
	}
	// Existing filtered manual runs fail without claiming completion or moving the cursor.
	filtered := scope
	filtered.App = "filtered"
	if e = store.Within(ctx, func(tx db.Executor) error { return (catalog.Repository{}).Bootstrap(ctx, tx, filtered) }); e != nil {
		t.Fatal(e)
	}
	result, e := service.Run(ctx, identity.Principal{Tenant: filtered.Tenant, App: filtered.App, Subject: "maker"}, RunInput{Period: "2026-10-08", Calendar: "UTC-v1", AgentIDs: []string{"only-one-agent"}}, id(), id())
	if e != nil || result.Status != 202 {
		t.Fatal(result.Status, e)
	}
	if done, e := scheduler.RunOnce(ctx, filtered); e == nil || done {
		t.Fatal("manual filter silently changed", done, e)
	}
	filteredState, e := (repo.Repository{}).Schedule(ctx, store.DB, filtered)
	if e != nil || filteredState.NextPeriod != "2026-10-08" || filteredState.Last != nil {
		t.Fatal(filteredState, e)
	}
	if count("settlement_runs") != 6 {
		t.Fatal("scope leakage")
	}
	t.Log("UTC boundary, concurrent100, atomic audit rollback, restart catchup, empty days, manual rejection reuse and independent approval PASS")
}
