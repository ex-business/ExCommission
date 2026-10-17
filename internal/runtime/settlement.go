package runtime

import (
	"context"
	"database/sql"
	"errors"
	authhttp "github.com/ex-business/ExCommission/internal/identity/application"
	principal "github.com/ex-business/ExCommission/internal/identity/domain"
	ledger "github.com/ex-business/ExCommission/internal/ledger/adapters"
	catalog "github.com/ex-business/ExCommission/internal/platform/adapters/catalog"
	"github.com/ex-business/ExCommission/internal/platform/adapters/httpapi"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	response "github.com/ex-business/ExCommission/internal/platform/application"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	repo "github.com/ex-business/ExCommission/internal/settlement/adapters"
	settlement "github.com/ex-business/ExCommission/internal/settlement/application"
	model "github.com/ex-business/ExCommission/internal/settlement/domain"
	workflowrepo "github.com/ex-business/ExCommission/internal/workflow/adapters"
	workflow "github.com/ex-business/ExCommission/internal/workflow/application"
	"net/http"
)

func settlementService(store *mysql.Store) settlement.Service {
	return settlement.Service{Calendar: catalog.Repository{}, Commands: store, Query: store.DB, Repo: repo.Repository{}, Ledger: ledger.Repository{}, Workflow: workflowrepo.Repository{}, Now: clockUTC, ID: newID}
}
func scheduledSettlement(store *mysql.Store) settlement.Scheduler {
	return settlement.Scheduler{UOW: store, Service: settlementService(store), Repo: repo.Repository{}, History: workflowrepo.Repository{}, Calendars: catalog.Repository{}, Now: clockUTC, ID: newID, Audit: func(ctx context.Context, tx db.Executor, s foundation.Scope, action, ref, code string) error {
		if store.Audit == nil {
			return errors.New("audit unavailable")
		}
		return store.Audit(ctx, tx, s, action, ref, code)
	}}
}
func installSettlement(mux *http.ServeMux, require func(string, authhttp.AuthorizedHandler) http.Handler, store *mysql.Store, approvals *workflow.Service, tenant, app string) error {
	for _, v := range []struct {
		subject     string
		permissions []string
	}{{"local-operator", []string{"statements:write", "statements:read"}}, {"local-approver", []string{"statements:read", "statements:approve"}}} {
		if err := bootstrapFeature(context.Background(), store, tenant, app, v.subject, "settlement-v1", v.permissions); err != nil {
			return err
		}
	}
	service := settlementService(store)
	approvals.Executors["STATEMENT"] = service.Execute
	if approvals.Rejections == nil {
		approvals.Rejections = map[string]workflow.Executor{}
	}
	approvals.Rejections["STATEMENT"] = service.Reject
	if approvals.Failures == nil {
		approvals.Failures = map[string]workflow.Executor{}
	}
	approvals.Failures["STATEMENT"] = service.Reject
	mux.Handle("POST /v1/statement-runs", require("statements:write", func(w http.ResponseWriter, r *http.Request, p principal.Principal, id string) {
		var input settlement.RunInput
		if err := httpapi.Decode(w, r, &input); err != nil {
			httpapi.Error(w, id, err)
			return
		}
		result, err := service.Run(r.Context(), p, input, r.Header.Get("Idempotency-Key"), id)
		if err != nil {
			httpapi.Error(w, id, err)
			return
		}
		httpapi.Write(w, result)
	}))
	mux.Handle("POST /v1/statements/{id}/submission", require("statements:write", func(w http.ResponseWriter, r *http.Request, p principal.Principal, id string) {
		var input settlement.Submission
		if err := httpapi.Decode(w, r, &input); err != nil {
			httpapi.Error(w, id, err)
			return
		}
		result, err := service.Submit(r.Context(), p, r.PathValue("id"), input, r.Header.Get("Idempotency-Key"), id)
		if err != nil {
			httpapi.Error(w, id, err)
			return
		}
		httpapi.Write(w, result)
	}))
	mux.Handle("GET /v1/statements/{id}", require("statements:read", func(w http.ResponseWriter, r *http.Request, p principal.Principal, id string) {
		v, err := service.Get(r.Context(), foundation.Scope{Tenant: p.Tenant, App: p.App}, r.PathValue("id"))
		if err != nil {
			httpapi.Error(w, id, err)
			return
		}
		result, _ := response.Result(200, v.ID, id, v)
		httpapi.Write(w, result)
	}))
	mux.Handle("GET /v1/settlement-schedule", require("statements:read", func(w http.ResponseWriter, r *http.Request, p principal.Principal, rid string) {
		state, err := (repo.Repository{}).Schedule(r.Context(), store.DB, foundation.Scope{Tenant: p.Tenant, App: p.App})
		initialized := err == nil
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			httpapi.Error(w, rid, err)
			return
		}
		var period *string
		var due any
		if initialized {
			period = &state.NextPeriod
			at, e := model.DueAt(state.NextPeriod)
			if e != nil {
				httpapi.Error(w, rid, e)
				return
			}
			due = at
		}
		result, _ := response.Result(200, "daily-settlement", rid, map[string]any{"timezone": "UTC", "hour": 1, "poll_interval_seconds": 5, "initialized": initialized, "next_period": period, "next_due_at": due, "last_result": state.Last})
		httpapi.Write(w, result)
	}))
	return nil
}
