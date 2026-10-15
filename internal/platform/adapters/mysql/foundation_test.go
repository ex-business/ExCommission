package mysql_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	audit "github.com/ex-business/ExCommission/internal/audit/adapters"
	identity "github.com/ex-business/ExCommission/internal/identity/adapters"
	auth "github.com/ex-business/ExCommission/internal/identity/domain"
	integration "github.com/ex-business/ExCommission/internal/integration/adapters"
	db "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	"github.com/ex-business/ExCommission/internal/platform/ports"
)

func open(t *testing.T) *db.Store {
	t.Helper()
	address := os.Getenv("TEST_MYSQL_ADDRESS")
	if address == "" {
		t.Skip("integration: TEST_MYSQL_ADDRESS not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store, err := db.Open(ctx, address, "excommission", os.Getenv("MYSQL_PASSWORD"), "excommission")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.DB.Close() })
	if err = store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return store
}
func scope(t *testing.T, store *db.Store) db.Scope {
	t.Helper()
	bytes := make([]byte, 12)
	if _, err := rand.Read(bytes); err != nil {
		t.Fatal(err)
	}
	s := db.Scope{Tenant: "test-" + hex.EncodeToString(bytes), App: "app"}
	t.Cleanup(func() {
		for _, table := range []string{"command_results", "integration_inbox", "transaction_outbox", "identity_grants", "audit_entries", "platform_revisions"} {
			_, err := store.DB.Exec("DELETE FROM "+table+" WHERE tenant_id=?", s.Tenant)
			if err != nil {
				t.Error(err)
			}
		}
	})
	return s
}
func result() db.Result {
	return db.Result{Status: 202, Body: []byte(`{"data":{"resource_id":"r1"},"request_id":"q1"}`), ResourceID: "r1"}
}
func TestCommandConcurrentReplayAndConflict(t *testing.T) {
	store := open(t)
	s := scope(t, store)
	ctx := context.Background()
	body, _ := db.CanonicalJSON([]byte(`{"a":"1","b":"2"}`))
	var calls atomic.Int32
	var wg sync.WaitGroup
	failures := make(chan error, 100)
	for range 100 {
		wg.Go(func() {
			r, err := store.Command(ctx, s, "test", "one-key", body, func(tx ports.Executor) (db.Result, error) {
				calls.Add(1)
				return result(), db.AppendOutbox(ctx, tx, db.OutboxEvent{Scope: s, ID: "out1", Topic: "test", Payload: []byte(`{}`), CreatedAt: time.Now()})
			})
			if err != nil {
				failures <- err
			} else if r.Status != 202 {
				failures <- errors.New("wrong replay result")
			}
		})
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("callback %d", calls.Load())
	}
	_, err := store.Command(ctx, s, "test", "one-key", []byte(`{"a":"different"}`), func(ports.Executor) (db.Result, error) { t.Fatal("conflict callback executed"); return result(), nil })
	if !errors.Is(err, db.ErrConflict) {
		t.Fatalf("conflict: %v", err)
	}
	var count int
	store.DB.QueryRow(`SELECT COUNT(*) FROM transaction_outbox WHERE tenant_id=?`, s.Tenant).Scan(&count)
	if count != 1 {
		t.Fatalf("outbox %d", count)
	}
}
func TestAtomicRollbackAndDeterministicFailure(t *testing.T) {
	store := open(t)
	s := scope(t, store)
	ctx := context.Background()
	boom := errors.New("before commit")
	_, err := store.Command(ctx, s, "rollback", "k", []byte(`{}`), func(tx ports.Executor) (db.Result, error) {
		if err := db.AppendOutbox(ctx, tx, db.OutboxEvent{Scope: s, ID: "rolled", Topic: "test", Payload: []byte(`{}`), CreatedAt: time.Now()}); err != nil {
			return db.Result{}, err
		}
		return db.Result{}, boom
	})
	if !errors.Is(err, boom) {
		t.Fatal(err)
	}
	for _, table := range []string{"command_results", "transaction_outbox"} {
		var count int
		store.DB.QueryRow("SELECT COUNT(*) FROM "+table+" WHERE tenant_id=?", s.Tenant).Scan(&count)
		if count != 0 {
			t.Fatal("partial commit", table)
		}
	}
	calls := 0
	fn := func(ports.Executor) (db.Result, error) { calls++; r := result(); r.Status = 422; return r, nil }
	for range 2 {
		r, err := store.Command(ctx, s, "definite", "k", []byte(`{}`), fn)
		if err != nil || r.Status != 422 {
			t.Fatalf("%v %v", r, err)
		}
	}
	if calls != 1 {
		t.Fatal("business failure not replayed")
	}
}
func TestAuthorizationAndTenantIsolation(t *testing.T) {
	store := open(t)
	s := scope(t, store)
	ctx := context.Background()
	p := auth.Principal{Tenant: s.Tenant, App: s.App, Subject: "operator"}
	_, err := store.DB.Exec(`INSERT INTO identity_grants VALUES(?,?,?,?)`, s.Tenant, s.App, p.Subject, "agents:write")
	if err != nil {
		t.Fatal(err)
	}
	if err = identity.Authorize(ctx, store.DB, p, s.Tenant, s.App, "agents:write"); err != nil {
		t.Fatal(err)
	}
	for _, target := range []db.Scope{{Tenant: "other", App: s.App}, {Tenant: s.Tenant, App: "other"}} {
		if err = identity.Authorize(ctx, store.DB, p, target.Tenant, target.App, "agents:write"); !errors.Is(err, auth.ErrPermission) {
			t.Fatal("cross scope accepted")
		}
	}
	if _, err = store.DB.Exec(`DELETE FROM identity_grants WHERE tenant_id=?`, s.Tenant); err != nil {
		t.Fatal(err)
	}
	if err = identity.Authorize(ctx, store.DB, p, s.Tenant, s.App, "agents:write"); !errors.Is(err, auth.ErrPermission) {
		t.Fatal("revoked permission accepted")
	}
	for _, tenant := range []string{s.Tenant, s.Tenant + "-other"} {
		r, err := store.Command(ctx, db.Scope{Tenant: tenant, App: s.App}, "test", "same-key", []byte(`{}`), func(ports.Executor) (db.Result, error) { return result(), nil })
		if err != nil || r.Replayed {
			t.Fatalf("scope collision %v", err)
		}
	}
	_, _ = store.DB.Exec(`DELETE FROM command_results WHERE tenant_id=? AND app_id=?`, s.Tenant+"-other", s.App)
}
func TestRevisionInboxAuditAndRecovery(t *testing.T) {
	store := open(t)
	s := scope(t, store)
	ctx := context.Background()
	for i, want := range []error{nil, db.ErrRevision} {
		err := store.Within(ctx, func(tx ports.Executor) error { return db.AdvanceRevision(ctx, tx, s, "config", "C1", 0) })
		if !errors.Is(err, want) {
			t.Fatalf("revision %d: %v", i, err)
		}
	}
	accept := func(eventID string, body []byte) error {
		return store.Within(ctx, func(tx ports.Executor) error {
			_, _, err := integration.AcceptInbox(ctx, tx, integration.InboxEvent{Scope: s, Source: "S1", EventID: eventID, BusinessID: "B1", Type: "FEE_CONFIRMED", CanonicalBusiness: body, At: time.Now()})
			return err
		})
	}
	if err := accept("E1", []byte(`{"amount":"1"}`)); err != nil {
		t.Fatal(err)
	}
	if err := accept("E2", []byte(`{"amount":"1"}`)); err != nil {
		t.Fatal("new event should dedupe", err)
	}
	if err := accept("E3", []byte(`{"amount":"2"}`)); !errors.Is(err, db.ErrConflict) {
		t.Fatal("business conflict", err)
	}
	err := store.Within(ctx, func(tx ports.Executor) error {
		return audit.Append(ctx, tx, audit.Entry{Tenant: s.Tenant, App: s.App, ID: "a1", Subject: "actor", RequestID: "request", Type: "OPERATION", Action: "test", Resource: "r1", Summary: map[string]string{"password": "secret", "result_code": "OK"}, At: time.Now()})
	})
	if err != nil {
		t.Fatal(err)
	}
	var summary string
	if err = store.DB.QueryRow(`SELECT summary FROM audit_entries WHERE tenant_id=?`, s.Tenant).Scan(&summary); err != nil {
		t.Fatal(err)
	}
	if summary != `{"result_code": "OK"}` {
		t.Fatalf("unsafe audit %s", summary)
	}
	if err = store.Within(ctx, func(tx ports.Executor) error {
		return db.AppendOutbox(ctx, tx, db.OutboxEvent{Scope: s, ID: "recover", Topic: "audit", Payload: []byte(`{}`), CreatedAt: time.Now()})
	}); err != nil {
		t.Fatal(err)
	}
	if err = store.DeliverOutbox(ctx, s, func(db.OutboxEvent) error { return errors.New("sink unavailable") }); err == nil {
		t.Fatal("delivery failure swallowed")
	}
	delivered := 0
	for range 2 {
		if err = store.DeliverOutbox(ctx, s, func(db.OutboxEvent) error { delivered++; return nil }); err != nil {
			t.Fatal(err)
		}
	}
	if delivered != 1 {
		t.Fatalf("delivered %d", delivered)
	}
}
func TestProcessCrashAfterCommitOutboxRecovery(t *testing.T) {
	if os.Getenv("EXCOMMISSION_AFTER_COMMIT_HELPER") == "1" {
		store := open(t)
		s := db.Scope{Tenant: os.Getenv("EXCOMMISSION_CRASH_TENANT"), App: "app"}
		_, err := store.Command(context.Background(), s, "crash-commit", "key", []byte(`{}`), func(tx ports.Executor) (db.Result, error) {
			return result(), db.AppendOutbox(context.Background(), tx, db.OutboxEvent{Scope: s, ID: "after-commit", Topic: "test", Payload: []byte(`{}`), CreatedAt: time.Now()})
		})
		if err != nil {
			os.Exit(3)
		}
		os.Exit(0)
	}
	store := open(t)
	s := scope(t, store)
	command := exec.Command(os.Args[0], "-test.run=^TestProcessCrashAfterCommitOutboxRecovery$")
	command.Env = append(os.Environ(), "EXCOMMISSION_AFTER_COMMIT_HELPER=1", "EXCOMMISSION_CRASH_TENANT="+s.Tenant)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("helper: %v %s", err, output)
	}
	r, err := store.Command(context.Background(), s, "crash-commit", "key", []byte(`{}`), func(ports.Executor) (db.Result, error) { t.Fatal("committed command repeated"); return result(), nil })
	if err != nil || !r.Replayed {
		t.Fatalf("post-crash replay %v", err)
	}
	calls := 0
	if err = store.DeliverOutbox(context.Background(), s, func(db.OutboxEvent) error { calls++; return nil }); err != nil || calls != 1 {
		t.Fatalf("outbox recovery %d %v", calls, err)
	}
}

func TestProcessCrashRollback(t *testing.T) {
	if os.Getenv("EXCOMMISSION_CRASH_HELPER") == "1" {
		store := open(t)
		tx, err := store.DB.Begin()
		if err != nil {
			os.Exit(3)
		}
		_, err = tx.Exec(`INSERT INTO platform_revisions VALUES(?,?,?,?,1)`, os.Getenv("EXCOMMISSION_CRASH_TENANT"), "app", "crash", "before-commit")
		if err != nil {
			os.Exit(4)
		}
		os.Exit(0)
	}
	store := open(t)
	s := scope(t, store)
	command := exec.Command(os.Args[0], "-test.run=^TestProcessCrashRollback$")
	command.Env = append(os.Environ(), "EXCOMMISSION_CRASH_HELPER=1", "EXCOMMISSION_CRASH_TENANT="+s.Tenant)
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("helper %v %s", err, out)
	}
	var count int
	if err := store.DB.QueryRow(`SELECT COUNT(*) FROM platform_revisions WHERE tenant_id=?`, s.Tenant).Scan(&count); err != nil || count != 0 {
		t.Fatalf("crash leaked write %d %v", count, err)
	}
}
