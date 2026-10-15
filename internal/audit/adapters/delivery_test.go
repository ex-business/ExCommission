package adapters

import (
	"context"
	"fmt"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	"os"
	"strings"
	"testing"
	"time"
)

func TestAuditDeliveryIdempotentRedactedAndScoped(t *testing.T) {
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
	s := foundation.Scope{Tenant: fmt.Sprint("test-audit-delivery-", time.Now().UnixNano()), App: "app"}
	defer store.DB.Exec(`DELETE FROM audit_entries WHERE tenant_id=?`, s.Tenant)
	at := time.Now().UTC().Truncate(time.Microsecond)
	for range 2 {
		if err = Deliver(ctx, store.DB, s, "event", "entitlement.created", []byte(`{"token":"must-not-be-logged"}`), at); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	var summary string
	store.DB.QueryRow(`SELECT COUNT(*),CAST(MAX(summary) AS CHAR) FROM audit_entries WHERE tenant_id=?`, s.Tenant).Scan(&count, &summary)
	if count != 1 || strings.Contains(summary, "must-not-be-logged") {
		t.Fatal("audit delivery duplicated or leaked", count, summary)
	}
	if err = Deliver(ctx, store.DB, s, "event", "entitlement.created", []byte(`{}`), at); err == nil {
		t.Fatal("event content conflict ignored")
	}
	other := s
	other.App = "other"
	if err = Deliver(ctx, store.DB, other, "event", "entitlement.created", []byte(`{}`), at); err != nil {
		t.Fatal("scope collision", err)
	}
}
