package runtime

import (
	"context"
	flowrepo "github.com/ex-business/ExCommission/internal/affiliateflow/adapters"
	flow "github.com/ex-business/ExCommission/internal/affiliateflow/application"
	tasks "github.com/ex-business/ExCommission/internal/affiliatetasks"
	identity "github.com/ex-business/ExCommission/internal/identity/adapters"
	auth "github.com/ex-business/ExCommission/internal/identity/application"
	principal "github.com/ex-business/ExCommission/internal/identity/domain"
	"github.com/ex-business/ExCommission/internal/platform/adapters/httpapi"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	response "github.com/ex-business/ExCommission/internal/platform/application"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	approvalrepo "github.com/ex-business/ExCommission/internal/workflow/adapters"
	workflow "github.com/ex-business/ExCommission/internal/workflow/application"
	"net/http"
)

func affiliateTaskManager(store *mysql.Store) tasks.Service {
	return tasks.Service{Repo: tasks.Repository{}, Query: store.DB, UOW: store, Commands: store, Workflow: approvalrepo.Repository{}, Now: clockUTC, ID: newID, Hash: approvalrepo.Hash, LockScope: (flowrepo.Repository{}).Lock, Authorize: func(ctx context.Context, tx db.Executor, p principal.Principal, permission string) error {
		return identity.Authorize(ctx, tx, p, p.Tenant, p.App, permission)
	}, Resolve: func(ctx context.Context, sc foundation.Scope, key tasks.Key) (tasks.Inspector, tasks.Executor, error) {
		service := affiliateFlowService(store)
		if key.Program == "legacy" {
			return func(c context.Context) (tasks.Report, error) { return service.InspectLegacyTask(c, sc, key) }, func(c context.Context) error { return service.ExecuteLegacyTask(c, sc, key) }, nil
		}
		programmes := programService(store, service)
		versions, e := programmes.Repo.Versions(ctx, store.DB, sc)
		if e != nil {
			return nil, nil, e
		}
		for _, v := range versions {
			if v.ID == key.Program && v.Revision == key.Revision {
				return func(c context.Context) (tasks.Report, error) { return programmes.InspectTaskDay(c, sc, v, key.Day) }, func(c context.Context) error { return programmes.ExecuteTaskDay(c, sc, v, key.Day) }, nil
			}
		}
		return nil, nil, tasks.ErrMissing
	}}
}
func configureAffiliateTaskRuns(store *mysql.Store, base flow.Service) flow.Service {
	base.TaskRun = affiliateTaskManager(store).Run
	return base
}
func runAffiliateTaskMaintenance(ctx context.Context, store *mysql.Store) error {
	manager := affiliateTaskManager(store)
	sc := localScope()
	existing, e := manager.Repo.Existing(ctx, store.DB, sc)
	if e != nil {
		return e
	}
	for _, record := range existing {
		inspect, execute, e := manager.Resolve(ctx, sc, record.Key)
		if e != nil {
			return e
		}
		if e = manager.Run(ctx, sc, record.Key, inspect, execute); e != nil {
			return e
		}
	}
	service := affiliateFlowService(store)
	if e = service.RefreshAll(ctx, sc); e != nil {
		return e
	}
	return service.RunOnce(ctx, sc)
}
func installAffiliateTaskRuns(mux *http.ServeMux, require func(string, auth.AuthorizedHandler) http.Handler, store *mysql.Store, approvals *workflow.Service) {
	manager := affiliateTaskManager(store)
	approvals.Executors["AFFILIATE_TASK_ALERT"] = manager.ExecuteRules
	write := func(w http.ResponseWriter, rid, id string, data any, e error) {
		if e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		out, _ := response.Result(200, id, rid, data)
		httpapi.Write(w, out)
	}
	mux.Handle("GET /v1/affiliate/task-runs", require("tasks:read", func(w http.ResponseWriter, r *http.Request, p principal.Principal, rid string) {
		sc := foundation.Scope{Tenant: p.Tenant, App: p.App}
		rows, e := manager.Repo.List(r.Context(), store.DB, sc, r.URL.Query().Get("status"), r.URL.Query().Get("program_id"))
		write(w, rid, "affiliate-tasks", map[string]any{"rows": rows, "limit": 200}, e)
	}))
	mux.Handle("GET /v1/affiliate/task-runs/{id}", require("tasks:read", func(w http.ResponseWriter, r *http.Request, p principal.Principal, rid string) {
		sc := foundation.Scope{Tenant: p.Tenant, App: p.App}
		record, e := manager.Repo.Get(r.Context(), store.DB, sc, r.PathValue("id"), false)
		if e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		attempts, e := manager.Repo.Attempts(r.Context(), store.DB, sc, record.ID)
		write(w, rid, record.ID, map[string]any{"task": record, "attempts": attempts}, e)
	}))
	mux.Handle("POST /v1/affiliate/task-runs/{id}/retry-requests", require("tasks:recover", func(w http.ResponseWriter, r *http.Request, p principal.Principal, rid string) {
		var in struct {
			Reason string `json:"reason"`
		}
		if e := httpapi.Decode(w, r, &in); e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		out, e := manager.Retry(r.Context(), p, r.PathValue("id"), in.Reason, r.Header.Get("Idempotency-Key"), rid)
		if e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		httpapi.Write(w, out)
	}))
	mux.Handle("GET /v1/affiliate/task-alerts", require("tasks:read", func(w http.ResponseWriter, r *http.Request, p principal.Principal, rid string) {
		sc := foundation.Scope{Tenant: p.Tenant, App: p.App}
		alerts, e := manager.Repo.Alerts(r.Context(), store.DB, sc)
		if e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		rules, e := manager.Repo.Rules(r.Context(), store.DB, sc)
		write(w, rid, "affiliate-task-alerts", map[string]any{"events": alerts, "configuration": rules}, e)
	}))
	mux.Handle("POST /v1/affiliate/task-alert-config-requests", require("affiliate:configure", func(w http.ResponseWriter, r *http.Request, p principal.Principal, rid string) {
		var in tasks.RuleChange
		if e := httpapi.Decode(w, r, &in); e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		out, e := manager.RequestRules(r.Context(), p, in, r.Header.Get("Idempotency-Key"), rid)
		if e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		httpapi.Write(w, out)
	}))
}
