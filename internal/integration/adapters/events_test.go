package adapters_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	agents "github.com/ex-business/ExCommission/internal/agents/adapters"
	agent "github.com/ex-business/ExCommission/internal/agents/domain"
	taskrepo "github.com/ex-business/ExCommission/internal/calculation/adapters"
	worker "github.com/ex-business/ExCommission/internal/calculation/application"
	calculation "github.com/ex-business/ExCommission/internal/calculation/domain"
	identity "github.com/ex-business/ExCommission/internal/identity/domain"
	source "github.com/ex-business/ExCommission/internal/integration/adapters"
	integration "github.com/ex-business/ExCommission/internal/integration/application"
	event "github.com/ex-business/ExCommission/internal/integration/domain"
	ledger "github.com/ex-business/ExCommission/internal/ledger/adapters"
	plans "github.com/ex-business/ExCommission/internal/plans/adapters"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	users "github.com/ex-business/ExCommission/internal/users/adapters"
	user "github.com/ex-business/ExCommission/internal/users/domain"
	workflowrepo "github.com/ex-business/ExCommission/internal/workflow/adapters"
	workflow "github.com/ex-business/ExCommission/internal/workflow/application"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func database(t *testing.T) (*mysql.Store, foundation.Scope) {
	t.Helper()
	if os.Getenv("TEST_MYSQL_ADDRESS") == "" {
		t.Skip("Docker MySQL required")
	}
	store, err := mysql.Open(context.Background(), os.Getenv("TEST_MYSQL_ADDRESS"), "excommission", os.Getenv("MYSQL_PASSWORD"), "excommission")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Migrate(context.Background()); err != nil {
		store.DB.Close()
		t.Fatal(err)
	}
	scope := foundation.Scope{Tenant: "test-event-" + fmt.Sprint(time.Now().UnixNano()), App: "app"}
	t.Cleanup(func() {
		for _, table := range []string{"ledger_recoveries", "calculation_adjustments", "workflow_requests", "calculation_refund_order", "calculation_refunds", "calculation_refund_spaces", "ledger_debt_ages", "ledger_payout_policies", "ledger_journal_links", "ledger_restrictions", "ledger_journals", "ledger_entitlements", "transaction_outbox", "integration_event_keys", "integration_inbox", "integration_raw_payloads", "calculation_tasks", "calculation_recoveries", "command_results", "users_external", "agent_status_history", "agent_accounts", "agent_relations", "agent_relation_spaces", "agent_grade_history", "agent_plan_states", "plan_drafts", "plan_versions", "plan_route_spaces", "user_agent_bindings"} {
			if _, err := store.DB.Exec("DELETE FROM "+table+" WHERE tenant_id=?", scope.Tenant); err != nil {
				t.Error(err)
			}
		}
		store.DB.Close()
	})
	return store, scope
}
func signature(raw, key []byte) string {
	mac := hmac.New(sha256.New, key)
	mac.Write(raw)
	return hex.EncodeToString(mac.Sum(nil))
}
func TestSignedEventsIdempotencySnapshotAndLedgerGate(t *testing.T) {
	store, s := database(t)
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	key := []byte("01234567890123456789012345678901")
	provider, err := source.NewSource("sandbox", key)
	if err != nil {
		t.Fatal(err)
	}
	var counter atomic.Int64
	ids := func() string { return fmt.Sprintf("id%06d", counter.Add(1)) }
	tasks := taskrepo.Tasks{DB: store.DB, Within: store.Within}
	service := integration.Service{Commands: store, Query: store.DB, Repo: source.Repository{}, Tasks: tasks, Source: provider, Plans: plans.Repository{}, Now: func() time.Time { return at }, ID: ids}
	p := identity.Principal{Tenant: s.Tenant, App: s.App, Subject: "operator"}
	yes := true
	input := event.Event{Schema: "1", Source: "sandbox", ID: "E1", BusinessID: "B1", Type: "FEE_CONFIRMED", Time: at, ExternalUser: "trader", Product: "BTC-USDT", Scene: "SPOT", Revision: "1", Payload: event.Payload{Basis: "1000", Currency: "USD", Confirmed: &yes}}
	raw, _ := json.Marshal(input)
	if _, err = service.Receive(ctx, p, raw, "wrong-signature", "wrong", "r"); err == nil {
		t.Fatal("unsigned fact accepted")
	}
	var wg sync.WaitGroup
	failures := make(chan error, 100)
	var first foundation.CommandResult
	var mu sync.Mutex
	for range 100 {
		wg.Go(func() {
			result, err := service.Receive(ctx, p, raw, signature(raw, key), "same-key", "request")
			if err != nil {
				failures <- err
				return
			}
			if result.Status != 202 {
				failures <- fmt.Errorf("status %d", result.Status)
			}
			mu.Lock()
			first = result
			mu.Unlock()
		})
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	var accepted struct {
		Data struct {
			EventID string `json:"event_id"`
			TaskID  string `json:"task_id"`
		}
	}
	if err = json.Unmarshal(first.Body, &accepted); err != nil {
		t.Fatal(err)
	}
	input.ID = "E2"
	raw2, _ := json.Marshal(input)
	duplicate, err := service.Receive(ctx, p, raw2, signature(raw2, key), "new-event-id", "request")
	if err != nil || string(duplicate.Body) == "" {
		t.Fatal(err)
	}
	var alias struct {
		Data struct {
			TaskID string `json:"task_id"`
		}
	}
	json.Unmarshal(duplicate.Body, &alias)
	if alias.Data.TaskID != accepted.Data.TaskID {
		t.Fatal("business duplicated task")
	}
	input.BusinessID = "B2"
	changed, _ := json.Marshal(input)
	conflict, err := service.Receive(ctx, p, changed, signature(changed, key), "alias-cannot-be-reused", "request")
	if err != nil || conflict.Status != 409 {
		t.Fatalf("event alias reused for different business %d %v", conflict.Status, err)
	}
	var count int
	store.DB.QueryRow(`SELECT COUNT(*) FROM calculation_tasks WHERE tenant_id=?`, s.Tenant).Scan(&count)
	if count != 1 {
		t.Fatalf("tasks %d", count)
	}
	// 同时准备业务时点有效的方案、身份、关系和资格，全部为显式本地事实。
	fixture, err := os.ReadFile("../../../conformance/v1/plan-percent.json")
	if err != nil {
		t.Fatal(err)
	}
	validator, err := plans.NewValidator(map[string]int{"USD": 2}, map[string]bool{"UTC-v1": true})
	if err != nil {
		t.Fatal(err)
	}
	config, canonical, err := validator.Decode(fixture)
	if err != nil {
		t.Fatal(err)
	}
	err = store.Within(ctx, func(tx db.Executor) error {
		repo := plans.Repository{}
		hash := plans.Hash(canonical)
		if err := repo.Create(ctx, tx, s, config, canonical, hash, at.Add(-3*time.Hour)); err != nil {
			return err
		}
		if err := repo.SetPending(ctx, tx, s, config.PlanID, 0, hash); err != nil {
			return err
		}
		if err := repo.Publish(ctx, tx, s, config.PlanID, 0, hash, "PV1", at.Add(-2*time.Hour), at.Add(-3*time.Hour)); err != nil {
			return err
		}
		uid, err := users.Upsert(ctx, tx, s, "sandbox", user.Record{ExternalID: "trader", DisplayName: "交易用户"}, at.Add(-2*time.Hour))
		if err != nil {
			return err
		}
		agentRepo := agents.Repository{}
		if err = agentRepo.Create(ctx, tx, s, "trader-agent", uid, "业务自身", at.Add(-2*time.Hour)); err != nil {
			return err
		}
		if err = agentRepo.Create(ctx, tx, s, "parent", "parent-user", "直属", at.Add(-2*time.Hour)); err != nil {
			return err
		}
		parent := "parent"
		if err = agentRepo.Move(ctx, tx, s, agent.RelationChange{AgentID: "trader-agent", NewParentID: &parent}, at.Add(-time.Hour)); err != nil {
			return err
		}
		return agentRepo.ApplyGrade(ctx, tx, s, agent.GradeChange{AgentID: "parent", PlanID: "P1", GradeID: "G0", Eligibility: "ELIGIBLE"}, "MANUAL", "evidence", at.Add(-time.Hour))
	})
	if err != nil {
		t.Fatal(err)
	}
	refunds := worker.RefundService{Commands: store, UOW: store, Query: store.DB, Repo: taskrepo.Refunds{}, Adjustments: taskrepo.Adjustments{}, Tasks: tasks, Ledger: ledger.Repository{}, Now: func() time.Time { return at }, ID: ids}
	refundInput := calculation.Refund{Source: "sandbox", ID: "refund1", BusinessID: "B1", EventType: "FEE_CONFIRMED", Total: "400", Currency: "USD", Time: at, Reason: "confirmed cancellation"}
	refundRaw, _ := json.Marshal(refundInput)
	normalized, err := provider.NormalizeRefund(refundRaw, signature(refundRaw, key))
	if err != nil {
		t.Fatal(err)
	}
	receivedRefund, err := refunds.Receive(ctx, p, normalized, "refund-first", "request")
	if err != nil || receivedRefund.Status != 202 {
		t.Fatal("refund before original", err, string(receivedRefund.Body))
	}
	invalidBeforeOriginal := normalized
	invalidBeforeOriginal.ID = "invalid-before-original"
	invalidBeforeOriginal.Total = "2000"
	rejectedLater, err := refunds.Receive(ctx, p, invalidBeforeOriginal, "invalid-before-original-key", "request")
	if err != nil || rejectedLater.Status != 202 {
		t.Fatal(err, string(rejectedLater.Body))
	}
	processor := worker.Worker{UOW: store, Tasks: tasks, Resolver: worker.Resolver{Plans: plans.Repository{}, Users: users.Repository{}, Accounts: agents.Repository{}, Relationships: agents.Repository{}, Grades: agents.Repository{}, Contracts: agents.Repository{}, Exponents: map[string]int{"USD": 2}}, Now: func() time.Time { return at }, Lease: 30 * time.Second}
	if done, err := processor.RunOnce(ctx, s); err != nil || !done {
		t.Fatalf("worker %t %v", done, err)
	}
	task, err := tasks.Get(ctx, store.DB, s, accepted.Data.TaskID)
	if err != nil || task.Status != "SUSPENDED" || task.Reason == nil || *task.Reason != "LEDGER_NOT_READY" || len(task.Snapshot) == 0 {
		t.Fatalf("ledger gate %+v %v", task, err)
	}
	var snapshot calculation.Input
	if err = json.Unmarshal(task.Snapshot, &snapshot); err != nil {
		t.Fatal(err)
	}
	calculated := calculation.Calculate(snapshot)
	if calculated.Status != "SUCCEEDED" || len(calculated.Items) != 1 || calculated.Items[0].BeneficiaryID != "parent" || calculated.Items[0].Minor != "10000" || snapshot.PlanVersionID != "PV1" {
		t.Fatalf("wrong frozen snapshot %+v", calculated)
	}
	q := snapshot.QualificationFacts["parent"]
	if !q.Joined || q.Revision != 1 || q.Source != "MANUAL" || q.Evidence != "evidence" || q.From.IsZero() || len(snapshot.RelationshipFacts) != 1 || snapshot.RelationshipFacts[0].Depth != 1 {
		t.Fatal("missing frozen historical evidence", snapshot.QualificationFacts, snapshot.RelationshipFacts)
	}
	before := string(task.Snapshot)
	if err = store.Within(ctx, func(tx db.Executor) error {
		return tasks.Resume(ctx, tx, s, task.ID, task.Revision, "verified-source-evidence", at.Add(time.Second))
	}); err != nil {
		t.Fatal(err)
	}
	at = at.Add(time.Second)
	if done, err := processor.RunOnce(ctx, s); err != nil || !done {
		t.Fatal(err)
	}
	again, err := tasks.Get(ctx, store.DB, s, task.ID)
	if err != nil || string(again.Snapshot) != before {
		t.Fatal("resume changed frozen snapshot", err)
	}
	var recoveries int
	store.DB.QueryRow(`SELECT COUNT(*) FROM calculation_recoveries WHERE tenant_id=?`, s.Tenant).Scan(&recoveries)
	if recoveries != 1 {
		t.Fatal("repair evidence missing")
	}

	if err = store.Within(ctx, func(tx db.Executor) error {
		return tasks.Resume(ctx, tx, s, again.ID, again.Revision, "ledger-installed", at)
	}); err != nil {
		t.Fatal(err)
	}
	processor.Committer = ledger.Repository{}
	processor.TargetPolicy = refunds
	if done, err := processor.RunOnce(ctx, s); err != nil || !done {
		t.Fatal("formal posting failed", err)
	}
	final, err := tasks.Get(ctx, store.DB, s, task.ID)
	if err != nil || final.Status != "SUCCEEDED" || string(final.Snapshot) != before {
		t.Fatal("task and ledger not committed together", final.Status, err)
	}
	for _, table := range []string{"ledger_entitlements", "ledger_journals", "transaction_outbox"} {
		if err = store.DB.QueryRow("SELECT COUNT(*) FROM "+table+" WHERE tenant_id=?", s.Tenant).Scan(&count); err != nil || count != 1 {
			t.Fatal("formal ledger count", table, count, err)
		}
	}
	if done, err := processor.RunOnce(ctx, s); err != nil || done {
		t.Fatal("completed task was reclaimed", err)
	}
	entitlements, err := (ledger.Repository{}).TaskEntitlements(ctx, store.DB, s, task.ID)
	if err != nil || len(entitlements) != 1 || entitlements[0].Target != "6000" {
		t.Fatal("first final commission ignored prior refund", entitlements, err)
	}
	entitlementID := entitlements[0].ID
	invalidRecord, err := refunds.Repo.Get(ctx, store.DB, s, rejectedLater.ResourceID)
	if err != nil || invalidRecord.Status != "REJECTED" {
		t.Fatal("unbounded pre-original refund was not rejected", invalidRecord, err)
	}
	acceptRefund := func(id, total string) calculation.RefundRecord {
		t.Helper()
		refundInput.ID = id
		refundInput.Total = total
		r, e := refunds.Receive(ctx, p, refundInput, "refund-key-"+id, "request")
		if e != nil || r.Status != 202 {
			t.Fatal(e, string(r.Body))
		}
		v, e := refunds.Repo.Get(ctx, store.DB, s, r.ResourceID)
		if e != nil {
			t.Fatal(e)
		}
		if e = refunds.Process(ctx, s, v); e != nil {
			t.Fatal(e)
		}
		v, e = refunds.Repo.Get(ctx, store.DB, s, r.ResourceID)
		if e != nil {
			t.Fatal(e)
		}
		return v
	}
	if r := acceptRefund("refund2", "500"); r.Status != "SUCCEEDED" {
		t.Fatal(r)
	}
	if r := acceptRefund("refund-stale", "450"); r.Status != "STALE_NOOP" {
		t.Fatal(r)
	}
	refundInput.ID = "refund-excess"
	refundInput.Total = "1001"
	excess, e := refunds.Receive(ctx, p, refundInput, "excess-key", "request")
	if e != nil || excess.Status != 422 {
		t.Fatal("over refund accepted", excess.Status, e)
	}
	transfer := func(from, to, amount, ref string) {
		t.Helper()
		if err := store.Within(ctx, func(tx db.Executor) error {
			return (ledger.Repository{}).Transfer(ctx, tx, s, entitlementID, from, to, amount, ref, at)
		}); err != nil {
			t.Fatal(err)
		}
	}
	transfer("pending", "available", "5000", "settlement-test")
	transfer("available", "reserved", "1000", "payment-test")
	waiting := acceptRefund("refund3", "600")
	if waiting.Status != "WAITING_PAYMENT" {
		t.Fatal("unknown payment refund moved funds", waiting)
	}
	money, err := (ledger.Repository{}).Get(ctx, store.DB, s, entitlementID, false)
	if err != nil || money.Target != "5000" || !money.Frozen || money.Reserved != "1000" {
		t.Fatal(money, err)
	}
	transfer("reserved", "paid", "1000", "payment-confirmed")
	if err = refunds.Process(ctx, s, waiting); err != nil {
		t.Fatal(err)
	}
	money, err = (ledger.Repository{}).Get(ctx, store.DB, s, entitlementID, false)
	if err != nil || money.Target != "4000" || money.Frozen || money.Paid != "1000" || money.Available != "0" || money.Pending != "3000" {
		t.Fatal("refund resumed wrong buckets", money, err)
	}
	if r := acceptRefund("refund4", "1000"); r.Status != "SUCCEEDED" {
		t.Fatal(r)
	}
	money, err = (ledger.Repository{}).Get(ctx, store.DB, s, entitlementID, false)
	if err != nil || money.Target != "0" || money.Debt != "1000" || money.Paid != "1000" || money.Available != "0" {
		t.Fatal("paid history/debt conservation", money, err)
	}
	recovery := worker.RecoveryService{Commands: store, Query: store.DB, Ledger: ledger.Repository{}, Workflow: workflowrepo.Repository{}, Now: func() time.Time { return at }, ID: ids}
	adjustment := worker.AdjustmentService{Commands: store, Tasks: tasks, Repo: taskrepo.Adjustments{}, Refunds: refunds, Ledger: ledger.Repository{}, Workflow: workflowrepo.Repository{}, Now: func() time.Time { return at }, ID: ids}
	approvals := workflow.Service{Commands: store, Query: store.DB, Repo: workflowrepo.Repository{}, Now: func() time.Time { return at }, Executors: map[string]workflow.Executor{"RECOVERY": recovery.Execute, "ADJUSTMENT": adjustment.Execute}}
	checker := p
	checker.Subject = "independent-checker"
	approve := func(command foundation.CommandResult) {
		t.Helper()
		if command.Status != 202 {
			t.Fatal("approval request rejected", string(command.Body))
		}
		var body struct {
			Data struct {
				ID   string `json:"approval_id"`
				Hash string `json:"object_hash"`
			}
		}
		json.Unmarshal(command.Body, &body)
		decision := workflow.Decision{Decision: "APPROVE", ObjectHash: body.Data.Hash, Revision: 0, Reason: "verified independently"}
		self, e := approvals.Decide(ctx, p, body.Data.ID, decision, ids(), "r")
		if e != nil || self.Status != 403 {
			t.Fatal("self approval bypass", self.Status, e)
		}
		answer, e := approvals.Decide(ctx, checker, body.Data.ID, decision, ids(), "r")
		if e != nil || answer.Status != 200 {
			t.Fatal(e, string(answer.Body))
		}
		a, e := approvals.Get(ctx, checker, body.Data.ID)
		if e != nil || a.Execution != "SUCCEEDED" {
			t.Fatal("approval execution", a, e)
		}
	}
	// A future approved commission can offset only this agent and currency, after independent approval.
	futureInput := snapshot
	futureInput.TaskID = "future-task"
	futureTask := task
	futureTask.ID = "future-task"
	futureTask.BusinessID = "future-business"
	futureResult := calculation.Calculate(futureInput)
	if err = store.Within(ctx, func(tx db.Executor) error {
		return (ledger.Repository{}).Commit(ctx, tx, s, futureTask, futureInput, futureResult, at)
	}); err != nil {
		t.Fatal(err)
	}
	future, err := (ledger.Repository{}).TaskEntitlements(ctx, store.DB, s, "future-task")
	if err != nil || len(future) != 1 {
		t.Fatal(future, err)
	}
	if err = store.Within(ctx, func(tx db.Executor) error {
		return (ledger.Repository{}).Transfer(ctx, tx, s, future[0].ID, "pending", "available", "10000", "future-approved", at)
	}); err != nil {
		t.Fatal(err)
	}
	request := worker.RecoveryInput{DebtID: entitlementID, Method: "FUTURE_OFFSET", Evidence: "offset-proof", Reason: "approved offset", Revision: money.Revision}
	request.Amount.Currency = "USD"
	request.Amount.Minor = "600"
	rec, err := recovery.Request(ctx, p, request, ids(), "r")
	if err != nil {
		t.Fatal(err)
	}
	approve(rec)
	money, err = (ledger.Repository{}).Get(ctx, store.DB, s, entitlementID, false)
	if err != nil || money.Debt != "400" || money.Recovered != "600" || money.Paid != "1000" {
		t.Fatal("offset conservation", money, err)
	}
	proofFile := t.TempDir() + "/evidence.json"
	proofBody, _ := json.Marshal(map[string]any{"cash-proof": map[string]any{"tenant_id": s.Tenant, "app_id": s.App, "agent_id": "parent", "currency": "USD", "minor_units": "400", "confirmed": true}})
	if err = os.WriteFile(proofFile, proofBody, 0600); err != nil {
		t.Fatal(err)
	}
	recovery.Evidence = taskrepo.SandboxEvidence{File: proofFile}
	request.Method = "CASH_CONFIRMED"
	request.Evidence = "missing-proof"
	request.Amount.Minor = "400"
	request.Revision = money.Revision
	if _, err = recovery.Request(ctx, p, request, ids(), "r"); err == nil {
		t.Fatal("unconfirmed cash accepted")
	}
	request.Evidence = "cash-proof"
	rec, err = recovery.Request(ctx, p, request, ids(), "r")
	if err != nil {
		t.Fatal(err)
	}
	approve(rec)
	money, err = (ledger.Repository{}).Get(ctx, store.DB, s, entitlementID, false)
	if err != nil || money.Debt != "0" || money.Recovered != "1000" || money.Paid != "1000" {
		t.Fatal("cash recovery conservation", money, err)
	}
	// Approved corrections append a revision; original snapshots and paid records are immutable.
	finalTask, err := tasks.Get(ctx, store.DB, s, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	facts := snapshot.Facts
	facts.Basis = "2000"
	adjustRequest := worker.AdjustmentInput{TaskID: task.ID, Facts: facts, Reason: "source facts corrected", Evidence: "correction-proof", Revision: finalTask.Revision}
	adj, err := adjustment.Request(ctx, p, adjustRequest, ids(), "r")
	if err != nil {
		t.Fatal(err)
	}
	approve(adj)
	money, err = (ledger.Repository{}).Get(ctx, store.DB, s, entitlementID, false)
	if err != nil || money.Target != "10000" || money.Pending != "10000" || money.Paid != "1000" || money.Recovered != "1000" {
		t.Fatal("adjusted target conservation", money, err)
	}
	afterCorrection, err := tasks.Get(ctx, store.DB, s, task.ID)
	if err != nil || string(afterCorrection.Snapshot) != before {
		t.Fatal("correction overwrote original snapshot", err)
	}
	if r := acceptRefund("refund-after-adjustment", "1500"); r.Status != "SUCCEEDED" {
		t.Fatal(r)
	}
	money, err = (ledger.Repository{}).Get(ctx, store.DB, s, entitlementID, false)
	if err != nil || money.Target != "5000" || money.Pending != "5000" {
		t.Fatal("refund ignored approved corrected snapshot", money, err)
	}

}
func TestTaskLeaseFencingAndRetryLimit(t *testing.T) {
	store, s := database(t)
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	repo := taskrepo.Tasks{DB: store.DB, Within: store.Within}
	create := func(id string) {
		if err := store.Within(ctx, func(tx db.Executor) error {
			return repo.Create(ctx, tx, s, calculation.Task{ID: id, EventID: "event-" + id, Source: "sandbox", SourceEvent: id, BusinessID: id, EventType: "FEE_CONFIRMED", BusinessTime: at, EventBody: []byte(`{}`)}, at)
		}); err != nil {
			t.Fatal(err)
		}
	}
	create("fenced")
	old, found, err := repo.Claim(ctx, s, at, time.Second)
	if err != nil || !found {
		t.Fatal(err)
	}
	current, found, err := repo.Claim(ctx, s, at.Add(2*time.Second), time.Minute)
	if err != nil || !found || current.Fencing <= old.Fencing {
		t.Fatal("lease did not fence old owner", err)
	}
	reason := "UNKNOWN_FACT"
	if err = store.Within(ctx, func(tx db.Executor) error {
		return repo.Finish(ctx, tx, s, old, "SUSPENDED", &reason, nil, at.Add(2*time.Second))
	}); err == nil {
		t.Fatal("old fencing token committed")
	}
	if err = store.Within(ctx, func(tx db.Executor) error {
		return repo.Finish(ctx, tx, s, current, "SUSPENDED", &reason, nil, at.Add(2*time.Second))
	}); err != nil {
		t.Fatal(err)
	}
	create("retry")
	delays := []time.Duration{time.Second, 5 * time.Second, 30 * time.Second, 120 * time.Second, 600 * time.Second}
	for index := 0; index < 6; index++ {
		task, found, err := repo.Claim(ctx, s, at, time.Minute)
		if err != nil || !found {
			t.Fatalf("attempt %d %v", index, err)
		}
		if err = repo.Retry(ctx, s, task, at); err != nil {
			t.Fatal(err)
		}
		if index < 5 {
			at = at.Add(delays[index])
		}
	}
	task, err := repo.Get(ctx, store.DB, s, "retry")
	if err != nil || task.Status != "DEAD" || task.Retries != 5 {
		t.Fatalf("unbounded retry %+v %v", task, err)
	}
}
