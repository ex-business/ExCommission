package runtime

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"

	identity "github.com/ex-business/ExCommission/internal/identity/adapters"
	principal "github.com/ex-business/ExCommission/internal/identity/domain"
	"github.com/ex-business/ExCommission/internal/identity/ports"
	"github.com/ex-business/ExCommission/internal/platform/adapters/httpapi"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
)

func loginProvider(legacy ports.Provider, p1, p2 principal.Principal) (*identity.Sessions, error) {
	var accounts []identity.LoginAccount
	if path := os.Getenv("AUTH_LOCAL_ACCOUNTS_FILE"); path != "" {
		body, err := os.ReadFile(path)
		if err != nil {
			return nil, errors.New("local login configuration unavailable")
		}
		if json.Unmarshal(body, &accounts) != nil {
			return nil, errors.New("local login configuration invalid")
		}
		for i := range accounts {
			switch accounts[i].Username {
			case "operator":
				accounts[i].Principal = p1
			case "approver":
				accounts[i].Principal = p2
			default:
				return nil, errors.New("unregistered local login account")
			}
		}
	}
	var external ports.Provider
	if path := os.Getenv("AUTH_EXTERNAL_FILE"); path != "" {
		body, err := os.ReadFile(path)
		if err != nil {
			return nil, errors.New("external identity configuration unavailable")
		}
		var c identity.ExternalConfig
		if json.Unmarshal(body, &c) != nil {
			return nil, errors.New("external identity configuration invalid")
		}
		if strings.TrimSpace(string(body)) != "{}" {
			external, err = identity.NewIntrospection(c, map[string]principal.Principal{p1.Subject: p1, p2.Subject: p2})
			if err != nil {
				return nil, err
			}
		}
	}
	return identity.NewSessions(accounts, legacy, external)
}
func installLogin(mux *http.ServeMux, store *mysql.Store, provider *identity.Sessions, scope foundation.Scope) {
	write := func(w http.ResponseWriter, status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("GET /v1/auth/config", func(w http.ResponseWriter, r *http.Request) {
		write(w, 200, map[string]any{"local_password": os.Getenv("AUTH_LOCAL_ACCOUNTS_FILE") != "", "external_bearer": provider.External != nil, "external_login_url": nil})
	})
	mux.Handle("POST /v1/auth/login", auditHTTP(store, nil, scope, "auth:login", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		var in struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		d.DisallowUnknownFields()
		err := d.Decode(&in)
		if err != nil || d.Decode(&struct{}{}) != io.EOF || len(in.Username) > 128 || len(in.Password) > 1024 || in.Password == "" {
			httpapi.Error(w, newID(), foundation.ErrValidation)
			return
		}
		token, p, err := provider.Login(in.Username, in.Password)
		if err != nil {
			if errors.Is(err, identity.ErrLoginRate) {
				w.Header().Set("Retry-After", "60")
				write(w, 429, map[string]any{"error": map[string]any{"code": "LOGIN_RATE_LIMITED", "message": "LOGIN_RATE_LIMITED", "retryable": false, "resource_ref": nil, "details": map[string]string{}}, "request_id": newID()})
			} else if errors.Is(err, principal.ErrUnauthenticated) {
				write(w, 401, map[string]any{"error": map[string]any{"code": "UNAUTHENTICATED", "message": "UNAUTHENTICATED", "retryable": false, "resource_ref": nil, "details": map[string]string{}}, "request_id": newID()})
			} else {
				httpapi.Error(w, newID(), err)
			}
			return
		}
		write(w, 200, map[string]any{"access_token": token, "token_type": "Bearer", "expires_in": 28800, "principal": map[string]string{"subject_id": p.Subject, "tenant_id": p.Tenant, "app_id": p.App}})
	})))
	authenticated := func(action string, next func(http.ResponseWriter, *http.Request, principal.Principal)) http.Handler {
		return auditHTTP(store, provider, scope, action, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			p, err := provider.Authenticate(r.Context(), r.Header.Get("Authorization"))
			if err != nil {
				if errors.Is(err, principal.ErrUnauthenticated) {
					write(w, 401, map[string]any{"error": map[string]any{"code": "UNAUTHENTICATED", "message": "UNAUTHENTICATED", "retryable": false, "resource_ref": nil, "details": map[string]string{}}, "request_id": newID()})
				} else {
					write(w, 503, map[string]any{"error": map[string]any{"code": "DEPENDENCY_UNAVAILABLE", "message": "DEPENDENCY_UNAVAILABLE", "retryable": true, "resource_ref": nil, "details": map[string]string{}}, "request_id": newID()})
				}
				return
			}
			if (r.Header.Get("X-Tenant-ID") != "" && r.Header.Get("X-Tenant-ID") != p.Tenant) || (r.Header.Get("X-App-ID") != "" && r.Header.Get("X-App-ID") != p.App) {
				write(w, 403, map[string]any{"error": map[string]any{"code": "PERMISSION_DENIED", "message": "PERMISSION_DENIED", "retryable": false, "resource_ref": nil, "details": map[string]string{}}, "request_id": newID()})
				return
			}
			next(w, r, p)
		}))
	}
	mux.Handle("GET /v1/auth/me", authenticated("auth:me", func(w http.ResponseWriter, r *http.Request, p principal.Principal) {
		grants, revision, err := identity.SubjectGrants(r.Context(), store.DB, foundation.Scope{Tenant: p.Tenant, App: p.App}, p.Subject)
		if err != nil {
			httpapi.Error(w, newID(), err)
			return
		}
		write(w, 200, map[string]any{"subject_id": p.Subject, "tenant_id": p.Tenant, "app_id": p.App, "permissions": grants, "revision": revision})
	}))
	mux.Handle("POST /v1/auth/logout", authenticated("auth:logout", func(w http.ResponseWriter, r *http.Request, p principal.Principal) {
		provider.Logout(r.Header.Get("Authorization"))
		w.WriteHeader(204)
	}))
}
