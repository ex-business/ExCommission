package application

import (
	"context"
	"encoding/json"
	"fmt"
	agents "github.com/ex-business/ExCommission/internal/agents/adapters"
	calc "github.com/ex-business/ExCommission/internal/calculation/domain"
	ledger "github.com/ex-business/ExCommission/internal/ledger/adapters"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	approval "github.com/ex-business/ExCommission/internal/workflow/domain"
	"os"
	"testing"
	"time"
)

func TestFreezeVersusPaymentReservationUsesOneLockOrder(t *testing.T) {
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
	s := foundation.Scope{Tenant: fmt.Sprint("test-risk-race-", time.Now().UnixNano()), App: "app"}
	defer func() {
		for _, table := range []string{"agent_status_history", "agent_accounts", "agent_relations", "ledger_restrictions", "ledger_journals", "ledger_entitlements", "transaction_outbox"} {
			store.DB.Exec("DELETE FROM "+table+" WHERE tenant_id=?", s.Tenant)
		}
	}()
	now := time.Now().UTC().Truncate(time.Microsecond)
	service := Service{Accounts: agents.Repository{}, Ledger: ledger.Repository{}}
	for i := 0; i < 10; i++ {
		id := fmt.Sprint("a", i)
		at := now.Add(-time.Hour)
		input := calc.Input{TaskID: id, PlanVersionID: "pv", Facts: calc.Facts{BusinessTime: at}}
		input.Plan.Currency = "USD"
		result := calc.Result{TaskID: id, Status: "SUCCEEDED", Items: []calc.Item{{RuleID: "r", BeneficiaryID: id, Currency: "USD", Minor: "100"}}}
		if err = store.Within(ctx, func(tx db.Executor) error {
			if err := (agents.Repository{}).Create(ctx, tx, s, id, id, id, at); err != nil {
				return err
			}
			return (ledger.Repository{}).Commit(ctx, tx, s, calc.Task{ID: id, Source: "s", BusinessID: id, EventType: "FEE_CONFIRMED"}, input, result, at)
		}); err != nil {
			t.Fatal(err)
		}
		funds, err := (ledger.Repository{}).TaskEntitlements(ctx, store.DB, s, id)
		if err != nil {
			t.Fatal(err)
		}
		eid := funds[0].ID
		if err = store.Within(ctx, func(tx db.Executor) error {
			return (ledger.Repository{}).Transfer(ctx, tx, s, eid, "pending", "available", "100", id+"-settle", at)
		}); err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		payment := make(chan error, 1)
		freeze := make(chan error, 1)
		go func() {
			<-start
			payment <- store.Within(ctx, func(tx db.Executor) error {
				if err := (agents.Repository{}).PaymentAllowed(ctx, tx, s, id); err != nil {
					return err
				}
				return (ledger.Repository{}).Transfer(ctx, tx, s, eid, "available", "reserved", "100", id+"-payment", now)
			})
		}()
		go func() {
			<-start
			payload, _ := json.Marshal(StatusInput{AgentID: id, Status: "FROZEN", Revision: 0, Evidence: "proof"})
			freeze <- store.Within(ctx, func(tx db.Executor) error {
				return service.ExecuteStatus(ctx, tx, s, approval.Request{ID: id + "-freeze", Payload: payload}, now)
			})
		}()
		close(start)
		payErr, freezeErr := <-payment, <-freeze
		if freezeErr != nil {
			t.Fatal(freezeErr)
		}
		if payErr != nil && payErr.Error() != "FUNDS_FROZEN" {
			t.Fatal(payErr)
		}
		e, err := (ledger.Repository{}).Get(ctx, store.DB, s, eid, false)
		if err != nil || !e.Frozen || e.Available != "0" {
			t.Fatal(e, err)
		}
		if payErr == nil {
			if e.Reserved != "100" || e.Pending != "0" || !e.PaymentHold {
				t.Fatal("submitted funds were released", e)
			}
		} else if e.Reserved != "0" || e.Pending != "100" {
			t.Fatal("frozen funds reserved", e)
		}
	}
}
