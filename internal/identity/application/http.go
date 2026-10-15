package application

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/ex-business/ExCommission/internal/identity/domain"
	"github.com/ex-business/ExCommission/internal/identity/ports"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	"net/http"
	"time"
)

type AuthorizedHandler func(http.ResponseWriter, *http.Request, domain.Principal, string)

func Require(provider ports.Provider, authorizer ports.Authorizer, permission string, next AuthorizedHandler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		r = r.WithContext(ctx)
		bytes := make([]byte, 16)
		if _, err := rand.Read(bytes); err != nil {
			http.Error(w, "dependency unavailable", 503)
			return
		}
		requestID := foundation.RequestFrom(r.Context()).RequestID
		if requestID == "" {
			requestID = hex.EncodeToString(bytes)
		}
		fail := func(status int, code string) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": code, "message": code, "retryable": status == 503, "resource_ref": nil, "details": map[string]string{}}, "request_id": requestID})
		}
		p, err := provider.Authenticate(r.Context(), r.Header.Get("Authorization"))
		if err != nil {
			if errors.Is(err, domain.ErrUnauthenticated) {
				fail(401, "UNAUTHENTICATED")
			} else {
				fail(503, "DEPENDENCY_UNAVAILABLE")
			}
			return
		}
		tenant, app := r.Header.Get("X-Tenant-ID"), r.Header.Get("X-App-ID")
		if tenant == "" {
			tenant = p.Tenant
		}
		if app == "" {
			app = p.App
		}
		if p.Tenant != tenant || p.App != app {
			fail(403, "PERMISSION_DENIED")
			return
		}
		if err = authorizer.Authorize(r.Context(), p, tenant, app, permission); err != nil {
			if errors.Is(err, domain.ErrPermission) {
				fail(403, "PERMISSION_DENIED")
			} else {
				fail(503, "DEPENDENCY_UNAVAILABLE")
			}
			return
		}
		r = r.WithContext(foundation.WithScope(foundation.WithRequest(r.Context(), requestID, p.Subject), foundation.Scope{Tenant: p.Tenant, App: p.App}))
		next(w, r, p, requestID)
	})
}
