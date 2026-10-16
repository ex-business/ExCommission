package runtime

import (
	"bytes"
	"context"
	audit "github.com/ex-business/ExCommission/internal/audit/adapters"
	identityports "github.com/ex-business/ExCommission/internal/identity/ports"
	"github.com/ex-business/ExCommission/internal/platform/adapters/httpapi"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"net/http"
	"strconv"
	"time"
)

func installAuditObserver(store *mysql.Store) {
	store.Audit = func(ctx context.Context, tx db.Executor, scope foundation.Scope, action, resource, code string) error {
		metadata := foundation.RequestFrom(ctx)
		subject := metadata.Subject
		rid := metadata.RequestID
		if subject == "" {
			subject = "system"
		}
		if rid == "" {
			rid = newID()
		}
		return audit.Append(ctx, tx, audit.Entry{Tenant: scope.Tenant, App: scope.App, ID: newID(), Subject: subject, RequestID: rid, Type: "OPERATION", Action: action, Resource: resource, Summary: map[string]string{"result_code": code, "object_hash": metadata.ObjectHash, "before_revision": metadata.BeforeRevision, "after_revision": metadata.AfterRevision}, At: clockUTC()})
	}
}

type bufferedResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (b *bufferedResponse) Header() http.Header { return b.header }
func (b *bufferedResponse) WriteHeader(status int) {
	if b.status == 0 {
		b.status = status
	}
}
func (b *bufferedResponse) Write(body []byte) (int, error) {
	if b.status == 0 {
		b.status = 200
	}
	return b.body.Write(body)
}
func auditHTTP(store *mysql.Store, provider identityports.Provider, scope foundation.Scope, action string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rid := newID()
		subject := "anonymous"
		if provider != nil {
			if p, err := provider.Authenticate(r.Context(), r.Header.Get("Authorization")); err == nil {
				subject = p.Subject
			}
		}
		ctx := foundation.WithOperation(foundation.WithScope(foundation.WithRequest(r.Context(), rid, subject), scope), action, false)
		r = r.WithContext(ctx)
		buffer := &bufferedResponse{header: http.Header{}}
		next.ServeHTTP(buffer, r)
		if buffer.status == 0 {
			buffer.status = 200
		}
		kind := "OPERATION"
		if buffer.status == 401 || buffer.status == 403 || action == "reports:export" || action == "reports:download" {
			kind = "SECURITY"
		}
		auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		err := audit.Append(auditCtx, store.DB, audit.Entry{Tenant: scope.Tenant, App: scope.App, ID: newID(), Subject: subject, RequestID: rid, Type: kind, Action: action, Resource: r.URL.Path, Summary: map[string]string{"result_code": strconv.Itoa(buffer.status)}, At: clockUTC()})
		if err != nil {
			httpapi.Error(w, rid, err)
			return
		}
		for key, values := range buffer.header {
			for _, v := range values {
				w.Header().Add(key, v)
			}
		}
		w.WriteHeader(buffer.status)
		_, _ = w.Write(buffer.body.Bytes())
	})
}
