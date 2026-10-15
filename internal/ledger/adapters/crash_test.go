package adapters

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	audit "github.com/ex-business/ExCommission/internal/audit/adapters"
	tasks "github.com/ex-business/ExCommission/internal/calculation/adapters"
	worker "github.com/ex-business/ExCommission/internal/calculation/application"
	calc "github.com/ex-business/ExCommission/internal/calculation/domain"
	plan "github.com/ex-business/ExCommission/internal/plans/domain"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestProcessExitBeforeAndAfterLedgerCommit(t *testing.T) {
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
	repo := tasks.Tasks{DB: store.DB, Within: store.Within}
	if stage := os.Getenv("LEDGER_EXIT_STAGE"); stage != "" {
		s := foundation.Scope{Tenant: os.Getenv("LEDGER_EXIT_TENANT"), App: "app"}
		task, err := repo.Get(ctx, store.DB, s, "task")
		if err != nil {
			t.Fatal(err)
		}
		var input calc.Input
		if err = json.Unmarshal(task.Snapshot, &input); err != nil {
			t.Fatal(err)
		}
		result := calc.Calculate(input)
		at := task.BusinessTime
		err = store.Within(ctx, func(tx db.Executor) error {
			if err := (Repository{}).Commit(ctx, tx, s, task, input, result, at); err != nil {
				return err
			}
			if stage == "before" {
				os.Exit(23)
			}
			body, _ := json.Marshal(result)
			return repo.Finish(ctx, tx, s, task, result.Status, nil, body, at)
		})
		if err != nil {
			t.Fatal(err)
		}
		os.Exit(24)
	}
	for _, stage := range []string{"before", "after"} {
		t.Run(stage, func(t *testing.T) {
			s := foundation.Scope{Tenant: fmt.Sprint("test-ledger-exit-", stage, "-", time.Now().UnixNano()), App: "app"}
			defer func() {
				for _, table := range []string{"ledger_payout_policies", "ledger_journal_links", "ledger_restrictions", "ledger_journals", "ledger_entitlements", "transaction_outbox", "calculation_tasks", "audit_entries"} {
					store.DB.Exec("DELETE FROM "+table+" WHERE tenant_id=?", s.Tenant)
				}
			}()
			at := time.Now().UTC().Truncate(time.Microsecond)
			body, err := os.ReadFile("../../../conformance/v1/plan-percent.json")
			if err != nil {
				t.Fatal(err)
			}
			var config plan.Config
			if err = json.Unmarshal(body, &config); err != nil {
				t.Fatal(err)
			}
			yes := true
			input := calc.Input{TaskID: "task", SnapshotID: "snapshot", StandardVersion: "1", PlanVersionID: "pv", ConfigHash: "frozen-config", CurrencyCatalogVersion: "currency-v1", CalendarVersion: "UTC-v1", Plan: config, Facts: calc.Facts{Basis: "10", Currency: "USD", Product: "BTC-USDT", Scene: "SPOT", Confirmed: &yes, BusinessTime: at}, Ancestors: []calc.Actor{{ID: "a", Depth: 1, Grade: "G0", Eligible: &yes, Status: "ACTIVE"}}, Contracts: map[string]calc.ContractFact{}, Exponent: 2}
			snapshot, _ := json.Marshal(input)
			snapshot, err = foundation.CanonicalJSON(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(snapshot)
			if err = store.Within(ctx, func(tx db.Executor) error {
				return repo.Create(ctx, tx, s, calc.Task{ID: "task", EventID: "event", Source: "s", SourceEvent: "e", BusinessID: "b", EventType: "FEE_CONFIRMED", BusinessTime: at, EventBody: []byte(`{}`)}, at)
			}); err != nil {
				t.Fatal(err)
			}
			claimed, found, err := repo.Claim(ctx, s, at, time.Second)
			if err != nil || !found {
				t.Fatal(err)
			}
			if err = store.Within(ctx, func(tx db.Executor) error {
				return repo.SaveSnapshot(ctx, tx, s, claimed, snapshot, hex.EncodeToString(sum[:]), at)
			}); err != nil {
				t.Fatal(err)
			}
			child := exec.Command(os.Args[0], "-test.run=^TestProcessExitBeforeAndAfterLedgerCommit$")
			child.Env = append(os.Environ(), "LEDGER_EXIT_STAGE="+stage, "LEDGER_EXIT_TENANT="+s.Tenant)
			output, err := child.CombinedOutput()
			var exit *exec.ExitError
			expected := 23
			if stage == "after" {
				expected = 24
			}
			if !errors.As(err, &exit) || exit.ExitCode() != expected {
				t.Fatalf("child exit %v %s", err, output)
			}
			var count int
			if err = store.DB.QueryRow(`SELECT COUNT(*) FROM ledger_entitlements WHERE tenant_id=?`, s.Tenant).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if stage == "before" && count != 0 {
				t.Fatal("process exit leaked uncommitted money")
			}
			if stage == "after" && count != 1 {
				t.Fatal("committed money missing")
			}
			processor := worker.Worker{UOW: store, Tasks: repo, Committer: Repository{}, Now: func() time.Time { return at.Add(2 * time.Second) }, Lease: time.Minute}
			done, err := processor.RunOnce(ctx, s)
			if err != nil || done != (stage == "before") {
				t.Fatal("crash recovery replay", done, err)
			}
			task, err := repo.Get(ctx, store.DB, s, "task")
			if err != nil || task.Status != "SUCCEEDED" || string(task.Snapshot) != string(snapshot) {
				t.Fatal("recovery changed frozen task", task.Status, err)
			}
			for _, table := range []string{"ledger_entitlements", "ledger_journals", "transaction_outbox"} {
				if err = store.DB.QueryRow("SELECT COUNT(*) FROM "+table+" WHERE tenant_id=?", s.Tenant).Scan(&count); err != nil || count != 1 {
					t.Fatal("duplicate or partial posting", table, count, err)
				}
			}
			deliver := func(event mysql.OutboxEvent) error {
				return audit.Deliver(ctx, store.DB, event.Scope, event.ID, event.Topic, event.Payload, event.CreatedAt)
			}
			if err = store.DeliverOutbox(ctx, s, func(event mysql.OutboxEvent) error {
				if err := deliver(event); err != nil {
					return err
				}
				return errors.New("injected missing acknowledgement")
			}); err == nil {
				t.Fatal("delivery failure ignored")
			}
			if err = store.DeliverOutbox(ctx, s, deliver); err != nil {
				t.Fatal(err)
			}
			if err = store.DB.QueryRow(`SELECT COUNT(*) FROM audit_entries WHERE tenant_id=?`, s.Tenant).Scan(&count); err != nil || count != 1 {
				t.Fatal("receiver duplicated after acknowledgement loss", count, err)
			}
		})
	}
}
