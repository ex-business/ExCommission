package runtime

import (
	"context"
	authhttp "github.com/ex-business/ExCommission/internal/identity/application"
	principal "github.com/ex-business/ExCommission/internal/identity/domain"
	"github.com/ex-business/ExCommission/internal/platform/adapters/httpapi"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	workflow "github.com/ex-business/ExCommission/internal/workflow/application"
	"net/http"
)

func installWorkflowRecovery(mux *http.ServeMux, require func(string, authhttp.AuthorizedHandler) http.Handler, store *mysql.Store, service *workflow.Service, tenant, app string) error {
	for _, v := range []struct {
		subject     string
		permissions []string
	}{{"local-operator", []string{"workflow:cancel"}}, {"local-approver", []string{"workflow:cancel", "workflow:recover"}}} {
		if err := bootstrapFeature(context.Background(), store, tenant, app, v.subject, "workflow-recovery-v1", v.permissions); err != nil {
			return err
		}
	}
	mux.Handle("POST /v1/approvals/{id}/execution-retries", require("workflow:recover", func(w http.ResponseWriter, r *http.Request, p principal.Principal, id string) {
		var input workflow.RecoveryInput
		if err := httpapi.Decode(w, r, &input); err != nil {
			httpapi.Error(w, id, err)
			return
		}
		result, err := service.RetryExecution(r.Context(), p, r.PathValue("id"), input, r.Header.Get("Idempotency-Key"), id)
		if err != nil {
			httpapi.Error(w, id, err)
			return
		}
		httpapi.Write(w, result)
	}))
	mux.Handle("POST /v1/approvals/{id}/cancellation", require("workflow:cancel", func(w http.ResponseWriter, r *http.Request, p principal.Principal, id string) {
		var input struct {
			Revision uint64 `json:"expected_revision"`
			Hash     string `json:"object_hash"`
			Reason   string `json:"reason"`
		}
		if err := httpapi.Decode(w, r, &input); err != nil {
			httpapi.Error(w, id, err)
			return
		}
		result, err := service.Cancel(r.Context(), p, r.PathValue("id"), workflow.Decision{Revision: input.Revision, ObjectHash: input.Hash, Reason: input.Reason}, r.Header.Get("Idempotency-Key"), id)
		if err != nil {
			httpapi.Error(w, id, err)
			return
		}
		httpapi.Write(w, result)
	}))
	return nil
}
