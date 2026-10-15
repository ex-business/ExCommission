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
	"testing"
	"time"
)

func TestMinimumPayoutCarriesPendingAndSmallAvailableForward(t *testing.T) {
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
	s := foundation.Scope{Tenant: fmt.Sprint("test-minimum-", time.Now().UnixNano()), App: "app"}
	defer func() {
		for _, table := range []string{"settlement_claims", "settlement_payout_policies", "settlement_items", "settlement_statements", "settlement_runs", "settlement_spaces", "workflow_requests", "command_results", "ledger_payout_policies", "ledger_journal_links", "ledger_restrictions", "ledger_journals", "ledger_entitlements", "transaction_outbox"} {
			store.DB.Exec("DELETE FROM "+table+" WHERE tenant_id=?", s.Tenant)
		}
	}()
	now := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	count := 0
	id := func() string { count++; return fmt.Sprint("id", count) }
	p := identity.Principal{Tenant: s.Tenant, App: s.App, Subject: "maker"}
	post := func(task, amount string) {
		t.Helper()
		input := calc.Input{TaskID: task, PlanVersionID: "pv", Facts: calc.Facts{BusinessTime: now.Add(-9 * 24 * time.Hour)}}
		input.Plan.Currency = "USD"
		input.Plan.Settlement.Minimum = "150"
		maximum := "200"
		input.Plan.Settlement.Maximum = &maximum
		result := calc.Result{TaskID: task, Status: "SUCCEEDED", Items: []calc.Item{{RuleID: "r", BeneficiaryID: "a", Currency: "USD", Minor: amount}}}
		if err := store.Within(ctx, func(tx db.Executor) error {
			return (ledger.Repository{}).Commit(ctx, tx, s, calc.Task{ID: task, Source: "s", BusinessID: task, EventType: "FEE_CONFIRMED"}, input, result, now)
		}); err != nil {
			t.Fatal(err)
		}
	}
	service := Service{Commands: store, Query: store.DB, Repo: repo.Repository{}, Ledger: ledger.Repository{}, Workflow: workflowrepo.Repository{}, Now: func() time.Time { return now }, ID: id}
	run := func(period string) []model.Statement {
		t.Helper()
		r, err := service.Run(ctx, p, RunInput{Period: period, Calendar: "UTC-v1"}, id(), id())
		if err != nil || r.Status != 202 {
			t.Fatal(err, string(r.Body))
		}
		var answer struct {
			Data struct {
				Statements []model.Statement `json:"statements"`
			}
		}
		json.Unmarshal(r.Body, &answer)
		return answer.Data.Statements
	}
	post("one", "100")
	if rows := run("2026-10-02"); len(rows) != 0 {
		t.Fatal("below minimum was settled", rows)
	}
	post("two", "100")
	rows := run("2026-10-03")
	if len(rows) != 1 || rows[0].Total != "200" || rows[0].Minimum != "150" || rows[0].Maximum == nil || *rows[0].Maximum != "200" {
		t.Fatal("minimum aggregation", rows)
	}
	submitted, err := service.Submit(ctx, p, rows[0].ID, Submission{Revision: 0, Hash: rows[0].Hash}, id(), id())
	if err != nil {
		t.Fatal(err)
	}
	var a struct {
		Data struct {
			ID   string `json:"approval_id"`
			Hash string `json:"object_hash"`
		}
	}
	json.Unmarshal(submitted.Body, &a)
	wf := workflow.Service{Commands: store, Query: store.DB, Repo: workflowrepo.Repository{}, Now: service.Now, Executors: map[string]workflow.Executor{"STATEMENT": service.Execute}}
	checker := p
	checker.Subject = "checker"
	approved, err := wf.Decide(ctx, checker, a.Data.ID, workflow.Decision{Decision: "APPROVE", ObjectHash: a.Data.Hash, Reason: "checked"}, id(), id())
	if err != nil || approved.Status != 200 {
		t.Fatal(err, string(approved.Body))
	}
	for index, item := range rows[0].Items {
		amount := "100"
		if index == 1 {
			amount = "50"
		}
		if err = store.Within(ctx, func(tx db.Executor) error {
			if err := (ledger.Repository{}).Transfer(ctx, tx, s, item.ID, "available", "reserved", amount, "reserve-"+item.ID, now); err != nil {
				return err
			}
			return (ledger.Repository{}).Transfer(ctx, tx, s, item.ID, "reserved", "paid", amount, "paid-"+item.ID, now)
		}); err != nil {
			t.Fatal(err)
		}
	}
	post("three", "150")
	next := run("2026-10-04")
	if len(next) != 1 || next[0].Total != "200" || len(next[0].Items) != 2 {
		t.Fatal("small available not carried forward", next)
	}
}
