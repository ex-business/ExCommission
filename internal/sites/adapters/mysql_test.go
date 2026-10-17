package adapters

import (
	"context"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	app "github.com/ex-business/ExCommission/internal/sites/application"
	model "github.com/ex-business/ExCommission/internal/sites/domain"
	"os"
	"strconv"
	"testing"
	"time"
)

func TestDisableWaitsForCommittedRewardGate(t *testing.T) {
	if os.Getenv("TEST_MYSQL_ADDRESS") == "" {
		t.Skip("MySQL required")
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
	scope := foundation.Scope{Tenant: "test-site-lock-" + strconv.FormatInt(time.Now().UnixNano(), 10), App: "app"}
	defer func() {
		for _, table := range []string{"site_agent_states", "site_agent_versions"} {
			if _, e := store.DB.Exec("DELETE FROM "+table+" WHERE tenant_id=?", scope.Tenant); e != nil {
				t.Error(e)
			}
		}
	}()
	repo := Repository{}
	if err = repo.Ensure(ctx, store.DB, scope); err != nil {
		t.Fatal(err)
	}
	service := app.Service{Repo: repo}
	gated := make(chan struct{})
	release := make(chan struct{})
	first := make(chan error, 1)
	off := make(chan error, 1)
	go func() {
		first <- store.Within(ctx, func(tx db.Executor) error {
			if e := service.Allow(ctx, tx, scope); e != nil {
				return e
			}
			close(gated)
			<-release
			return nil
		})
	}()
	<-gated
	go func() {
		off <- store.Within(ctx, func(tx db.Executor) error {
			return repo.Apply(ctx, tx, scope, model.Change{Enabled: false, PlanID: "P1", Venues: []string{}, Reason: "stop"}, "approval", time.Now())
		})
	}()
	select {
	case e := <-off:
		close(release)
		t.Fatalf("disable raced past shared business gate: %v", e)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if e := <-first; e != nil {
		t.Fatal(e)
	}
	if e := <-off; e != nil {
		t.Fatal(e)
	}
	if e := store.Within(ctx, func(tx db.Executor) error { return service.Allow(ctx, tx, scope) }); e == nil || e.Error() != "SITE_AGENT_DISABLED" {
		t.Fatal(e)
	}
}
