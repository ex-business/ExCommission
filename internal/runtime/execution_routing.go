package runtime

import (
	"context"
	"errors"
	repo "github.com/ex-business/ExCommission/internal/executionrouting/adapters"
	app "github.com/ex-business/ExCommission/internal/executionrouting/application"
	model "github.com/ex-business/ExCommission/internal/executionrouting/domain"
	identity "github.com/ex-business/ExCommission/internal/identity/adapters"
	auth "github.com/ex-business/ExCommission/internal/identity/application"
	principal "github.com/ex-business/ExCommission/internal/identity/domain"
	"github.com/ex-business/ExCommission/internal/platform/adapters/httpapi"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	response "github.com/ex-business/ExCommission/internal/platform/application"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	approvals "github.com/ex-business/ExCommission/internal/workflow/adapters"
	workflow "github.com/ex-business/ExCommission/internal/workflow/application"
	"net/http"
	"time"
)

// 独立模块只复用方案权限和审批机制，不把前端传入的站点或引擎标识作为授权依据。
func executionRoutingService(store *mysql.Store) app.Service {
	return app.Service{Commands: store, Query: store.DB, Repo: repo.Repository{}, Workflow: approvals.Repository{}, Now: clockUTC, ID: newID, Hash: approvals.Hash, Authorize: func(ctx context.Context, tx db.Executor, p principal.Principal, permission string) error {
		return identity.Authorize(ctx, tx, p, p.Tenant, p.App, permission)
	}}
}
func installExecutionRouting(ctx context.Context, mux *http.ServeMux, require func(string, auth.AuthorizedHandler) http.Handler, store *mysql.Store, wf *workflow.Service) error {
	// DDL在启动阶段执行，初始化失败禁止带着缺失的防双计约束受理业务。
	if e := (repo.Repository{}).Ensure(ctx, store.DB); e != nil {
		return e
	}
	service := executionRoutingService(store)
	wf.Executors["EXECUTION_ROUTING"] = service.Execute
	send := func(w http.ResponseWriter, rid string, v any) {
		out, e := response.Result(200, "execution-routing", rid, v)
		if e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		httpapi.Write(w, out)
	}
	mux.Handle("GET /v1/execution-routing/config", require("plans:read", func(w http.ResponseWriter, r *http.Request, p principal.Principal, rid string) {
		state, e := service.Repo.Head(r.Context(), store.DB, foundation.Scope{Tenant: p.Tenant, App: p.App}, false)
		if e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		send(w, rid, state)
	}))
	mux.Handle("POST /v1/execution-routing/config-requests", require("plans:write", func(w http.ResponseWriter, r *http.Request, p principal.Principal, rid string) {
		var in app.Change
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
	mux.Handle("GET /v1/execution-routing/config-requests/{id}", require("plans:read", func(w http.ResponseWriter, r *http.Request, p principal.Principal, rid string) {
		a, e := service.Workflow.Get(r.Context(), store.DB, foundation.Scope{Tenant: p.Tenant, App: p.App}, r.PathValue("id"), false)
		if e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		if a.Type != "EXECUTION_ROUTING" {
			httpapi.Error(w, rid, errors.New("NOT_FOUND"))
			return
		}
		change, e := app.DecodeChange(a.Payload)
		if e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		send(w, rid, map[string]any{"approval": a, "change": change})
	}))
	mux.Handle("GET /v1/execution-routing/records", require("plans:read", func(w http.ResponseWriter, r *http.Request, p principal.Principal, rid string) {
		rows, e := service.Repo.Records(r.Context(), store.DB, foundation.Scope{Tenant: p.Tenant, App: p.App})
		if e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		send(w, rid, map[string]any{"rows": rows})
	}))
	mux.Handle("POST /v1/execution-routing/preview", require("plans:read", func(w http.ResponseWriter, r *http.Request, p principal.Principal, rid string) {
		var in struct {
			Config model.Config `json:"config"`
			Source string       `json:"source_id"`
			Kind   string       `json:"event_kind"`
			At     time.Time    `json:"business_time"`
		}
		if e := httpapi.Decode(w, r, &in); e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		if in.At.IsZero() || (model.Claim{Source: in.Source, EventID: "preview", BusinessID: "preview", Kind: in.Kind, Engine: model.General}).Validate() != nil {
			httpapi.Error(w, rid, foundation.ErrValidation)
			return
		}
		if e := in.Config.Validate(); e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		rule, found := in.Config.Select(in.Source, in.Kind, in.At)
		var selected any
		if found {
			selected = rule
		}
		send(w, rid, map[string]any{"preview_only": true, "matched": found, "rule": selected})
	}))
	return nil
}
