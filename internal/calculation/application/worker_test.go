package application

import (
	"context"
	"fmt"
	repo "github.com/ex-business/ExCommission/internal/calculation/adapters"
	"github.com/ex-business/ExCommission/internal/calculation/domain"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"os"
	"testing"
	"time"
)

type deadlineResolver struct{}

func (deadlineResolver) Resolve(ctx context.Context, _ db.Executor, _ foundation.Scope, _ domain.Task) (domain.Input, error) {
	<-ctx.Done()
	return domain.Input{}, ctx.Err()
}
func TestDeadlinePersistsRetryUsingRecoveryContext(t *testing.T) {
	if os.Getenv("TEST_MYSQL_ADDRESS") == "" {
		t.Skip("Docker MySQL required")
	}
	store, err := mysql.Open(context.Background(), os.Getenv("TEST_MYSQL_ADDRESS"), "excommission", os.Getenv("MYSQL_PASSWORD"), "excommission")
	if err != nil {
		t.Fatal(err)
	}
	defer store.DB.Close()
	if err = store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	scope := foundation.Scope{Tenant: fmt.Sprint("test-timeout-", time.Now().UnixNano()), App: "app"}
	defer store.DB.Exec(`DELETE FROM calculation_tasks WHERE tenant_id=?`, scope.Tenant)
	at := time.Now().UTC().Truncate(time.Microsecond)
	tasks := repo.Tasks{DB: store.DB, Within: store.Within}
	if err = store.Within(context.Background(), func(tx db.Executor) error {
		return tasks.Create(context.Background(), tx, scope, domain.Task{ID: "task", EventID: "event", Source: "s", SourceEvent: "e", BusinessID: "b", EventType: "FEE_CONFIRMED", BusinessTime: at, EventBody: []byte(`{}`)}, at)
	}); err != nil {
		t.Fatal(err)
	}
	processor := Worker{UOW: store, Tasks: tasks, Resolver: deadlineResolver{}, Now: func() time.Time { return at }, Lease: time.Minute}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if done, err := processor.RunOnce(ctx, scope); err != nil || !done {
		t.Fatalf("timeout handling %t %v", done, err)
	}
	task, err := tasks.Get(context.Background(), store.DB, scope, "task")
	if err != nil || task.Status != "RETRY_WAIT" || task.Retries != 1 {
		t.Fatalf("deadline left task running %+v %v", task, err)
	}
}
