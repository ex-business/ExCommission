package adapters

import (
	"context"
	"fmt"
	calc "github.com/ex-business/ExCommission/internal/calculation/domain"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"os"
	"testing"
	"time"
)

func TestRecoveryFIFOProofUniquenessAndAgentCurrencyIsolation(t *testing.T) {
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
	s := foundation.Scope{Tenant: fmt.Sprint("test-recover-", time.Now().UnixNano()), App: "app"}
	defer func() {
		for _, table := range []string{"ledger_recoveries", "ledger_debt_ages", "ledger_restrictions", "ledger_journals", "ledger_entitlements", "transaction_outbox"} {
			store.DB.Exec("DELETE FROM "+table+" WHERE tenant_id=?", s.Tenant)
		}
	}()
	now := time.Now().UTC().Truncate(time.Microsecond)
	r := Repository{}
	create := func(taskID, agent, currency string, paid bool, at time.Time) string {
		t.Helper()
		task := calc.Task{ID: taskID, Source: "s", BusinessID: taskID, EventType: "FEE_CONFIRMED"}
		input := calc.Input{TaskID: taskID, PlanVersionID: "pv", Facts: calc.Facts{BusinessTime: at}}
		input.Plan.Currency = currency
		result := calc.Result{TaskID: taskID, Status: "SUCCEEDED", Items: []calc.Item{{RuleID: "r", BeneficiaryID: agent, Currency: currency, Minor: "100"}}}
		id := stableID("s", taskID, "FEE_CONFIRMED", "pv", "r", agent, currency)
		err := store.Within(ctx, func(tx db.Executor) error {
			if e := r.Commit(ctx, tx, s, task, input, result, at); e != nil {
				return e
			}
			if e := r.Transfer(ctx, tx, s, id, "pending", "available", "100", taskID+"-settle", at); e != nil {
				return e
			}
			if !paid {
				return nil
			}
			if e := r.Transfer(ctx, tx, s, id, "available", "reserved", "100", taskID+"-reserve", at); e != nil {
				return e
			}
			if e := r.Transfer(ctx, tx, s, id, "reserved", "paid", "100", taskID+"-paid", at); e != nil {
				return e
			}
			return r.Reduce(ctx, tx, s, id, "0", taskID+"-refund", at)
		})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	first := create("first", "a", "USD", true, now.Add(-time.Second))
	second := create("second", "a", "USD", true, now)
	recover := func(id, method, amount, reference, evidence string) error {
		e, err := r.Get(ctx, store.DB, s, id, false)
		if err != nil {
			return err
		}
		return store.Within(ctx, func(tx db.Executor) error {
			return r.Recover(ctx, tx, s, id, e.Revision, method, amount, reference, evidence, now)
		})
	}
	if err = recover(second, "CASH_CONFIRMED", "100", "late-first", "proof-late"); err == nil || err.Error() != "FIFO_DEBT_REQUIRED" {
		t.Fatal("FIFO bypass", err)
	}
	if err = recover(first, "CASH_CONFIRMED", "100", "cash-first", "proof-one"); err != nil {
		t.Fatal(err)
	}
	if err = recover(second, "CASH_CONFIRMED", "100", "reuse-cash", "proof-one"); err == nil {
		t.Fatal("cash evidence reused")
	}
	e, err := r.Get(ctx, store.DB, s, second, false)
	if err != nil || e.Debt != "100" || e.Recovered != "0" {
		t.Fatal("reused proof rollback", e, err)
	}
	create("other-agent", "b", "USD", false, now)
	create("other-currency", "a", "BTC", false, now)
	if err = recover(second, "FUTURE_OFFSET", "60", "cross-scope", "offset-one"); err == nil || err.Error() != "INSUFFICIENT_BALANCE" {
		t.Fatal("other agent/currency offset", err)
	}
	funding := create("funding", "a", "USD", false, now)
	if err = recover(second, "FUTURE_OFFSET", "60", "valid-offset", "offset-two"); err != nil {
		t.Fatal(err)
	}
	e, err = r.Get(ctx, store.DB, s, second, false)
	fund, err2 := r.Get(ctx, store.DB, s, funding, false)
	if err != nil || err2 != nil || e.Debt != "40" || e.Recovered != "60" || fund.Available != "40" || fund.Paid != "60" {
		t.Fatal("offset accounting", e, fund, err, err2)
	}
}
