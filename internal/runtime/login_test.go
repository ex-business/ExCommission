package runtime

import (
	"bytes"
	"context"
	"crypto/pbkdf2"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	identity "github.com/ex-business/ExCommission/internal/identity/adapters"
	authhttp "github.com/ex-business/ExCommission/internal/identity/application"
	principal "github.com/ex-business/ExCommission/internal/identity/domain"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
)

func TestLocalPasswordHTTPAuthorizationAndLogout(t *testing.T) {
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
	tenant := "test-login-" + newID()
	scope := foundation.Scope{Tenant: tenant, App: "test"}
	defer func() {
		for _, table := range []string{"identity_grants", "audit_entries"} {
			if _, e := store.DB.Exec("DELETE FROM "+table+" WHERE tenant_id=?", tenant); e != nil {
				t.Error(e)
			}
		}
	}()
	salt := make([]byte, 16)
	hash, err := pbkdf2.Key(sha256.New, "test-secret-password", salt, 600000, 32)
	if err != nil {
		t.Fatal(err)
	}
	p := principal.Principal{Tenant: tenant, App: "test", Subject: "local-operator"}
	sessions, err := identity.NewSessions([]identity.LoginAccount{{Username: "operator", Salt: hex.EncodeToString(salt), Hash: hex.EncodeToString(hash), Principal: p}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.DB.Exec("INSERT INTO identity_grants VALUES(?,?,?,?)", tenant, "test", p.Subject, "agents:read"); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	installLogin(mux, store, sessions, scope)
	mux.Handle("GET /protected", authhttp.Require(sessions, identity.DatabaseAuthorizer{Query: store.DB}, "agents:read", func(w http.ResponseWriter, r *http.Request, p principal.Principal, id string) { w.WriteHeader(204) }))
	call := func(method, path, token, body string, status int) map[string]any {
		t.Helper()
		r := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		v := map[string]any{}
		if w.Body.Len() > 0 {
			if e := json.Unmarshal(w.Body.Bytes(), &v); e != nil {
				t.Fatal(e)
			}
		}
		return v
	}
	call("POST", "/v1/auth/login", "", `{"username":"operator","password":"wrong"}`, 401)
	call("POST", "/v1/auth/login", "", `{"username":"operator","password":"test-secret-password","role":"admin"}`, 400)
	v := call("POST", "/v1/auth/login", "", `{"username":"operator","password":"test-secret-password"}`, 200)
	token := v["access_token"].(string)
	me := call("GET", "/v1/auth/me", token, "", 200)
	if me["subject_id"] != p.Subject || len(me["permissions"].([]any)) != 1 {
		t.Fatal(me)
	}
	call("GET", "/protected", token, "", 204)
	if _, err = store.DB.Exec("DELETE FROM identity_grants WHERE tenant_id=?", tenant); err != nil {
		t.Fatal(err)
	}
	call("GET", "/protected", token, "", 403)
	me = call("GET", "/v1/auth/me", token, "", 200)
	if len(me["permissions"].([]any)) != 0 {
		t.Fatal("stale permissions")
	}
	call("POST", "/v1/auth/logout", token, "", 204)
	call("GET", "/v1/auth/me", token, "", 401)
	var leaked int
	if err = store.DB.QueryRow("SELECT COUNT(*) FROM audit_entries WHERE tenant_id=? AND (CAST(summary AS CHAR) LIKE ? OR CAST(summary AS CHAR) LIKE ?)", tenant, "%test-secret-password%", "%"+token+"%").Scan(&leaked); err != nil {
		t.Fatal(err)
	}
	if leaked != 0 {
		t.Fatal("credential leaked")
	}
}
