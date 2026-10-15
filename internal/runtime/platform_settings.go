package runtime

import (
	"context"
	calculation "github.com/ex-business/ExCommission/internal/calculation/domain"
	authhttp "github.com/ex-business/ExCommission/internal/identity/application"
	principal "github.com/ex-business/ExCommission/internal/identity/domain"
	planrepo "github.com/ex-business/ExCommission/internal/plans/adapters"
	plan "github.com/ex-business/ExCommission/internal/plans/domain"
	catalog "github.com/ex-business/ExCommission/internal/platform/adapters/catalog"
	"github.com/ex-business/ExCommission/internal/platform/adapters/httpapi"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	application "github.com/ex-business/ExCommission/internal/platform/application"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	workflowrepo "github.com/ex-business/ExCommission/internal/workflow/adapters"
	workflow "github.com/ex-business/ExCommission/internal/workflow/application"
	"net/http"
	"time"
)

type catalogValidator struct {
	Query db.Executor
	Scope foundation.Scope
}

func (v catalogValidator) Decode(raw []byte) (plan.Config, []byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	calendars, err := (catalog.Repository{}).CalendarVersions(ctx, v.Query, v.Scope)
	if err != nil {
		return plan.Config{}, nil, err
	}
	validator, err := planrepo.NewValidator(foundation.CurrencyExponents(), calendars)
	if err != nil {
		return plan.Config{}, nil, err
	}
	return validator.Decode(raw)
}
func installPlatform(mux *http.ServeMux, require func(string, authhttp.AuthorizedHandler) http.Handler, store *mysql.Store, approvals *workflow.Service, tenant, app string) error {
	scope := foundation.Scope{Tenant: tenant, App: app}
	if err := store.Within(context.Background(), func(tx db.Executor) error { return (catalog.Repository{}).Bootstrap(context.Background(), tx, scope) }); err != nil {
		return err
	}
	for _, v := range []struct {
		subject     string
		permissions []string
	}{{"local-operator", []string{"platform:read", "platform:request"}}, {"local-approver", []string{"platform:read", "platform:approve"}}} {
		if err := bootstrapFeature(context.Background(), store, tenant, app, v.subject, "platform-settings-v1", v.permissions); err != nil {
			return err
		}
	}
	service := application.SettingsService{Commands: store, Repo: catalog.Repository{}, Workflow: workflowrepo.Repository{}, Now: clockUTC, ID: newID}
	approvals.Executors["PLATFORM_SETTING"] = service.Execute
	mux.Handle("POST /v1/platform-setting-requests", require("platform:request", func(w http.ResponseWriter, r *http.Request, p principal.Principal, id string) {
		var input application.SettingInput
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
	mux.Handle("GET /v1/platform/catalogs", require("platform:read", func(w http.ResponseWriter, r *http.Request, p principal.Principal, id string) {
		versions, err := (catalog.Repository{}).Versions(r.Context(), store.DB, foundation.Scope{Tenant: p.Tenant, App: p.App})
		if err != nil {
			httpapi.Error(w, id, err)
			return
		}
		result, _ := application.Result(200, "catalogs", id, map[string]any{"versions": versions})
		httpapi.Write(w, result)
	}))
	mux.Handle("GET /v1/platform/components", require("platform:read", func(w http.ResponseWriter, r *http.Request, p principal.Principal, id string) {
		scope := foundation.Scope{Tenant: p.Tenant, App: p.App}
		components := []map[string]any{}
		for _, v := range []struct {
			kind, id, credential string
			capabilities         []string
		}{{"SOURCE", "sandbox", "local://source", []string{"SIGNED_EVENTS", "SIGNED_REFUNDS"}}, {"PAYMENT", "sandbox", "local://payment", []string{"SUBMIT_IDEMPOTENT", "QUERY_ORIGINAL_ID", "VERIFY_CALLBACK", "RECONCILE"}}, {"NOTIFICATION", "local-inbox", "", []string{"IDEMPOTENT_SEND"}}} {
			enabled, revision, err := (catalog.Repository{}).Component(r.Context(), store.DB, scope, v.kind, v.id)
			if err != nil {
				httpapi.Error(w, id, err)
				return
			}
			components = append(components, map[string]any{"component_type": v.kind, "component_id": v.id, "implementation_version": "1", "contract_version": "1", "capabilities": v.capabilities, "config_schema_ref": "compiled://" + v.kind + "/1", "credential_ref": v.credential, "enabled": enabled, "validation_status": "VALID", "revision": revision})
		}
		for algorithm, version := range calculation.Capabilities() {
			components = append(components, map[string]any{"component_type": "ALGORITHM", "component_id": algorithm, "implementation_version": version, "contract_version": "1", "capabilities": []string{"CALCULATE", "REFUND_TARGETS"}, "config_schema_ref": "contracts/v1/plan.schema.json", "credential_ref": nil, "enabled": true, "validation_status": "VALID", "revision": 0})
		}
		components = append(components, map[string]any{"component_type": "IDENTITY", "component_id": "local-static", "implementation_version": "1", "contract_version": "1", "capabilities": []string{"TRUSTED_LOCAL_PRINCIPAL", "SCOPE_AUTHORIZATION"}, "credential_ref": "local://identity", "config_schema_ref": "compiled://IDENTITY/1", "enabled": true, "validation_status": "SANDBOX_ONLY"})
		components = append(components, map[string]any{"component_type": "AUDIT", "component_id": "local-mysql", "implementation_version": "1", "contract_version": "1", "capabilities": []string{"IDEMPOTENT_DELIVERY", "REDACTED_SUMMARY"}, "credential_ref": "local://audit", "config_schema_ref": "compiled://AUDIT/1", "enabled": true, "validation_status": "VALID"})
		result, _ := application.Result(200, "components", id, map[string]any{"components": components})
		httpapi.Write(w, result)
	}))
	mux.Handle("GET /v1/notifications", require("platform:read", func(w http.ResponseWriter, r *http.Request, p principal.Principal, id string) {
		rows, err := (catalog.Repository{}).Notifications(r.Context(), store.DB, foundation.Scope{Tenant: p.Tenant, App: p.App})
		if err != nil {
			httpapi.Error(w, id, err)
			return
		}
		result, _ := application.Result(200, "notifications", id, map[string]any{"rows": rows, "as_of": clockUTC()})
		httpapi.Write(w, result)
	}))
	return nil
}

func (v catalogValidator) ValidateSimulation(raw []byte) error {
	validator, err := planrepo.NewValidator(foundation.CurrencyExponents(), map[string]bool{"UTC-v1": true})
	if err != nil {
		return err
	}
	return validator.ValidateSimulation(raw)
}
