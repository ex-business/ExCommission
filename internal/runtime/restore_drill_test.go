package runtime

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
	calculation "github.com/ex-business/ExCommission/internal/calculation/application"
	identity "github.com/ex-business/ExCommission/internal/identity/domain"
	source "github.com/ex-business/ExCommission/internal/integration/adapters"
	integration "github.com/ex-business/ExCommission/internal/integration/application"
	event "github.com/ex-business/ExCommission/internal/integration/domain"
	ledger "github.com/ex-business/ExCommission/internal/ledger/adapters"
	payrepo "github.com/ex-business/ExCommission/internal/payments/adapters"
	payments "github.com/ex-business/ExCommission/internal/payments/application"
	payment "github.com/ex-business/ExCommission/internal/payments/domain"
	payports "github.com/ex-business/ExCommission/internal/payments/ports"
	plans "github.com/ex-business/ExCommission/internal/plans/adapters"
	catalog "github.com/ex-business/ExCommission/internal/platform/adapters/catalog"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	statements "github.com/ex-business/ExCommission/internal/settlement/adapters"
	settlement "github.com/ex-business/ExCommission/internal/settlement/application"
	statement "github.com/ex-business/ExCommission/internal/settlement/domain"
	users "github.com/ex-business/ExCommission/internal/users/adapters"
	user "github.com/ex-business/ExCommission/internal/users/domain"
	workflowrepo "github.com/ex-business/ExCommission/internal/workflow/adapters"
	workflow "github.com/ex-business/ExCommission/internal/workflow/application"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

