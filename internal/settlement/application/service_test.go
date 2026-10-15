package application

import (
	"context"
	"encoding/json"
	"fmt"
	calc "github.com/ex-business/ExCommission/internal/calculation/domain"
	identity "github.com/ex-business/ExCommission/internal/identity/domain"
	ledger "github.com/ex-business/ExCommission/internal/ledger/adapters"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	repo "github.com/ex-business/ExCommission/internal/settlement/adapters"
	model "github.com/ex-business/ExCommission/internal/settlement/domain"
	workflowrepo "github.com/ex-business/ExCommission/internal/workflow/adapters"
	workflow "github.com/ex-business/ExCommission/internal/workflow/application"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

func TestSettlementIndependentApprovalClaimsAndStaleFreeze(t *testing.T) {
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
	scope := foundation.Scope{Tenant: fmt.Sprint("test-statement-", time.Now().UnixNano()), App: "app"}
	defer func() {
		for _, table := range []string{"settlement_claims", "settlement_item_targets", "settlement_payout_policies", "settlement_items", "settlement_statements", "settlement_runs", "settlement_spaces", "workflow_requests", "command_results", "ledger_payout_policies", "ledger_journal_links", "ledger_restrictions", "ledger_journals", "ledger_entitlements", "transaction_outbox"} {
			store.DB.Exec("DELETE FROM "+table+" WHERE tenant_id=?", scope.Tenant)
		}
	}()
	at := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	var counter atomic.Int64
	id := func() string { return fmt.Sprint("id", counter.Add(1)) }
	p := identity.Principal{Tenant: scope.Tenant, App: scope.App, Subject: "maker"}
	checker := p
	checker.Subject = "checker"
	service := Service{Commands: store, Query: store.DB, Repo: repo.Repository{}, Ledger: ledger.Repository{}, Workflow: workflowrepo.Repository{}, Now: func() time.Time { return at }, ID: id}
	wf := workflow.Service{Commands: store, Query: store.DB, Repo: workflowrepo.Repository{}, Now: service.Now, Executors: map[string]workflow.Executor{"STATEMENT": service.Execute}, Rejections: map[string]workflow.Executor{"STATEMENT": service.Reject}, Failures: map[string]workflow.Executor{"STATEMENT": service.Reject}}
	post := func(taskID, agent, status string) {
		t.Helper()
		task := calc.Task{ID: taskID, Source: "s", BusinessID: taskID, EventType: "FEE_CONFIRMED"}
		input := calc.Input{TaskID: taskID, PlanVersionID: "pv", Facts: calc.Facts{BusinessTime: at.Add(-9 * 24 * time.Hour)}, Ancestors: []calc.Actor{{ID: agent, Status: status}}}
		input.Plan.Currency = "USD"
		result := calc.Result{TaskID: taskID, Status: "SUCCEEDED", Items: []calc.Item{{RuleID: "r", BeneficiaryID: agent, Currency: "USD", Minor: "100"}}}
		if err := store.Within(ctx, func(tx db.Executor) error {
			return (ledger.Repository{}).Commit(ctx, tx, scope, task, input, result, at)
		}); err != nil {
			t.Fatal(err)
		}
	}
	post("t1", "a", "ACTIVE")
	post("t2", "b", "FROZEN")
	run := func(period string) []model.Statement {
		t.Helper()
		r, e := service.Run(ctx, p, RunInput{Period: period, Calendar: "UTC-v1"}, id(), id())
		if e != nil || r.Status != 202 {
			t.Fatalf("run %d %v %s", r.Status, e, r.Body)
		}
		var body struct {
			Data struct {
				Statements []model.Statement `json:"statements"`
			}
		}
		json.Unmarshal(r.Body, &body)
		return body.Data.Statements
	}
	first, second := run("2026-10-02"), run("2026-10-03")
	if len(first) != 1 || len(second) != 1 || first[0].AgentID != "a" {
		t.Fatal("freeze/period collection", first, second)
	}
	results := make(chan foundation.CommandResult, 2)
	errs := make(chan error, 2)
	for _, v := range []model.Statement{first[0], second[0]} {
		go func(v model.Statement) {
			r, e := service.Submit(ctx, p, v.ID, Submission{Revision: v.Revision, Hash: v.Hash}, id(), id())
			results <- r
			errs <- e
		}(v)
	}
	var approvalID, approvalHash string
	success := 0
	for i := 0; i < 2; i++ {
		r := <-results
		if e := <-errs; e != nil {
			t.Fatal(e)
		}
		if r.Status == 202 {
			success++
			var b struct {
				Data struct {
					ID   string `json:"approval_id"`
					Hash string `json:"object_hash"`
				}
			}
			json.Unmarshal(r.Body, &b)
			approvalID = b.Data.ID
			approvalHash = b.Data.Hash
		} else if r.Status != 409 {
			t.Fatal("unexpected claim response", r.Status, string(r.Body))
		}
	}
	if success != 1 {
		t.Fatal("duplicate entitlement claimed", success)
	}
	decision := workflow.Decision{Decision: "APPROVE", Revision: 0, ObjectHash: approvalHash, Reason: "verified"}
	r, e := wf.Decide(ctx, p, approvalID, decision, id(), id())
	if e != nil || r.Status != 403 {
		t.Fatalf("self approval %d %v %s", r.Status, e, r.Body)
	}
	r, e = wf.Decide(ctx, checker, approvalID, decision, id(), id())
	if e != nil || r.Status != 200 {
		t.Fatalf("independent approval %d %v %s", r.Status, e, r.Body)
	}
	a, _ := wf.Get(ctx, checker, approvalID)
	if a.Execution != "SUCCEEDED" {
		t.Fatal("approval execution failed", a)
	}
	v, e := service.Repo.Get(ctx, store.DB, scope, a.ObjectRef, false)
	if e != nil || v.Status != "APPROVED" {
		t.Fatal(v, e)
	}
	money, e := service.Ledger.Get(ctx, store.DB, scope, v.Items[0].ID, false)
	if e != nil || money.Pending != "0" || money.Available != "100" {
		t.Fatal("settlement journal not posted", money, e)
	}
	// Frozen earnings become collectible only after release, preserving original business time.
	if _, e = store.DB.Exec(`UPDATE ledger_restrictions SET closed_at=? WHERE tenant_id=?`, at, scope.Tenant); e != nil {
		t.Fatal(e)
	}
	next := run("2026-10-04")
	if len(next) != 1 || next[0].AgentID != "b" {
		t.Fatal("released late entitlement", next)
	}
	sub, e := service.Submit(ctx, p, next[0].ID, Submission{Revision: 0, Hash: next[0].Hash}, id(), id())
	if e != nil || sub.Status != 202 {
		t.Fatal(e, string(sub.Body))
	}
	var body struct {
		Data struct {
			ID   string `json:"approval_id"`
			Hash string `json:"object_hash"`
		}
	}
	json.Unmarshal(sub.Body, &body)
	if _, e = store.DB.Exec(`INSERT INTO ledger_restrictions VALUES(?,?,?,?,?,?,NULL)`, scope.Tenant, scope.App, next[0].Items[0].ID, "new-risk", "MANUAL", at); e != nil {
		t.Fatal(e)
	}
	decision.ObjectHash = body.Data.Hash
	r, e = wf.Decide(ctx, checker, body.Data.ID, decision, id(), id())
	if e != nil || r.Status != 200 {
		t.Fatal(e, string(r.Body))
	}
	a, e = wf.Get(ctx, checker, body.Data.ID)
	if e != nil || a.Status != "STALE" || a.Execution != "FAILED" {
		t.Fatal("stale freeze was approved", a, e)
	}
	money, e = service.Ledger.Get(ctx, store.DB, scope, next[0].Items[0].ID, false)
	if e != nil || money.Pending != "100" || money.Available != "0" {
		t.Fatal("stale approval moved money", money, e)
	}
	r, e = service.Run(ctx, p, RunInput{Period: "2026-09-30", Calendar: "UTC-v1"}, id(), id())
	if e != nil || r.Status != 409 {
		t.Fatal("past settlement period reopened", r.Status, e)
	}
	r, e = service.Run(ctx, p, RunInput{Period: "2026-10-02", Calendar: "UTC-v1", AgentIDs: []string{"a"}}, id(), id())
	if e != nil || r.Status != 409 {
		t.Fatal("existing run filter changed", r.Status, e)
	}
	post("late", "c", "ACTIVE")
	late := run("2026-10-05")
	if len(late) != 1 || late[0].AgentID != "c" {
		t.Fatal("late fact missing", late)
	}
}
