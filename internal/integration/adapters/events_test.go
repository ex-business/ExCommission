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
	plans "github.com/ex-business/ExCommission/internal/plans/adapters"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	users "github.com/ex-business/ExCommission/internal/users/adapters"
	user "github.com/ex-business/ExCommission/internal/users/domain"
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
		for _, table := range []string{"integration_event_keys", "integration_inbox", "integration_raw_payloads", "calculation_tasks", "calculation_recoveries", "command_results", "users_external", "agent_accounts", "agent_relations", "agent_relation_spaces", "agent_grade_history", "agent_plan_states", "plan_drafts", "plan_versions", "plan_route_spaces", "user_agent_bindings"} {
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