// TestLocalRestoreDrill is invoked only by the isolated local backup/restore verifier.
func TestLocalRestoreDrill(t *testing.T) {
	mode := os.Getenv("RESTORE_MODE")
	tenant := os.Getenv("RESTORE_TENANT")
	if mode == "" {
		t.Skip("explicit local restore drill only")
	}
	if !regexp.MustCompile(`^test-restore-[a-f0-9]{32}$`).MatchString(tenant) {
		t.Fatal("invalid isolated fixture scope")
	}
	database := os.Getenv("RESTORE_DATABASE")
	if database == "" {
		database = "excommission"
	}
	if database != "excommission" && !regexp.MustCompile(`^excommission_restore_[a-f0-9]{16}$`).MatchString(database) {
		t.Fatal("invalid restore database")
	}
	ctx := context.Background()
	store, err := mysql.Open(ctx, os.Getenv("TEST_MYSQL_ADDRESS"), "excommission", os.Getenv("MYSQL_PASSWORD"), database)
	if err != nil {
		t.Fatal(err)
	}
	defer store.DB.Close()
	if err = store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	s := foundation.Scope{Tenant: tenant, App: "app"}
	now := clockUTC()
	at := now.Truncate(24 * time.Hour).Add(-12 * time.Hour)
	counter := 0
	id := func() string { counter++; return fmt.Sprint("restore-id-", counter) }
	p := identity.Principal{Tenant: tenant, App: s.App, Subject: "fixture-maker"}
	tasks := taskrepo.Tasks{DB: store.DB, Within: store.Within}
	provider, err := source.NewSource("sandbox", []byte(strings.Repeat("r", 32)))
	if err != nil {
		t.Fatal(err)
	}
	ingress := integration.Service{Commands: store, Query: store.DB, Repo: source.Repository{}, Tasks: tasks, Plans: plans.Repository{}, Source: provider, Now: func() time.Time { return now }, ID: id}
	payProvider := payrepo.Sandbox{Key: []byte(strings.Repeat("p", 32))}
	pay := payments.Service{Commands: store, UOW: store, Query: store.DB, Repo: payrepo.Repository{}, Ledger: ledger.Repository{}, Accounts: agents.Repository{}, Statements: statements.Repository{}, Providers: map[string]payports.Provider{"sandbox": payProvider}, Now: func() time.Time { return now }, ID: id}
	if mode == "cleanup" {
		rows, err := store.DB.Query(`SELECT TABLE_NAME FROM information_schema.columns WHERE TABLE_SCHEMA=DATABASE() AND COLUMN_NAME='tenant_id' ORDER BY TABLE_NAME`)
		if err != nil {
			t.Fatal(err)
		}
		tables := []string{}
		for rows.Next() {
			var table string
			if err = rows.Scan(&table); err != nil {
				t.Fatal(err)
			}
			if !regexp.MustCompile(`^[a-z_]+$`).MatchString(table) {
				t.Fatal("unexpected table")
			}
			tables = append(tables, table)
		}
		if err = rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		if err = store.Within(ctx, func(tx db.Executor) error {
			for _, table := range tables {
				if _, err := tx.ExecContext(ctx, "DELETE FROM `"+table+"` WHERE tenant_id=?", tenant); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return
	}
	if mode == "seed" {
		fixture, err := os.ReadFile("../../conformance/v1/plan-percent.json")
		if err != nil {
			t.Fatal(err)
		}
		validator, err := plans.NewValidator(foundation.CurrencyExponents(), map[string]bool{"UTC-v1": true})
		if err != nil {
			t.Fatal(err)
		}
		config, canonical, err := validator.Decode(fixture)
		if err != nil {
			t.Fatal(err)
		}
		before := at.Add(-time.Hour)
		if err = store.Within(ctx, func(tx db.Executor) error {
			if err := (catalog.Repository{}).Bootstrap(ctx, tx, s); err != nil {
				return err
			}
			if err := (plans.Repository{}).Create(ctx, tx, s, config, canonical, plans.Hash(canonical), before); err != nil {
				return err
			}
			if err := (plans.Repository{}).SetPending(ctx, tx, s, config.PlanID, 0, plans.Hash(canonical)); err != nil {
				return err
			}
			if err := (plans.Repository{}).Publish(ctx, tx, s, config.PlanID, 0, plans.Hash(canonical), "restore-pv", before, before); err != nil {
				return err
			}
			trader, err := (users.Repository{}).Upsert(ctx, tx, s, "sandbox", user.Record{ExternalID: "trader", DisplayName: "恢复演练交易用户"}, before)
			if err != nil {
				return err
			}
			agentUser, err := (users.Repository{}).Upsert(ctx, tx, s, "sandbox", user.Record{ExternalID: "agent-user", DisplayName: "恢复演练代理"}, before)
			if err != nil {
				return err
			}
			if err = (agents.Repository{}).Create(ctx, tx, s, "agent", agentUser, "恢复演练代理", before); err != nil {
				return err
			}
			if err = (agents.Repository{}).ApplyGrade(ctx, tx, s, agent.GradeChange{AgentID: "agent", PlanID: config.PlanID, GradeID: "G0", Eligibility: "ELIGIBLE"}, "MANUAL", "verified-fixture", before); err != nil {
				return err
			}
			return (users.Repository{}).BindInitial(ctx, tx, s, trader, "agent", "verified-fixture", before)
		}); err != nil {
			t.Fatal(err)
		}
		yes := true
		raw, _ := json.Marshal(event.Event{Schema: "1", Source: "sandbox", ID: "original", BusinessID: "order", Type: "FEE_CONFIRMED", Time: at, ExternalUser: "trader", Product: "BTC-USDT", Scene: "SPOT", Revision: "1", Payload: event.Payload{Basis: "10", Currency: "USD", Confirmed: &yes}})
		mac := hmac.New(sha256.New, provider.Key)
		mac.Write(raw)
		accepted, err := ingress.Receive(ctx, p, raw, hex.EncodeToString(mac.Sum(nil)), "source-original", "fixture")
		if err != nil || accepted.Status != 202 {
			t.Fatal(err, string(accepted.Body))
		}
		processor := calculation.Worker{UOW: store, Tasks: tasks, Resolver: calculation.Resolver{Plans: plans.Repository{}, Users: users.Repository{}, Accounts: agents.Repository{}, Relationships: agents.Repository{}, Grades: agents.Repository{}, Contracts: agents.Repository{}, Exponents: foundation.CurrencyExponents(), Catalog: catalog.Repository{}}, Committer: ledger.Repository{}, Now: func() time.Time { return now }, Lease: time.Minute}
		if done, err := processor.RunOnce(ctx, s); err != nil || !done {
			t.Fatal(err)
		}
		settle := settlement.Service{Commands: store, Query: store.DB, Repo: statements.Repository{}, Ledger: ledger.Repository{}, Workflow: workflowrepo.Repository{}, Now: func() time.Time { return now }, ID: id}
		run, err := settle.Run(ctx, p, settlement.RunInput{Period: at.Format("2006-01-02"), Calendar: "UTC-v1"}, "run", "fixture")
		if err != nil || run.Status != 202 {
			t.Fatal(err, string(run.Body))
		}
		var batch struct {
			Data struct {
				Statements []statement.Statement `json:"statements"`
			}
		}
		json.Unmarshal(run.Body, &batch)
		if len(batch.Data.Statements) != 1 || batch.Data.Statements[0].Total != "100" {
			t.Fatal("restore fixture settlement", string(run.Body))
		}
		v := batch.Data.Statements[0]
		submitted, err := settle.Submit(ctx, p, v.ID, settlement.Submission{Revision: 0, Hash: v.Hash}, "submit", "fixture")
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
		checker := p
		checker.Subject = "fixture-checker"
		wf := workflow.Service{Commands: store, Query: store.DB, Repo: workflowrepo.Repository{}, Now: func() time.Time { return now }, Executors: map[string]workflow.Executor{"STATEMENT": settle.Execute}}
		approved, err := wf.Decide(ctx, checker, approval.Data.ID, workflow.Decision{Decision: "APPROVE", ObjectHash: approval.Data.Hash, Reason: "verified independently"}, "approve", "fixture")
		if err != nil || approved.Status != 200 {
			t.Fatal(err, string(approved.Body))
		}
		v, err = settle.Repo.Get(ctx, store.DB, s, v.ID, false)
		if err != nil {
			t.Fatal(err)
		}
		created, err := pay.Create(ctx, p, payments.CreateInput{StatementID: v.ID, Amount: payment.Money{Currency: "USD", Minor: "40"}, Channel: "sandbox", Revision: v.Revision}, "payment", "fixture")
		if err != nil || created.Status != 202 {
			t.Fatal(err, string(created.Body))
		}
		if err = pay.Dispatch(ctx, s, created.ResourceID); err != nil {
			t.Fatal(err)
		}
		pending, err := pay.Repo.Pending(ctx, store.DB, s)
		if err != nil || len(pending) != 1 || pending[0].Status != "UNKNOWN" {
			t.Fatal("missing unknown payment fixture", pending, err)
		}
		t.Log("isolated restore fixture: signed event, immutable snapshot, approved statement, UNKNOWN payment")
		return
	}
	if mode != "verify" {
		t.Fatal("unknown restore mode")
	}
	if database == "excommission" {
		t.Fatal("verification may mutate only restored database")
	}
	pending, err := pay.Repo.Pending(ctx, store.DB, s)
	if err != nil || len(pending) != 1 || pending[0].Status != "UNKNOWN" {
		t.Fatal("restored unknown payment missing", pending, err)
	}
	order := pending[0]
	if err = pay.Verify(ctx, s, order.ID); err != nil {
		t.Fatal(err)
	}
	outcome := payment.Outcome{CallbackID: "restore-confirmed", MerchantID: order.MerchantID, Currency: order.Currency, Amount: order.Amount, Status: "SUCCEEDED"}
	raw, _ := json.Marshal(outcome)
	mac := hmac.New(sha256.New, payProvider.Key)
	mac.Write(raw)
	for range 2 {
		if err = pay.Callback(ctx, s, "sandbox", raw, hex.EncodeToString(mac.Sum(nil))); err != nil {
			t.Fatal(err)
		}
	}
	money, err := (ledger.Repository{}).Get(ctx, store.DB, s, order.Allocations[0].EntitlementID, false)
	if err != nil || money.Target != "100" || money.Available != "60" || money.Reserved != "0" || money.Paid != "40" {
		t.Fatal("restored conservation", money, err)
	}
	task, err := tasks.ByBusiness(ctx, store.DB, s, "sandbox", "order", "FEE_CONFIRMED")
	if err != nil {
		t.Fatal(err)
	}
	var original event.Event
	if err = json.Unmarshal(task.EventBody, &original); err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(original)
	mac = hmac.New(sha256.New, provider.Key)
	mac.Write(raw)
	replayed, err := ingress.Receive(ctx, p, raw, hex.EncodeToString(mac.Sum(nil)), "restored-replay", "fixture")
	if err != nil || replayed.Status != 202 {
		t.Fatal(err, string(replayed.Body))
	}
	processor := calculation.Worker{UOW: store, Tasks: tasks, Committer: ledger.Repository{}, Now: func() time.Time { return now }, Lease: time.Minute}
	if done, err := processor.RunOnce(ctx, s); err != nil || done {
		t.Fatal("restored completed task ran twice", done, err)
	}
	var count int
	if err = store.DB.QueryRow(`SELECT COUNT(*) FROM ledger_entitlements WHERE tenant_id=?`, tenant).Scan(&count); err != nil || count != 1 {
		t.Fatal("restored replay duplicated entitlement", count, err)
	}
	if err = store.DeliverOutbox(ctx, s, func(e mysql.OutboxEvent) error {
		return (catalog.Repository{}).Send(ctx, store.DB, e.Scope, e.ID, e.Topic, e.Payload, e.CreatedAt)
	}); err != nil {
		t.Fatal(err)
	}
	t.Log("restored database: original merchant ID verified, signed callback replay idempotent, event replay did not pay twice, money conserved")
}
