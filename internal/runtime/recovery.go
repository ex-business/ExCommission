package runtime

import (
	"context"
	calcadapter "github.com/ex-business/ExCommission/internal/calculation/adapters"
	calculation "github.com/ex-business/ExCommission/internal/calculation/application"
	authhttp "github.com/ex-business/ExCommission/internal/identity/application"
	principal "github.com/ex-business/ExCommission/internal/identity/domain"
	ledger "github.com/ex-business/ExCommission/internal/ledger/adapters"
	"github.com/ex-business/ExCommission/internal/platform/adapters/httpapi"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	response "github.com/ex-business/ExCommission/internal/platform/application"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	workflowrepo "github.com/ex-business/ExCommission/internal/workflow/adapters"
	workflow "github.com/ex-business/ExCommission/internal/workflow/application"
	"net/http"
	"os"
)

func installRecovery(mux *http.ServeMux, require func(string, authhttp.AuthorizedHandler) http.Handler, store *mysql.Store, approvals *workflow.Service, tenant, app string) error {
	for _, v := range []struct {
		subject     string
		permissions []string
	}{{"local-operator", []string{"recoveries:request", "adjustments:request", "ledger:read"}}, {"local-approver", []string{"recoveries:approve", "adjustments:approve", "ledger:read"}}} {
		if err := bootstrapFeature(context.Background(), store, tenant, app, v.subject, "recovery-v1", v.permissions); err != nil {
			return err
		}
	}
	recovery := calculation.RecoveryService{Commands: store, Query: store.DB, Ledger: ledger.Repository{}, Workflow: workflowrepo.Repository{}, Evidence: calcadapter.SandboxEvidence{File: os.Getenv("LOCAL_RECOVERY_EVIDENCE_FILE")}, Now: clockUTC, ID: newID}
	adjustment := calculation.AdjustmentService{Commands: store, Tasks: taskRepository(store), Repo: calcadapter.Adjustments{}, Refunds: refundService(store), Ledger: ledger.Repository{}, Workflow: workflowrepo.Repository{}, Now: clockUTC, ID: newID}
	approvals.Executors["RECOVERY"] = recovery.Execute
	approvals.Executors["ADJUSTMENT"] = adjustment.Execute
	mux.Handle("POST /v1/recovery-requests", require("recoveries:request", func(w http.ResponseWriter, r *http.Request, p principal.Principal, id string) {
		var input calculation.RecoveryInput
		if err := httpapi.Decode(w, r, &input); err != nil {
			httpapi.Error(w, id, err)
			return
		}
		result, err := recovery.Request(r.Context(), p, input, r.Header.Get("Idempotency-Key"), id)
		if err != nil {
			httpapi.Error(w, id, err)
			return
		}
		httpapi.Write(w, result)
	}))
	mux.Handle("POST /v1/adjustment-requests", require("adjustments:request", func(w http.ResponseWriter, r *http.Request, p principal.Principal, id string) {
		var input calculation.AdjustmentInput
		if err := httpapi.Decode(w, r, &input); err != nil {
			httpapi.Error(w, id, err)
			return
		}
		result, err := adjustment.Request(r.Context(), p, input, r.Header.Get("Idempotency-Key"), id)
		if err != nil {
			httpapi.Error(w, id, err)
			return
		}
		httpapi.Write(w, result)
	}))
	for _, path := range []string{"GET /v1/debts/{id}", "GET /v1/entitlements/{id}"} {
		mux.Handle(path, require("ledger:read", func(w http.ResponseWriter, r *http.Request, p principal.Principal, id string) {
			v, err := (ledger.Repository{}).Get(r.Context(), store.DB, foundation.Scope{Tenant: p.Tenant, App: p.App}, r.PathValue("id"), false)
			if err != nil {
				httpapi.Error(w, id, err)
				return
			}
			result, _ := response.Result(200, v.ID, id, v)
			httpapi.Write(w, result)
		}))
	}
	mux.Handle("GET /v1/calculation-tasks/{id}/adjustments", require("tasks:read", func(w http.ResponseWriter, r *http.Request, p principal.Principal, id string) {
		scope := foundation.Scope{Tenant: p.Tenant, App: p.App}
		task, err := adjustment.Tasks.Get(r.Context(), store.DB, scope, r.PathValue("id"))
		if err != nil {
			httpapi.Error(w, id, err)
			return
		}
		snapshot, revision, err := adjustment.Repo.Latest(r.Context(), store.DB, scope, task)
		if err != nil {
			httpapi.Error(w, id, err)
			return
		}
		result, _ := response.Result(200, task.ID, id, map[string]any{"original_task_id": task.ID, "revision": revision, "effective_snapshot": snapshot, "original_snapshot_hash": task.SnapshotHash})
		httpapi.Write(w, result)
	}))
	return nil
}
