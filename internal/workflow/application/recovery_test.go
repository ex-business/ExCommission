package application

import (
	"context"
	"errors"
	"fmt"
	identity "github.com/ex-business/ExCommission/internal/identity/domain"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	repo "github.com/ex-business/ExCommission/internal/workflow/adapters"
	model "github.com/ex-business/ExCommission/internal/workflow/domain"
	"os"
	"testing"
	"time"
)

func TestApprovedFailedExecutionRecoveryPreservesDecisionAndCancel(t *testing.T) {
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
	s := foundation.Scope{Tenant: fmt.Sprint("test-approval-recovery-", time.Now().UnixNano()), App: "app"}
	defer func() {
		for _, table := range []string{"workflow_execution_attempts", "workflow_requests", "command_results", "platform_revisions"} {
			store.DB.Exec("DELETE FROM "+table+" WHERE tenant_id=?", s.Tenant)
		}
	}()
	now := time.Now().UTC().Truncate(time.Microsecond)
	body := []byte(`{}`)
	hash := repo.Hash(body)
	maker := identity.Principal{Tenant: s.Tenant, App: s.App, Subject: "maker"}
	checker := maker
	checker.Subject = "checker"
	failed := true
	calls, cancelCalls := 0, 0
	service := Service{Commands: store, Query: store.DB, Repo: repo.Repository{}, Now: func() time.Time { return now }, Executors: map[string]Executor{"TEST": func(ctx context.Context, tx db.Executor, s foundation.Scope, a model.Request, at time.Time) error {
		calls++
		if _, err := tx.ExecContext(ctx, `INSERT INTO platform_revisions VALUES(?,?,?,'effect',1)`, s.Tenant, s.App, a.ID); err != nil {
			return err
		}
		if failed {
			return errors.New("TRANSIENT_DOMAIN_DEPENDENCY")
		}
		return nil
	}}, Rejections: map[string]Executor{"TEST": func(ctx context.Context, tx db.Executor, s foundation.Scope, a model.Request, at time.Time) error {
		cancelCalls++
		_, err := tx.ExecContext(ctx, `INSERT INTO platform_revisions VALUES(?,?,?,'cancelled',1)`, s.Tenant, s.App, a.ID)
		return err
	}}}
	create := func(id string) {
		t.Helper()
		if err := store.Within(ctx, func(tx db.Executor) error {
			return service.Repo.Create(ctx, tx, s, model.Request{ID: id, Type: "TEST", Applicant: "maker", ObjectRef: id, ObjectHash: hash, Payload: body}, "fixture", now)
		}); err != nil {
			t.Fatal(err)
		}
	}
	create("recover")
	decided, err := service.Decide(ctx, checker, "recover", Decision{Decision: "APPROVE", Revision: 0, ObjectHash: hash, Reason: "independent review"}, "decide", "r")
	if err != nil || decided.Status != 200 {
		t.Fatal(err, string(decided.Body))
	}
	a, err := service.Get(ctx, checker, "recover")
	if err != nil || a.Status != "APPROVED" || a.Execution != "FAILED" {
		t.Fatal(a, err)
	}
	var count int
	store.DB.QueryRow(`SELECT COUNT(*) FROM platform_revisions WHERE tenant_id=?`, s.Tenant).Scan(&count)
	if count != 0 {
		t.Fatal("failed execution partially committed")
	}
	var originalAt time.Time
	var originalActor string
	if err = store.DB.QueryRow(`SELECT decided_at,decision_by FROM workflow_requests WHERE tenant_id=? AND approval_id='recover'`, s.Tenant).Scan(&originalAt, &originalActor); err != nil {
		t.Fatal(err)
	}
	input := RecoveryInput{Revision: a.Revision, Hash: hash, Reason: "dependency recovered", Evidence: "verified-proof"}
	self, err := service.RetryExecution(ctx, maker, "recover", input, "self-retry", "r")
	if err != nil || self.Status != 403 {
		t.Fatal("self retry bypass", self.Status, err)
	}
	failed = false
	now = now.Add(time.Second)
	retried, err := service.RetryExecution(ctx, checker, "recover", input, "retry", "r")
	if err != nil || retried.Status != 200 {
		t.Fatal(err, string(retried.Body))
	}
	a, err = service.Get(ctx, checker, "recover")
	if err != nil || a.Execution != "SUCCEEDED" || a.Revision != 2 {
		t.Fatal(a, err)
	}
	if _, err = service.RetryExecution(ctx, checker, "recover", input, "retry", "r"); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal("execution replayed", calls)
	}
	var afterAt time.Time
	var afterActor string
	store.DB.QueryRow(`SELECT decided_at,decision_by FROM workflow_requests WHERE tenant_id=? AND approval_id='recover'`, s.Tenant).Scan(&afterAt, &afterActor)
	if !afterAt.Equal(originalAt) || afterActor != originalActor {
		t.Fatal("retry rewrote original approval")
	}
	store.DB.QueryRow(`SELECT COUNT(*) FROM workflow_execution_attempts WHERE tenant_id=?`, s.Tenant).Scan(&count)
	if count != 1 {
		t.Fatal("recovery evidence missing", count)
	}
	create("cancel")
	cancel := Decision{Revision: 0, ObjectHash: hash, Reason: "withdrawn"}
	denied, err := service.Cancel(ctx, checker, "cancel", cancel, "other-cancel", "r")
	if err != nil || denied.Status != 403 {
		t.Fatal("nonapplicant cancellation", denied.Status, err)
	}
	cancelled, err := service.Cancel(ctx, maker, "cancel", cancel, "cancel", "r")
	if err != nil || cancelled.Status != 200 {
		t.Fatal(err, string(cancelled.Body))
	}
	if _, err = service.Cancel(ctx, maker, "cancel", cancel, "cancel", "r"); err != nil {
		t.Fatal(err)
	}
	a, err = service.Get(ctx, maker, "cancel")
	if err != nil || a.Status != "CANCELLED" || cancelCalls != 1 {
		t.Fatal(a, cancelCalls, err)
	}
}
