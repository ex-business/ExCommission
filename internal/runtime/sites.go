package runtime

import (
	"context"
	"errors"
	agents "github.com/ex-business/ExCommission/internal/agents/adapters"
	calculation "github.com/ex-business/ExCommission/internal/calculation/domain"
	identity "github.com/ex-business/ExCommission/internal/identity/application"
	principal "github.com/ex-business/ExCommission/internal/identity/domain"
	plans "github.com/ex-business/ExCommission/internal/plans/adapters"
	plan "github.com/ex-business/ExCommission/internal/plans/domain"
	planports "github.com/ex-business/ExCommission/internal/plans/ports"
	"github.com/ex-business/ExCommission/internal/platform/adapters/httpapi"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	response "github.com/ex-business/ExCommission/internal/platform/application"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	repo "github.com/ex-business/ExCommission/internal/sites/adapters"
	app "github.com/ex-business/ExCommission/internal/sites/application"
	model "github.com/ex-business/ExCommission/internal/sites/domain"
	workflowrepo "github.com/ex-business/ExCommission/internal/workflow/adapters"
	workflow "github.com/ex-business/ExCommission/internal/workflow/application"
	"net/http"
	"time"
)

func siteService(store *mysql.Store) app.Service {
	return app.Service{StepUp: func(ctx context.Context, p principal.Principal, key string, in any) error {
		return verifyStepUp(store, ctx, p, "site-config-request", key, in)
	}, Commands: store, Query: store.DB, Repo: repo.Repository{}, Plans: plans.Repository{}, Workflow: workflowrepo.Repository{}, Now: clockUTC, ID: newID}
}
func siteAllow(ctx context.Context, tx db.Executor, scope foundation.Scope) error {
	return (app.Service{Repo: repo.Repository{}}).Allow(ctx, tx, scope)
}

type sitePlansRepository struct{ planports.Repository }

func (sitePlansRepository) Select(ctx context.Context, tx db.Executor, scope foundation.Scope, route plan.Route, at time.Time) (plan.Version, error) {
	if err := siteAllow(ctx, tx, scope); err != nil {
		return plan.Version{}, err
	}
	c, err := (repo.Repository{}).At(ctx, tx, scope, at)
	if err != nil {
		return plan.Version{}, err
	}
	if c.Configured && !c.Enabled {
		return plan.Version{}, errors.New("SITE_AGENT_DISABLED")
	}
	if !c.Configured || route.EventType == "MILESTONE_REACHED" {
		return (plans.Repository{}).Select(ctx, tx, scope, route, at)
	}
	if !c.AllowsVenue(route.Product) {
		return plan.Version{}, errors.New("VENUE_NOT_PARTICIPATING")
	}
	version, err := (plans.Repository{}).AtPlan(ctx, tx, scope, c.PlanID, at)
	if err != nil {
		return version, err
	}
	if version.Config.Route != route {
		return version, errors.New("NO_EFFECTIVE_PLAN_VERSION")
	}
	return version, nil
}
func sitePlans() planports.Repository { return sitePlansRepository{Repository: plans.Repository{}} }

type sitePaymentAccounts struct{ agents.Repository }

func (sitePaymentAccounts) PaymentAllowed(ctx context.Context, tx db.Executor, scope foundation.Scope, agent string) error {
	if err := siteAllow(ctx, tx, scope); err != nil {
		return err
	}
	return (agents.Repository{}).PaymentAllowed(ctx, tx, scope, agent)
}
func siteCommitGuard(ctx context.Context, tx db.Executor, scope foundation.Scope, _ calculation.Task, _ calculation.Input) error {
	return siteAllow(ctx, tx, scope)
}
func installSites(ctx context.Context, mux *http.ServeMux, require func(string, identity.AuthorizedHandler) http.Handler, store *mysql.Store, approvals *workflow.Service, tenant, application string) error {
	if err := (repo.Repository{}).Ensure(ctx, store.DB, foundation.Scope{Tenant: tenant, App: application}); err != nil {
		return err
	}
	for _, v := range []struct {
		subject     string
		permissions []string
	}{{"local-operator", []string{"site:read", "site:request"}}, {"local-approver", []string{"site:read", "site:approve"}}} {
		if err := bootstrapFeature(ctx, store, tenant, application, v.subject, "sites-v1", v.permissions); err != nil {
			return err
		}
	}
	service := siteService(store)
	approvals.Executors["SITE_AGENT_CONFIG"] = service.Execute
	mux.Handle("GET /v1/site-agent-config", require("site:read", func(w http.ResponseWriter, r *http.Request, p principal.Principal, rid string) {
		c, err := (repo.Repository{}).Current(r.Context(), store.DB, foundation.Scope{Tenant: p.Tenant, App: p.App}, false)
		if err != nil {
			httpapi.Error(w, rid, err)
			return
		}
		result, _ := response.Result(200, "site-agent-config", rid, c)
		httpapi.Write(w, result)
	}))
	mux.Handle("POST /v1/site-agent-config-requests", require("site:request", func(w http.ResponseWriter, r *http.Request, p principal.Principal, rid string) {
		var in model.Change
		if err := httpapi.Decode(w, r, &in); err != nil {
			httpapi.Error(w, rid, err)
			return
		}
		result, err := service.Request(r.Context(), p, in, r.Header.Get("Idempotency-Key"), rid)
		if err != nil {
			httpapi.Error(w, rid, err)
			return
		}
		httpapi.Write(w, result)
	}))
	mux.Handle("GET /v1/site-agent-options", require("site:read", func(w http.ResponseWriter, r *http.Request, p principal.Principal, rid string) {
		rows, err := store.DB.QueryContext(r.Context(), `SELECT plan_id,version_id,product_code,event_type,scene FROM plan_versions WHERE tenant_id=? AND app_id=? AND effective_from<=? AND (effective_to IS NULL OR effective_to>?) ORDER BY plan_id,version_number`, p.Tenant, p.App, clockUTC(), clockUTC())
		if err != nil {
			httpapi.Error(w, rid, err)
			return
		}
		defer rows.Close()
		out := []map[string]string{}
		for rows.Next() {
			var id, version, venue, kind, scene string
			if err = rows.Scan(&id, &version, &venue, &kind, &scene); err != nil {
				httpapi.Error(w, rid, err)
				return
			}
			out = append(out, map[string]string{"plan_id": id, "version_id": version, "venue_id": venue, "event_type": kind, "scene": scene})
		}
		if err = rows.Err(); err != nil {
			httpapi.Error(w, rid, err)
			return
		}
		result, _ := response.Result(200, "site-agent-options", rid, map[string]any{"rows": out})
		httpapi.Write(w, result)
	}))
	return nil
}
