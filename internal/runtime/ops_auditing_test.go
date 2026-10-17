package runtime

import (
	"context"
	identity "github.com/ex-business/ExCommission/internal/identity/adapters"
	principal "github.com/ex-business/ExCommission/internal/identity/domain"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestOpsAuditUsesAuthenticatedSiteAndPreservesLegacy(t *testing.T) {
	if os.Getenv("TEST_MYSQL_ADDRESS") == "" {
		t.Skip("需要MySQL测试连接")
	}
	store, e := mysql.Open(context.Background(), os.Getenv("TEST_MYSQL_ADDRESS"), "excommission", os.Getenv("MYSQL_PASSWORD"), "excommission")
	if e != nil {
		t.Fatal(e)
	}
	defer store.DB.Close()
	if e = store.Migrate(context.Background()); e != nil {
		t.Fatal(e)
	}
	prefix := "test-ops-audit-" + newID()
	scopes := []foundation.Scope{{Tenant: prefix + "-default", App: "app"}, {Tenant: prefix + "-a", App: "app-a"}, {Tenant: prefix + "-b", App: "app-b"}}
	defer func() {
		for _, scope := range scopes {
			for _, table := range []string{"audit_handling_records", "audit_security_states", "audit_entries"} {
				if _, e := store.DB.Exec("DELETE FROM "+table+" WHERE tenant_id=?", scope.Tenant); e != nil {
					t.Error(e)
				}
			}
		}
	}()
	ta, tb, legacy := strings.Repeat("a", 32), strings.Repeat("b", 32), strings.Repeat("l", 32)
	provider := identity.NewLocalProvider(map[string]principal.Principal{ta: {Subject: "ops-1", Tenant: scopes[1].Tenant, App: scopes[1].App, OpsCredential: "ops1.fixture"}, tb: {Subject: "ops-2", Tenant: scopes[2].Tenant, App: scopes[2].App, OpsCredential: "ops1.fixture"}, legacy: {Subject: "local-operator", Tenant: scopes[0].Tenant, App: scopes[0].App}})
	for i, c := range []struct {
		token, subject string
		scope          foundation.Scope
	}{{ta, "ops-1", scopes[1]}, {tb, "ops-2", scopes[2]}, {legacy, "local-operator", scopes[0]}, {"invalid", "anonymous", scopes[0]}} {
		path := "/test-audit/" + newID()
		handler := auditHTTP(store, provider, scopes[0], "affiliate:read", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "Bearer "+c.token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != 204 {
			t.Fatalf("第%d个审计请求失败 %d %s", i, w.Code, w.Body.String())
		}
		var tenant, app, subject string
		if e = store.DB.QueryRow(`SELECT tenant_id,app_id,subject_id FROM audit_entries WHERE resource_ref=?`, path).Scan(&tenant, &app, &subject); e != nil {
			t.Fatal(e)
		}
		if tenant != c.scope.Tenant || app != c.scope.App || subject != c.subject {
			t.Fatalf("审计归属错误 %s %s %s", tenant, app, subject)
		}
	}
}
