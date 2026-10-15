package adapters

import (
	"context"
	"errors"
	"fmt"
	tasks "github.com/ex-business/ExCommission/internal/calculation/adapters"
	calc "github.com/ex-business/ExCommission/internal/calculation/domain"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"os"
	"sync"
	"testing"
	"time"
)

func TestAtomicLedgerReplayIsolationAndFencing(t *testing.T) {
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
	scope := foundation.Scope{Tenant: fmt.Sprint("test-ledger-", time.Now().UnixNano()), App: "app"}
	defer func() {
		for _, table := range []string{"ledger_restrictions", "ledger_journals", "ledger_entitlements", "transaction_outbox", "calculation_tasks"} {
			store.DB.Exec("DELETE FROM "+table+" WHERE tenant_id=?", scope.Tenant)
		}
	}()
	now := time.Now().UTC().Truncate(time.Microsecond)
	repo := tasks.Tasks{DB: store.DB, Within: store.Within}
	task := calc.Task{ID: "task", EventID: "event", Source: "source", SourceEvent: "external", BusinessID: "business", EventType: "FEE_CONFIRMED", BusinessTime: now, EventBody: []byte(`{}`)}
	if err = store.Within(ctx, func(tx db.Executor) error { return repo.Create(ctx, tx, scope, task, now) }); err != nil {
		t.Fatal(err)
	}
	task, _, err = repo.Claim(ctx, scope, now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	input := calc.Input{TaskID: "task", PlanVersionID: "pv", Facts: calc.Facts{BusinessTime: now}, Ancestors: []calc.Actor{{ID: "a", Status: "FROZEN"}}}
	input.Plan.Currency = "USD"
	result := calc.Result{TaskID: "task", Status: "SUCCEEDED", Items: []calc.Item{{RuleID: "r", BeneficiaryID: "a", Currency: "USD", Minor: "10000"}}}
	commit := func(task calc.Task, finish bool) error {
		return store.Within(ctx, func(tx db.Executor) error {
			if err := (Repository{}).Commit(ctx, tx, scope, task, input, result, now); err != nil {
				return err
			}
			if finish {
				return repo.Finish(ctx, tx, scope, task, "SUCCEEDED", nil, []byte(`{}`), now)
			}
			return nil
		})
	}
	// A process that lost its lease must roll back its tentative ledger writes.
	stale := task
	stale.Fencing++
	if err = commit(stale, true); err == nil {
		t.Fatal("stale process committed")
	}
	var n int
	store.DB.QueryRow(`SELECT COUNT(*) FROM ledger_entitlements WHERE tenant_id=?`, scope.Tenant).Scan(&n)
	if n != 0 {
		t.Fatal("partial posting survived fencing failure")
	}
	// Failure after all postings is equivalent to a crash before COMMIT.
	if err = store.Within(ctx, func(tx db.Executor) error {
		if e := (Repository{}).Commit(ctx, tx, scope, task, input, result, now); e != nil {
			return e
		}
		return errors.New("injected rollback")
	}); err == nil {
		t.Fatal("injected failure ignored")
	}
	store.DB.QueryRow(`SELECT COUNT(*) FROM transaction_outbox WHERE tenant_id=?`, scope.Tenant).Scan(&n)
	if n != 0 {
		t.Fatal("outbox survived rollback")
	}
	if err = commit(task, true); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	failures := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); failures <- commit(task, false) }()
	}
	wg.Wait()
	close(failures)
	for e := range failures {
		if e != nil {
			t.Fatal(e)
		}
	}
	for _, table := range []string{"ledger_entitlements", "ledger_journals", "ledger_restrictions", "transaction_outbox"} {
		if err = store.DB.QueryRow("SELECT COUNT(*) FROM "+table+" WHERE tenant_id=?", scope.Tenant).Scan(&n); err != nil || n != 1 {
			t.Fatalf("%s count=%d err=%v", table, n, err)
		}
	}
	var amount, pending, available, reserved, paid, debt, recovered, debit, credit string
	err = store.DB.QueryRow(`SELECT CAST(original_target AS CHAR),CAST(pending AS CHAR),CAST(available AS CHAR),CAST(reserved AS CHAR),CAST(paid AS CHAR),CAST(debt AS CHAR),CAST(recovered AS CHAR) FROM ledger_entitlements WHERE tenant_id=?`, scope.Tenant).Scan(&amount, &pending, &available, &reserved, &paid, &debt, &recovered)
	if err != nil || amount != "10000" || pending != amount || available != "0" || reserved != "0" || paid != "0" || debt != "0" || recovered != "0" {
		t.Fatalf("invalid money buckets %s %s %v", amount, pending, err)
	}
	err = store.DB.QueryRow(`SELECT debit_account,credit_account FROM ledger_journals WHERE tenant_id=?`, scope.Tenant).Scan(&debit, &credit)
	if err != nil || debit != "COMMISSION_EXPENSE" || credit != "PAYABLE_PENDING" {
		t.Fatal("wrong journal", err)
	}
	other := scope
	other.App = "other"
	if err = store.Within(ctx, func(tx db.Executor) error { return (Repository{}).Commit(ctx, tx, other, task, input, result, now) }); err != nil {
		t.Fatal("scope collision", err)
	}

	id := stableID(task.Source, task.BusinessID, task.EventType, input.PlanVersionID, "r", "a", "USD")
	move := func(from, to, amount, ref string) error {
		return store.Within(ctx, func(tx db.Executor) error {
			return (Repository{}).Transfer(ctx, tx, scope, id, from, to, amount, ref, now)
		})
	}
	if err = move("pending", "available", "10000", "settle"); err == nil {
		t.Fatal("frozen entitlement settled")
	}
	if _, err = store.DB.Exec(`UPDATE ledger_restrictions SET closed_at=? WHERE tenant_id=? AND app_id=?`, now, scope.Tenant, scope.App); err != nil {
		t.Fatal(err)
	}
	if err = move("pending", "available", "10000", "settle"); err != nil {
		t.Fatal(err)
	}
	if err = move("pending", "available", "10000", "settle"); err != nil {
		t.Fatal("transfer replay", err)
	}
	if err = move("pending", "available", "9999", "settle"); err == nil {
		t.Fatal("changed transfer replay accepted")
	}
	outcomes := make(chan error, 2)
	for _, ref := range []string{"pay1", "pay2"} {
		go func(ref string) { outcomes <- move("available", "reserved", "7000", ref) }(ref)
	}
	successes := 0
	for i := 0; i < 2; i++ {
		if <-outcomes == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatal("concurrent overspend", successes)
	}
	e, err := (Repository{}).Get(ctx, store.DB, scope, id, false)
	if err != nil || e.Pending != "0" || e.Available != "3000" || e.Reserved != "7000" {
		t.Fatal("bucket conservation", e, err)
	}
	// A second currency is stored independently without conversion or aggregation.
	input.Plan.Currency = "BTC"
	result.Items[0].Currency = "BTC"
	result.Items[0].Minor = "12345678"
	if err = commit(task, false); err != nil {
		t.Fatal(err)
	}
	btcID := stableID(task.Source, task.BusinessID, task.EventType, input.PlanVersionID, "r", "a", "BTC")
	btc, err := (Repository{}).Get(ctx, store.DB, scope, btcID, false)
	if err != nil || btc.Pending != "12345678" {
		t.Fatal("currency isolation", btc, err)
	}
	input.Plan.Currency = "USD"
	result.Items[0].Currency = "USD"
	result.Items[0].Minor = "10001"
	if err = commit(task, false); err == nil {
		t.Fatal("changed replay accepted")
	}
	result.Items[0].Minor = "10000"
	result.Items = append(result.Items, result.Items[0])
	if err = commit(task, false); err == nil {
		t.Fatal("duplicate item accepted")
	}
	result.Items = result.Items[:1]
	result.Items[0].Minor = "100000000000000000000000000000000000000"
	if err = commit(task, false); err == nil {
		t.Fatal("overflow accepted")
	}
}
