package runtime

import (
	"context"
	"encoding/json"
	identity "github.com/ex-business/ExCommission/internal/identity/adapters"
	principal "github.com/ex-business/ExCommission/internal/identity/domain"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMemberExternalBindingRequiresTrustedUserAndSubject(t *testing.T) {
	sub := "external-member"
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"active": true, "sub": sub, "iss": "issuer", "aud": "commission", "exp": time.Now().Add(time.Hour).Unix()})
	}))
	defer server.Close()
	file := filepath.Join(t.TempDir(), "external.json")
	c := identity.ExternalConfig{Endpoint: server.URL, ClientID: "client", ClientSecret: "test-secret", Issuer: "issuer", Audience: "commission", Bindings: map[string]string{"external-member": "member-registered"}, Members: map[string]string{"member-registered": "usr-trusted"}}
	write := func() {
		b, _ := json.Marshal(c)
		if err := os.WriteFile(file, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write()
	t.Setenv("AUTH_EXTERNAL_FILE", file)
	t.Setenv("AUTH_LOCAL_ACCOUNTS_FILE", "")
	p1 := principal.Principal{Tenant: "tenant", App: "app", Subject: "local-operator"}
	p2 := principal.Principal{Tenant: "tenant", App: "app", Subject: "local-approver"}
	sessions, err := loginProvider(nil, p1, p2)
	if err != nil {
		t.Fatal(err)
	}
	external := sessions.External.(*identity.Introspection)
	external.Client = server.Client()
	p, err := sessions.Authenticate(context.Background(), "Bearer test-external-member")
	if err != nil || p.MemberUserID != "usr-trusted" || p.Subject != "member-registered" || p.Tenant != "tenant" || p.App != "app" {
		t.Fatal(p, err)
	}
	if p.Authorize("tenant", "app", "payments:write") == nil {
		t.Fatal("member gained management grant")
	}
	sub = "unregistered"
	if _, err = sessions.Authenticate(context.Background(), "Bearer other"); err == nil {
		t.Fatal("unbound external identity authenticated")
	}
	c.Members = map[string]string{"local-operator": "usr-trusted"}
	write()
	if _, err = loginProvider(nil, p1, p2); err == nil {
		t.Fatal("admin member binding allowed")
	}
}
