package runtime

import (
	"context"
	"encoding/json"
	agents "github.com/ex-business/ExCommission/internal/agents/adapters"
	tasks "github.com/ex-business/ExCommission/internal/calculation/adapters"
	calculation "github.com/ex-business/ExCommission/internal/calculation/application"
	model "github.com/ex-business/ExCommission/internal/calculation/domain"
	authhttp "github.com/ex-business/ExCommission/internal/identity/application"
	principal "github.com/ex-business/ExCommission/internal/identity/domain"
	source "github.com/ex-business/ExCommission/internal/integration/adapters"
	integration "github.com/ex-business/ExCommission/internal/integration/application"
	ledger "github.com/ex-business/ExCommission/internal/ledger/adapters"
	plans "github.com/ex-business/ExCommission/internal/plans/adapters"
	"github.com/ex-business/ExCommission/internal/platform/adapters/httpapi"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	response "github.com/ex-business/ExCommission/internal/platform/application"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	users "github.com/ex-business/ExCommission/internal/users/adapters"
	"io"
	"net/http"
	"os"
	"time"
)

func clockUTC() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }
func taskRepository(store *mysql.Store) tasks.Tasks {
	return tasks.Tasks{DB: store.DB, Within: store.Within}
}
func buildWorker(store *mysql.Store) *calculation.Worker {
	return &calculation.Worker{UOW: store, Tasks: taskRepository(store), Resolver: calculation.Resolver{Plans: plans.Repository{}, Users: users.Repository{}, Accounts: agents.Repository{}, Relationships: agents.Repository{}, Grades: agents.Repository{}, Contracts: agents.Repository{}, Exponents: map[string]int{"USD": 2, "BTC": 8, "POINT": 0}}, Committer: ledger.Repository{}, TargetPolicy: refundService(store), Now: clockUTC, Lease: 30 * time.Second}
}
func localScope() foundation.Scope {
	tenant, app := os.Getenv("LOCAL_TENANT"), os.Getenv("LOCAL_APP")
	if tenant == "" {
		tenant = "local-sandbox"
	}
	if app == "" {
		app = "commission"
	}
	return foundation.Scope{Tenant: tenant, App: app}
}
func taskView(task model.Task) map[string]any {
	result := model.Result{TaskID: task.ID, SnapshotID: "snapshot_" + task.ID, Status: task.Status, Items: []model.Item{}, Reasons: []string{}, Trace: []model.Trace{}}
	if len(task.Result) > 0 {
		var stored model.Result
		if json.Unmarshal(task.Result, &stored) == nil {
			result.Trace = stored.Trace
			if task.Status == "SUCCEEDED" || task.Status == "NO_MATCH" {
				result.Items = stored.Items
			}
			result.Reasons = stored.Reasons
		}
	}
	if task.Reason != nil {
		result.Reasons = []string{*task.Reason}
	}
	return map[string]any{"task_id": task.ID, "snapshot_id": result.SnapshotID, "status": task.Status, "items": result.Items, "reason_codes": result.Reasons, "reason_code": task.Reason, "trace": result.Trace, "revision": task.Revision, "retry_count": task.Retries, "snapshot_hash": task.SnapshotHash, "recovery_url": "/v1/calculation-tasks/" + task.ID + "/resume"}
}
func installEvents(mux *http.ServeMux, require func(string, authhttp.AuthorizedHandler) http.Handler, store *mysql.Store, tenant, app string) error {
	if err := bootstrapFeature(context.Background(), store, tenant, app, "local-operator", "events-v1", []string{"events:write", "events:read", "tasks:read", "tasks:recover"}); err != nil {
		return err
	}
	key := []byte(os.Getenv("LOCAL_SOURCE_KEY"))
	if len(key) < 32 {
		return foundation.ErrValidation
	}
	provider, err := source.NewSource("sandbox", key)
	if err != nil {
		return err
	}
	repository := taskRepository(store)
	service := integration.Service{Commands: store, Query: store.DB, Repo: source.Repository{}, Tasks: repository, Source: provider, Plans: plans.Repository{}, Now: clockUTC, ID: newID}
	mux.Handle("POST /v1/events", require("events:write", func(w http.ResponseWriter, r *http.Request, p principal.Principal, id string) {
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if err != nil {
			httpapi.Error(w, id, foundation.ErrValidation)
			return
		}
		result, err := service.Receive(r.Context(), p, raw, r.Header.Get("X-Source-Signature"), r.Header.Get("Idempotency-Key"), id)
		if err != nil {
			httpapi.Error(w, id, err)
			return
		}
		httpapi.Write(w, result)
	}))
	mux.Handle("GET /v1/events/{id}", require("events:read", func(w http.ResponseWriter, r *http.Request, p principal.Principal, id string) {
		task, err := repository.ByEvent(r.Context(), store.DB, foundation.Scope{Tenant: p.Tenant, App: p.App}, r.PathValue("id"))
		if err != nil {
			httpapi.Error(w, id, err)
			return
		}
		result, _ := response.Result(200, task.EventID, id, map[string]any{"event_id": task.EventID, "task_id": task.ID, "source_id": task.Source, "source_event_id": task.SourceEvent, "acceptance_status": "ACCEPTED", "task_status": task.Status, "reason_code": task.Reason})
		httpapi.Write(w, result)
	}))
	for _, path := range []string{"GET /v1/calculation-tasks/{id}", "GET /v1/calculation-tasks/{id}/trace"} {
		mux.Handle(path, require("tasks:read", func(w http.ResponseWriter, r *http.Request, p principal.Principal, id string) {
			task, err := repository.Get(r.Context(), store.DB, foundation.Scope{Tenant: p.Tenant, App: p.App}, r.PathValue("id"))
			if err != nil {
				httpapi.Error(w, id, err)
				return
			}
			result, _ := response.Result(200, task.ID, id, taskView(task))
			httpapi.Write(w, result)
		}))
	}
	mux.Handle("POST /v1/calculation-tasks/{id}/resume", require("tasks:recover", func(w http.ResponseWriter, r *http.Request, p principal.Principal, id string) {
		var input struct {
			Revision uint64 `json:"expected_revision"`
			Evidence string `json:"evidence_ref"`
			Reason   string `json:"reason"`
		}
		if err := httpapi.Decode(w, r, &input); err != nil {
			httpapi.Error(w, id, err)
			return
		}
		if !foundation.ValidID(input.Evidence) || !foundation.ValidID(input.Reason) {
			httpapi.Error(w, id, foundation.ErrValidation)
			return
		}
		ctx := foundation.WithRequest(r.Context(), id, p.Subject)
		scope := foundation.Scope{Tenant: p.Tenant, App: p.App}
		payload := struct {
			TaskID   string `json:"task_id"`
			Revision uint64 `json:"expected_revision"`
			Evidence string `json:"evidence_ref"`
			Reason   string `json:"reason"`
		}{r.PathValue("id"), input.Revision, input.Evidence, input.Reason}
		result, err := store.Execute(ctx, scope, "task-resume", r.Header.Get("Idempotency-Key"), payload, func(tx db.Executor) (foundation.CommandResult, error) {
			if err := repository.Resume(ctx, tx, scope, payload.TaskID, input.Revision, input.Evidence, clockUTC()); err != nil {
				return foundation.CommandResult{}, err
			}
			return response.Result(202, payload.TaskID, id, map[string]string{"task_id": payload.TaskID, "status": "QUEUED"})
		})
		if err != nil {
			httpapi.Error(w, id, err)
			return
		}
		httpapi.Write(w, result)
	}))
	return nil
}
