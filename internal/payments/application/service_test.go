package application

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	calc "github.com/ex-business/ExCommission/internal/calculation/domain"
	identity "github.com/ex-business/ExCommission/internal/identity/domain"
	ledger "github.com/ex-business/ExCommission/internal/ledger/adapters"
	adapter "github.com/ex-business/ExCommission/internal/payments/adapters"
	model "github.com/ex-business/ExCommission/internal/payments/domain"
	providerports "github.com/ex-business/ExCommission/internal/payments/ports"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	statements "github.com/ex-business/ExCommission/internal/settlement/adapters"
	settlement "github.com/ex-business/ExCommission/internal/settlement/application"
	statement "github.com/ex-business/ExCommission/internal/settlement/domain"
	workflowrepo "github.com/ex-business/ExCommission/internal/workflow/adapters"
	workflow "github.com/ex-business/ExCommission/internal/workflow/application"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type testProvider struct {
	disabled bool
	mu       sync.Mutex
	sends    []string
	outcome  model.Outcome
	key      []byte
}

func (p *testProvider) Enabled() bool { return !p.disabled }
func (p *testProvider) Submit(_ context.Context, v model.Payment) (model.Outcome, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sends = append(p.sends, v.MerchantID)
	return model.Outcome{MerchantID: v.MerchantID, Currency: v.Currency, Amount: v.Amount, Status: "UNKNOWN"}, nil
}
func (p *testProvider) Query(_ context.Context, v model.Payment) (model.Outcome, error) {
	if p.outcome.Status != "" {
		return p.outcome, nil
	}
	return model.Outcome{MerchantID: v.MerchantID, Currency: v.Currency, Amount: v.Amount, Status: "UNKNOWN"}, nil
}
func (p *testProvider) VerifyCallback(b []byte, s string) (model.Outcome, error) {
	return (adapter.Sandbox{Key: p.key}).VerifyCallback(b, s)
}
func (p *testProvider) Reconcile(context.Context, string) ([]model.Outcome, error) {
	return []model.Outcome{p.outcome}, nil
}
func TestPaymentUnknownOriginalIDCallbacksAndConservation(t *testing.T) {
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
	scope := foundation.Scope{Tenant: fmt.Sprint("test-payment-", time.Now().UnixNano()), App: "app"}
	defer func() {
		for _, table := range []string{"payment_callbacks", "payment_allocations", "payment_orders", "settlement_claims", "settlement_items", "settlement_statements", "settlement_runs", "settlement_spaces", "workflow_requests", "command_results", "ledger_restrictions", "ledger_journals", "ledger_entitlements", "transaction_outbox"} {
			store.DB.Exec("DELETE FROM "+table+" WHERE tenant_id=?", scope.Tenant)
		}
	}()
	now := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	var counter atomic.Int64
	id := func() string { return fmt.Sprint("id", counter.Add(1)) }
	p := identity.Principal{Tenant: scope.Tenant, App: scope.App, Subject: "maker"}
	checker := p
	checker.Subject = "checker"
	input := calc.Input{TaskID: "task", PlanVersionID: "pv", Facts: calc.Facts{BusinessTime: now.Add(-48 * time.Hour)}}
	input.Plan.Currency = "USD"
	result := calc.Result{TaskID: "task", Status: "SUCCEEDED", Items: []calc.Item{{RuleID: "r", BeneficiaryID: "agent", Currency: "USD", Minor: "10000"}}}
	task := calc.Task{ID: "task", Source: "source", BusinessID: "business", EventType: "FEE_CONFIRMED"}
	if err = store.Within(ctx, func(tx db.Executor) error {
		return (ledger.Repository{}).Commit(ctx, tx, scope, task, input, result, now)
	}); err != nil {
		t.Fatal(err)
	}
	settle := settlement.Service{Commands: store, Query: store.DB, Repo: statements.Repository{}, Ledger: ledger.Repository{}, Workflow: workflowrepo.Repository{}, Now: func() time.Time { return now }, ID: id}
	run, err := settle.Run(ctx, p, settlement.RunInput{Period: "2026-10-09", Calendar: "UTC-v1"}, id(), id())
	if err != nil || run.Status != 202 {
		t.Fatal(err, string(run.Body))
	}
	var runBody struct {
		Data struct {
			Statements []statement.Statement `json:"statements"`
		}
	}
	json.Unmarshal(run.Body, &runBody)
	v := runBody.Data.Statements[0]
	submitted, err := settle.Submit(ctx, p, v.ID, settlement.Submission{Revision: 0, Hash: v.Hash}, id(), id())
	if err != nil {
		t.Fatal(err)
	}
	var approval struct {
		Data struct {
			ID   string `json:"approval_id"`
			Hash string `json:"object_hash"`
		}
	}
	json.Unmarshal(submitted.Body, &approval)
	wf := workflow.Service{Commands: store, Query: store.DB, Repo: workflowrepo.Repository{}, Now: settle.Now, Executors: map[string]workflow.Executor{"STATEMENT": settle.Execute}}
	approved, err := wf.Decide(ctx, checker, approval.Data.ID, workflow.Decision{Decision: "APPROVE", Revision: 0, ObjectHash: approval.Data.Hash, Reason: "checked"}, id(), id())
	if err != nil || approved.Status != 200 {
		t.Fatal(err, string(approved.Body))
	}
	provider := &testProvider{key: []byte("01234567890123456789012345678901")}
	service := Service{Commands: store, UOW: store, Query: store.DB, Repo: adapter.Repository{}, Ledger: ledger.Repository{}, Statements: statements.Repository{}, Providers: map[string]providerports.Provider{"sandbox": provider}, Now: settle.Now, ID: id}
	v, err = settle.Repo.Get(ctx, store.DB, scope, v.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	create := CreateInput{StatementID: v.ID, Amount: model.Money{Currency: "USD", Minor: "6000"}, Channel: "sandbox", Revision: v.Revision}
	key := id()
	created, err := service.Create(ctx, p, create, key, id())
	if err != nil || created.Status != 202 {
		t.Fatal(err, string(created.Body))
	}
	pid := created.ResourceID
	if err = service.Recover(ctx, scope); err != nil {
		t.Fatal(err)
	}
	if err = service.Dispatch(ctx, scope, pid); err != nil {
		t.Fatal(err)
	}
	payment, err := service.Repo.Get(ctx, store.DB, scope, pid, false)
	if err != nil || payment.Status != "UNKNOWN" || len(provider.sends) != 1 || provider.sends[0] != payment.MerchantID {
		t.Fatal("unknown resent or wrong identity", payment, provider.sends, err)
	}
	money, err := service.Ledger.Get(ctx, store.DB, scope, v.Items[0].ID, false)
	if err != nil || money.Available != "4000" || money.Reserved != "6000" || money.Paid != "0" {
		t.Fatal("unknown released funds", money, err)
	}
	v, _ = settle.Repo.Get(ctx, store.DB, scope, v.ID, false)
	tooMuch := create
	tooMuch.Revision = v.Revision
	tooMuch.Amount.Minor = "5000"
	rejected, err := service.Create(ctx, p, tooMuch, id(), id())
	if err != nil || rejected.Status != 422 {
		t.Fatal("overspend accepted", rejected.Status, err)
	}
	if err = service.Recover(ctx, scope); err != nil {
		t.Fatal(err)
	}
	if len(provider.sends) != 1 {
		t.Fatal("recovery resent ambiguous transfer")
	}
	provider.disabled = true
	replay, err := service.Create(ctx, p, create, key, id())
	if err != nil || replay.ResourceID != pid {
		t.Fatal("disabled channel broke original replay", err)
	}
	provider.outcome = model.Outcome{MerchantID: payment.MerchantID, Currency: "USD", Amount: "6000", Status: "SUCCEEDED"}
	if err = service.Verify(ctx, scope, pid); err != nil {
		t.Fatal("disabled channel blocked original query", err)
	}
	callback := provider.outcome
	callback.CallbackID = "cb-success"
	raw, _ := json.Marshal(callback)
	mac := hmac.New(sha256.New, provider.key)
	mac.Write(raw)
	signature := hex.EncodeToString(mac.Sum(nil))
	if err = service.Callback(ctx, scope, "sandbox", raw, "bad"); err == nil {
		t.Fatal("bad callback signature accepted")
	}
	var wg sync.WaitGroup
	failures := make(chan error, 20)
	for range 20 {
		wg.Go(func() { failures <- service.Callback(ctx, scope, "sandbox", raw, signature) })
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	money, err = service.Ledger.Get(ctx, store.DB, scope, v.Items[0].ID, false)
	if err != nil || money.Paid != "6000" || money.Reserved != "0" || money.Available != "4000" {
		t.Fatal("success replay moved funds again", money, err)
	}
	provider.disabled = false
	v, _ = settle.Repo.Get(ctx, store.DB, scope, v.ID, false)
	create.Amount.Minor = "4000"
	create.Revision = v.Revision
	created, err = service.Create(ctx, p, create, id(), id())
	if err != nil || created.Status != 202 {
		t.Fatal(err, string(created.Body))
	}
	pid2 := created.ResourceID
	if err = service.Dispatch(ctx, scope, pid2); err != nil {
		t.Fatal(err)
	}
	p2, _ := service.Repo.Get(ctx, store.DB, scope, pid2, false)
	provider.outcome = model.Outcome{MerchantID: p2.MerchantID, Currency: "USD", Amount: "4000", Status: "FAILED_FINAL"}
	if err = service.Reconcile(ctx, scope, "sandbox", "statement-ref"); err != nil {
		t.Fatal(err)
	}
	money, err = service.Ledger.Get(ctx, store.DB, scope, v.Items[0].ID, false)
	if err != nil || money.Paid != "6000" || money.Available != "4000" || money.Reserved != "0" {
		t.Fatal("final failure did not release", money, err)
	}
	provider.outcome.Status = "SUCCEEDED"
	if err = service.Verify(ctx, scope, pid2); err != nil {
		t.Fatal(err)
	}
	p2, _ = service.Repo.Get(ctx, store.DB, scope, pid2, false)
	money, _ = service.Ledger.Get(ctx, store.DB, scope, v.Items[0].ID, false)
	if p2.Status != "FAILED_FINAL" || p2.Reconciliation != "RECONCILIATION_EXCEPTION" || !money.Frozen || money.Paid != "6000" {
		t.Fatal("terminal conflict rewrote funds", p2, money)
	}
	other := scope
	other.Tenant += "other"
	if err = service.Apply(ctx, other, "sandbox", provider.outcome); err == nil {
		t.Fatal("cross tenant result accepted")
	}
}
