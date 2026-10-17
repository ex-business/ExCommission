package runtime

import (
	"context"
	"encoding/json"
	"errors"
	repo "github.com/ex-business/ExCommission/internal/affiliate/adapters"
	app "github.com/ex-business/ExCommission/internal/affiliate/application"
	model "github.com/ex-business/ExCommission/internal/affiliate/domain"
	auth "github.com/ex-business/ExCommission/internal/identity/application"
	principal "github.com/ex-business/ExCommission/internal/identity/domain"
	"github.com/ex-business/ExCommission/internal/platform/adapters/httpapi"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	response "github.com/ex-business/ExCommission/internal/platform/application"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	approvalrepo "github.com/ex-business/ExCommission/internal/workflow/adapters"
	workflow "github.com/ex-business/ExCommission/internal/workflow/application"
	"net/http"
)

func affiliateConfiguration(store *mysql.Store) app.ConfigurationService {
	service := app.ConfigurationService{Commands: store, Query: store.DB, Repo: repo.Repository{}, Workflow: approvalrepo.Repository{}, Now: clockUTC, ID: newID, Hash: approvalrepo.Hash}
	if affiliateFlowEnabled() {
		service.ValidateBusiness = validateAffiliateCurrency
	}
	return service
}
func installAffiliate(ctx context.Context, mux *http.ServeMux, require func(string, auth.AuthorizedHandler) http.Handler, store *mysql.Store, approvals *workflow.Service, tenant, application string) error {
	if e := (repo.Repository{}).Ensure(ctx, store.DB, foundation.Scope{Tenant: tenant, App: application}); e != nil {
		return e
	}
	for _, v := range []struct {
		subject     string
		permissions []string
	}{{"local-operator", []string{"affiliate:read", "affiliate:configure"}}, {"local-approver", []string{"affiliate:read", "affiliate:approve"}}} {
		if e := bootstrapFeature(ctx, store, tenant, application, v.subject, "affiliate-config-v1", v.permissions); e != nil {
			return e
		}
	}
	service := affiliateConfiguration(store)
	approvals.Executors["AFFILIATE_CONFIG"] = service.Execute
	mux.Handle("GET /v1/affiliate/config", require("affiliate:read", func(w http.ResponseWriter, r *http.Request, p principal.Principal, rid string) {
		data, e := service.Repo.Configuration(r.Context(), store.DB, foundation.Scope{Tenant: p.Tenant, App: p.App}, clockUTC(), false)
		if e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		out, _ := response.Result(200, "affiliate-config", rid, data)
		httpapi.Write(w, out)
	}))
	mux.Handle("GET /v1/affiliate/venue-types", require("affiliate:read", func(w http.ResponseWriter, r *http.Request, p principal.Principal, rid string) {
		data, e := service.Repo.Venues(r.Context(), store.DB, foundation.Scope{Tenant: p.Tenant, App: p.App})
		if e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		out, _ := response.Result(200, "affiliate-venues", rid, map[string]any{"rows": data})
		httpapi.Write(w, out)
	}))

	mux.Handle("GET /v1/affiliate/config-requests/{id}", require("affiliate:read", func(w http.ResponseWriter, r *http.Request, p principal.Principal, rid string) {
		a, e := service.Workflow.Get(r.Context(), store.DB, foundation.Scope{Tenant: p.Tenant, App: p.App}, r.PathValue("id"), false)
		if e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		if a.Type != "AFFILIATE_CONFIG" {
			httpapi.Error(w, rid, errors.New("NOT_FOUND"))
			return
		}
		var in model.Change
		if e = json.Unmarshal(a.Payload, &in); e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		out, _ := response.Result(200, a.ID, rid, map[string]any{"approval_id": a.ID, "object_hash": a.ObjectHash, "revision": a.Revision, "approval_status": a.Status, "execution_status": a.Execution, "applicant_id": a.Applicant, "change": in})
		httpapi.Write(w, out)
	}))
	// 预检沿用本站配置权限，只返回切换策略，不发布配置、不兑换或迁移历史资金。
	mux.Handle("POST /v1/affiliate/config-preview", require("affiliate:configure", func(w http.ResponseWriter, r *http.Request, p principal.Principal, rid string) {
		var in model.Change
		if e := httpapi.Decode(w, r, &in); e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		data, e := service.Preview(r.Context(), foundation.Scope{Tenant: p.Tenant, App: p.App}, in)
		if e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		out, e := response.Result(200, "affiliate-config-preview", rid, data)
		if e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		httpapi.Write(w, out)
	}))
	mux.Handle("POST /v1/affiliate/config-requests", require("affiliate:configure", func(w http.ResponseWriter, r *http.Request, p principal.Principal, rid string) {
		var in model.Change
		if e := httpapi.Decode(w, r, &in); e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		out, e := service.Request(r.Context(), p, in, r.Header.Get("Idempotency-Key"), rid)
		if e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		httpapi.Write(w, out)
	}))
	return nil
}
