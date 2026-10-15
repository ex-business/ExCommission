package runtime

import (
	"context"
	"errors"
	identity "github.com/ex-business/ExCommission/internal/identity/adapters"
	authhttp "github.com/ex-business/ExCommission/internal/identity/application"
	principal "github.com/ex-business/ExCommission/internal/identity/domain"
	"github.com/ex-business/ExCommission/internal/platform/adapters/httpapi"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	response "github.com/ex-business/ExCommission/internal/platform/application"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	repo "github.com/ex-business/ExCommission/internal/reporting/adapters"
	reporting "github.com/ex-business/ExCommission/internal/reporting/application"
	model "github.com/ex-business/ExCommission/internal/reporting/domain"
	"net/http"
	"time"
)

func installReports(mux *http.ServeMux, require func(string, authhttp.AuthorizedHandler) http.Handler, store *mysql.Store, tenant, app string) error {
	for _, v := range []struct {
		subject     string
		permissions []string
	}{{"local-operator", []string{"reports:read", "reports:export", "reports:download"}}, {"local-approver", []string{"reports:read"}}} {
		if err := bootstrapFeature(context.Background(), store, tenant, app, v.subject, "reports-v1", v.permissions); err != nil {
			return err
		}
	}
	service := reporting.Service{Commands: store, Query: store.DB, Reader: repo.Reader{DB: store.DB}, Exports: repo.Exports{}, Now: clockUTC, ID: newID, TTL: 24 * time.Hour}
	mux.Handle("GET /v1/reports/{kind}", require("reports:read", func(w http.ResponseWriter, r *http.Request, p principal.Principal, id string) {
		f := model.Filters{PlanID: r.URL.Query().Get("plan_id"), VersionID: r.URL.Query().Get("version_id"), AgentID: r.URL.Query().Get("agent_id"), Currency: r.URL.Query().Get("currency")}
		for _, entry := range []struct {
			name   string
			target **time.Time
		}{{"from", &f.From}, {"to", &f.To}} {
			if value := r.URL.Query().Get(entry.name); value != "" {
				at, err := time.Parse(time.RFC3339Nano, value)
				if err != nil {
					httpapi.Error(w, id, foundation.ErrValidation)
					return
				}
				*entry.target = &at
			}
		}
		report, err := service.Reader.Read(r.Context(), foundation.Scope{Tenant: p.Tenant, App: p.App}, r.PathValue("kind"), f)
		if err != nil {
			httpapi.Error(w, id, err)
			return
		}
		result, _ := response.Result(200, "report-"+r.PathValue("kind"), id, report)
		httpapi.Write(w, result)
	}))
	mux.Handle("POST /v1/report-exports", require("reports:export", func(w http.ResponseWriter, r *http.Request, p principal.Principal, id string) {
		if err := identity.Authorize(r.Context(), store.DB, p, p.Tenant, p.App, "reports:read"); err != nil {
			httpapi.Error(w, id, err)
			return
		}
		var input reporting.ExportInput
		if err := httpapi.Decode(w, r, &input); err != nil {
			httpapi.Error(w, id, err)
			return
		}
		result, err := service.CreateExport(r.Context(), p, input, r.Header.Get("Idempotency-Key"), id)
		if err != nil {
			httpapi.Error(w, id, err)
			return
		}
		httpapi.Write(w, result)
	}))
	mux.Handle("GET /v1/report-exports/{id}", require("reports:read", func(w http.ResponseWriter, r *http.Request, p principal.Principal, id string) {
		v, err := service.Exports.Get(r.Context(), store.DB, foundation.Scope{Tenant: p.Tenant, App: p.App}, r.PathValue("id"), clockUTC())
		if err != nil {
			httpapi.Error(w, id, err)
			return
		}
		result, _ := response.Result(200, v.ID, id, v)
		httpapi.Write(w, result)
	}))
	mux.Handle("GET /v1/report-exports/{id}/download", require("reports:download", func(w http.ResponseWriter, r *http.Request, p principal.Principal, id string) {
		if err := identity.Authorize(r.Context(), store.DB, p, p.Tenant, p.App, "reports:read"); err != nil {
			httpapi.Error(w, id, err)
			return
		}
		v, err := service.Exports.Get(r.Context(), store.DB, foundation.Scope{Tenant: p.Tenant, App: p.App}, r.PathValue("id"), clockUTC())
		if err != nil {
			httpapi.Error(w, id, err)
			return
		}
		if v.Status == "EXPIRED" {
			httpapi.Error(w, id, reportExpired)
			return
		}
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="`+v.ID+`.csv"`)
		w.Header().Set("ETag", `"`+v.Hash+`"`)
		w.Header().Set("Cache-Control", "private, no-store")
		w.WriteHeader(200)
		_, _ = w.Write(v.CSV)
	}))
	return nil
}

var reportExpired = errors.New("REPORT_EXPIRED")
