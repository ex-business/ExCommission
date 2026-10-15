package runtime

import (
	"context"
	"encoding/json"
	agents "github.com/ex-business/ExCommission/internal/agents/adapters"
	audit "github.com/ex-business/ExCommission/internal/audit/adapters"
	authhttp "github.com/ex-business/ExCommission/internal/identity/application"
	principal "github.com/ex-business/ExCommission/internal/identity/domain"
	ledger "github.com/ex-business/ExCommission/internal/ledger/adapters"
	"github.com/ex-business/ExCommission/internal/platform/adapters/httpapi"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	response "github.com/ex-business/ExCommission/internal/platform/application"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	repo "github.com/ex-business/ExCommission/internal/risk/adapters"
	risk "github.com/ex-business/ExCommission/internal/risk/application"
	hash "github.com/ex-business/ExCommission/internal/workflow/adapters"
	workflow "github.com/ex-business/ExCommission/internal/workflow/application"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

func installRiskAudit(mux *http.ServeMux, require func(string, authhttp.AuthorizedHandler) http.Handler, store *mysql.Store, approvals *workflow.Service, tenant, app string) error {
	for _, v := range []struct {
		subject     string
		permissions []string
	}{{"local-operator", []string{"risk:write", "risk:read", "risk:release:request", "agents:status:request", "audit:read", "security:handle"}}, {"local-approver", []string{"risk:read", "risk:approve", "agents:status:approve", "audit:read"}}} {
		if err := bootstrapFeature(context.Background(), store, tenant, app, v.subject, "risk-audit-v1", v.permissions); err != nil {
			return err
		}
	}
	service := risk.Service{Commands: store, Query: store.DB, Repo: repo.Repository{}, Ledger: ledger.Repository{}, Accounts: agents.Repository{}, Workflow: hash.Repository{}, Now: clockUTC, ID: newID}
	approvals.Executors["RISK_RELEASE"] = service.ExecuteRelease
	approvals.Executors["AGENT_STATUS"] = service.ExecuteStatus
	if approvals.Rejections == nil {
		approvals.Rejections = map[string]workflow.Executor{}
	}
	approvals.Rejections["RISK_RELEASE"] = service.RejectRelease
	if approvals.Failures == nil {
		approvals.Failures = map[string]workflow.Executor{}
	}
	approvals.Failures["RISK_RELEASE"] = service.RejectRelease
	mux.Handle("POST /v1/risk-holds", require("risk:write", func(w http.ResponseWriter, r *http.Request, p principal.Principal, id string) {
		var input risk.HoldInput
		if err := httpapi.Decode(w, r, &input); err != nil {
			httpapi.Error(w, id, err)
			return
		}
		result, err := service.Hold(r.Context(), p, input, r.Header.Get("Idempotency-Key"), id)
		if err != nil {
			httpapi.Error(w, id, err)
			return
		}
		httpapi.Write(w, result)
	}))
	mux.Handle("POST /v1/risk-holds/{id}/release-requests", require("risk:release:request", func(w http.ResponseWriter, r *http.Request, p principal.Principal, id string) {
		var input risk.ReleaseInput
		if err := httpapi.Decode(w, r, &input); err != nil {
			httpapi.Error(w, id, err)
			return
		}
		result, err := service.ReleaseRequest(r.Context(), p, r.PathValue("id"), input, r.Header.Get("Idempotency-Key"), id)
		if err != nil {
			httpapi.Error(w, id, err)
			return
		}
		httpapi.Write(w, result)
	}))
	mux.Handle("POST /v1/risk-holds/{id}/appeals", require("risk:write", func(w http.ResponseWriter, r *http.Request, p principal.Principal, id string) {
		var input struct {
			Reason   string `json:"reason"`
			Evidence string `json:"evidence_ref"`
		}
		if err := httpapi.Decode(w, r, &input); err != nil {
			httpapi.Error(w, id, err)
			return
		}
		result, err := service.Appeal(r.Context(), p, r.PathValue("id"), input.Reason, input.Evidence, r.Header.Get("Idempotency-Key"), id)
		if err != nil {
			httpapi.Error(w, id, err)
			return
		}
		httpapi.Write(w, result)
	}))
	mux.Handle("GET /v1/risk-holds/{id}", require("risk:read", func(w http.ResponseWriter, r *http.Request, p principal.Principal, id string) {
		scope := foundation.Scope{Tenant: p.Tenant, App: p.App}
		rows, err := service.Ledger.Restrictions(r.Context(), store.DB, scope, r.PathValue("id"))
		if err != nil {
			httpapi.Error(w, id, err)
			return
		}
		if len(rows) == 0 {
			http.NotFound(w, r)
			return
		}
		v, err := service.Repo.Get(r.Context(), store.DB, scope, r.PathValue("id"), false)
		if err != nil && err.Error() != "NOT_FOUND" {
			httpapi.Error(w, id, err)
			return
		}
		result, _ := response.Result(200, r.PathValue("id"), id, map[string]any{"risk_id": r.PathValue("id"), "revision": v.Revision, "status": v.Status, "restrictions": rows})
		httpapi.Write(w, result)
	}))
	mux.Handle("POST /v1/agent-status-change-requests", require("agents:status:request", func(w http.ResponseWriter, r *http.Request, p principal.Principal, id string) {
		var input risk.StatusInput
		if err := httpapi.Decode(w, r, &input); err != nil {
			httpapi.Error(w, id, err)
			return
		}
		result, err := service.StatusRequest(r.Context(), p, input, r.Header.Get("Idempotency-Key"), id)
		if err != nil {
			httpapi.Error(w, id, err)
			return
		}
		httpapi.Write(w, result)
	}))
	mux.Handle("GET /v1/agents/{id}", require("agents:read", func(w http.ResponseWriter, r *http.Request, p principal.Principal, id string) {
		scope := foundation.Scope{Tenant: p.Tenant, App: p.App}
		v, err := (agents.Repository{}).AccountAt(r.Context(), store.DB, scope, r.PathValue("id"), clockUTC())
		if err != nil {
			httpapi.Error(w, id, err)
			return
		}
		revision, err := service.Accounts.Revision(r.Context(), store.DB, scope, v.ID)
		if err != nil {
			httpapi.Error(w, id, err)
			return
		}
		result, _ := response.Result(200, v.ID, id, map[string]any{"agent_id": v.ID, "user_id": v.UserID, "status": v.Status, "revision": revision})
		httpapi.Write(w, result)
	}))
	mux.Handle("GET /v1/audit-logs", require("audit:read", func(w http.ResponseWriter, r *http.Request, p principal.Principal, id string) {
		scope := foundation.Scope{Tenant: p.Tenant, App: p.App}
		f := audit.Filter{Type: r.URL.Query().Get("type"), Actor: r.URL.Query().Get("actor"), Object: r.URL.Query().Get("object_ref"), To: clockUTC(), Limit: 100}
		if value := r.URL.Query().Get("as_of"); value != "" {
			at, err := time.Parse(time.RFC3339Nano, value)
			if err != nil {
				httpapi.Error(w, id, foundation.ErrValidation)
				return
			}
			if at.After(f.To) {
				httpapi.Error(w, id, foundation.ErrValidation)
				return
			}
			f.To = at
		}
		if value := r.URL.Query().Get("limit"); value != "" {
			n, err := strconv.Atoi(value)
			if err != nil || n < 1 || n > 100 {
				httpapi.Error(w, id, foundation.ErrValidation)
				return
			}
			f.Limit = n
		}
		if value := r.URL.Query().Get("time_range"); value != "" {
			parts := strings.Split(value, ",")
			if len(parts) != 2 {
				httpapi.Error(w, id, foundation.ErrValidation)
				return
			}
			from, e1 := time.Parse(time.RFC3339Nano, parts[0])
			to, e2 := time.Parse(time.RFC3339Nano, parts[1])
			if e1 != nil || e2 != nil || !from.Before(to) {
				httpapi.Error(w, id, foundation.ErrValidation)
				return
			}
			f.From = from
			if to.Before(f.To) {
				f.To = to
			}
		}
		filterBody, _ := json.Marshal(map[string]string{"type": f.Type, "actor": f.Actor, "object": f.Object, "from": f.From.Format(time.RFC3339Nano)})
		cursor := httpapi.Cursor{Tenant: scope.Tenant, App: scope.App, Root: hash.Hash(filterBody), At: f.To.UTC().Format(time.RFC3339Nano), Limit: f.Limit}
		codec := httpapi.CursorCodec{Key: []byte(os.Getenv("LOCAL_CURSOR_KEY"))}
		if token := r.URL.Query().Get("cursor"); token != "" {
			decoded, err := codec.Decode(token, cursor)
			if err != nil {
				httpapi.Error(w, id, err)
				return
			}
			f.Offset = decoded.Offset
		}
		rows, err := audit.List(r.Context(), store.DB, scope, f)
		if err != nil {
			httpapi.Error(w, id, err)
			return
		}
		var next *string
		if len(rows) == f.Limit {
			cursor.Offset = f.Offset + f.Limit
			value, err := codec.Encode(cursor)
			if err != nil {
				httpapi.Error(w, id, err)
				return
			}
			next = &value
		}
		result, _ := response.Result(200, "audit-list", id, map[string]any{"rows": rows, "as_of": f.To, "next_cursor": next})
		httpapi.Write(w, result)
	}))
	mux.Handle("POST /v1/security-events/{id}/handling-records", require("security:handle", func(w http.ResponseWriter, r *http.Request, p principal.Principal, id string) {
		var input struct {
			Revision uint64 `json:"expected_revision"`
			Status   string `json:"status"`
			Reason   string `json:"reason"`
		}
		if err := httpapi.Decode(w, r, &input); err != nil {
			httpapi.Error(w, id, err)
			return
		}
		scope := foundation.Scope{Tenant: p.Tenant, App: p.App}
		payload := struct {
			ID    string `json:"event_id"`
			Input any    `json:"input"`
		}{r.PathValue("id"), input}
		result, err := store.Execute(r.Context(), scope, "security-handle", r.Header.Get("Idempotency-Key"), payload, func(tx db.Executor) (foundation.CommandResult, error) {
			record := newID()
			if err := audit.Handle(r.Context(), tx, scope, r.PathValue("id"), record, p.Subject, input.Status, input.Reason, input.Revision, clockUTC()); err != nil {
				return foundation.CommandResult{}, err
			}
			return response.Result(201, record, id, map[string]any{"record_id": record, "revision": input.Revision + 1, "status": input.Status})
		})
		if err != nil {
			httpapi.Error(w, id, err)
			return
		}
		httpapi.Write(w, result)
	}))
	return nil
}
