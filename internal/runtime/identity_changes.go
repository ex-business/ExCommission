package runtime

import (
	"context"
	identity "github.com/ex-business/ExCommission/internal/identity/adapters"
	authhttp "github.com/ex-business/ExCommission/internal/identity/application"
	principal "github.com/ex-business/ExCommission/internal/identity/domain"
	"github.com/ex-business/ExCommission/internal/platform/adapters/httpapi"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	response "github.com/ex-business/ExCommission/internal/platform/application"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	workflowrepo "github.com/ex-business/ExCommission/internal/workflow/adapters"
	workflow "github.com/ex-business/ExCommission/internal/workflow/application"
	"net/http"
	"strings"
)

func installIdentityChanges(mux *http.ServeMux, require func(string, authhttp.AuthorizedHandler) http.Handler, store *mysql.Store, approvals *workflow.Service, tenant, app string) error {
	for _, v := range []struct {
		subject     string
		permissions []string
	}{{"local-operator", []string{"identity:request", "identity:read"}}, {"local-approver", []string{"identity:approve", "identity:read"}}} {
		if err := bootstrapFeature(context.Background(), store, tenant, app, v.subject, "identity-changes-v1", v.permissions); err != nil {
			return err
		}
	}
	allowed := map[string]bool{}
	for _, permission := range strings.Fields("users:sync users:read agents:read agents:write agents:status:request agents:status:approve relations:request relations:approve workflow:read workflow:decide workflow:recover workflow:cancel plans:read plans:write plans:simulate plans:approve grades:request grades:evaluate events:write events:read tasks:read tasks:recover statements:write statements:read statements:approve payments:write payments:read payments:verify payments:reconcile refunds:write refunds:read recoveries:request recoveries:approve adjustments:request adjustments:approve ledger:read risk:write risk:read risk:release:request risk:approve audit:read security:handle identity:read identity:request identity:approve reports:read reports:export reports:download platform:read platform:request platform:approve milestones:request milestones:approve milestones:write milestones:read member:read member:claim agents:directory agents:sensitive:read users:statistics site:read site:request site:approve member:refer affiliate:read affiliate:configure affiliate:approve") {
		allowed[permission] = true
	}
	service := authhttp.GrantService{Commands: store, Workflow: workflowrepo.Repository{}, Allowed: allowed, Now: clockUTC, ID: newID, Apply: identity.ChangeGrants}
	approvals.Executors["IDENTITY_GRANT"] = service.Execute
	mux.Handle("POST /v1/permission-change-requests", require("identity:request", func(w http.ResponseWriter, r *http.Request, p principal.Principal, id string) {
		var input authhttp.GrantChange
		if err := httpapi.Decode(w, r, &input); err != nil {
			httpapi.Error(w, id, err)
			return
		}
		result, err := service.Request(r.Context(), p, input, r.Header.Get("Idempotency-Key"), id)
		if err != nil {
			httpapi.Error(w, id, err)
			return
		}
		httpapi.Write(w, result)
	}))
	mux.Handle("GET /v1/identity/subjects/{id}/permissions", require("identity:read", func(w http.ResponseWriter, r *http.Request, p principal.Principal, id string) {
		permissions, revision, err := identity.SubjectGrants(r.Context(), store.DB, foundation.Scope{Tenant: p.Tenant, App: p.App}, r.PathValue("id"))
		if err != nil {
			httpapi.Error(w, id, err)
			return
		}
		result, _ := response.Result(200, r.PathValue("id"), id, map[string]any{"subject_id": r.PathValue("id"), "permissions": permissions, "revision": revision})
		httpapi.Write(w, result)
	}))
	return nil
}
