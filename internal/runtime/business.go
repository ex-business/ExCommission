package runtime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strconv"
	"time"

	agentrepo "github.com/ex-business/ExCommission/internal/agents/adapters"
	agents "github.com/ex-business/ExCommission/internal/agents/application"
	agent "github.com/ex-business/ExCommission/internal/agents/domain"
	identity "github.com/ex-business/ExCommission/internal/identity/adapters"
	authhttp "github.com/ex-business/ExCommission/internal/identity/application"
	principal "github.com/ex-business/ExCommission/internal/identity/domain"
	"github.com/ex-business/ExCommission/internal/platform/adapters/httpapi"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	response "github.com/ex-business/ExCommission/internal/platform/application"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	userrepo "github.com/ex-business/ExCommission/internal/users/adapters"
	users "github.com/ex-business/ExCommission/internal/users/application"
	user "github.com/ex-business/ExCommission/internal/users/domain"
	userports "github.com/ex-business/ExCommission/internal/users/ports"
	workflowrepo "github.com/ex-business/ExCommission/internal/workflow/adapters"
	workflow "github.com/ex-business/ExCommission/internal/workflow/application"
	approval "github.com/ex-business/ExCommission/internal/workflow/domain"
)

func newID() string {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		panic("random source unavailable")
	}
	return hex.EncodeToString(bytes[:])
}

func businessHandler(ctx context.Context, store *mysql.Store) (http.Handler, error) {
	if os.Getenv("APP_MODE") != "local" {
		return nil, errors.New("explicit local identity configuration required")
	}
	installAuditObserver(store)
	operator, approver := os.Getenv("LOCAL_OPERATOR_TOKEN"), os.Getenv("LOCAL_APPROVER_TOKEN")
	if len(operator) < 32 || len(approver) < 32 || operator == approver || len(os.Getenv("LOCAL_CURSOR_KEY")) < 32 {
		return nil, errors.New("local credentials unavailable")
	}
	tenant, app := os.Getenv("LOCAL_TENANT"), os.Getenv("LOCAL_APP")
	if tenant == "" {
		tenant = "local-sandbox"
	}
	if app == "" {
		app = "commission"
	}
	p1 := principal.Principal{Tenant: tenant, App: app, Subject: "local-operator"}
	p2 := principal.Principal{Tenant: tenant, App: app, Subject: "local-approver"}
	// 本地受控初始化，仅初始化缺失授权；不会在重启时恢复已撤销授权。
	for _, item := range []struct {
		subject     string
		permissions []string
	}{{p1.Subject, []string{"users:sync", "users:read", "agents:write", "agents:read", "relations:request", "workflow:read"}}, {p2.Subject, []string{"agents:read", "workflow:read", "relations:approve"}}} {

		if err := store.Within(ctx, func(tx db.Executor) error {
			return identity.BootstrapOnce(ctx, tx, tenant, app, item.subject, item.subject, item.permissions)
		}); err != nil {
			return nil, err
		}
	}
	provider := identity.NewLocalProvider(map[string]principal.Principal{operator: p1, approver: p2})
	authorize := identity.DatabaseAuthorizer{Query: store.DB}
	require := func(permission string, handler authhttp.AuthorizedHandler) http.Handler {
		return auditHTTP(store, provider, foundation.Scope{Tenant: tenant, App: app}, permission, authhttp.Require(provider, authorize, permission, handler))
	}
	sources := map[users.SourceKey]userports.Source{}
	// 可选显式沙箱数据；不生成看似真实的外部用户。
	if file := os.Getenv("LOCAL_USERS_FILE"); file != "" {
		body, err := os.ReadFile(file)
		if err != nil {
			return nil, errors.New("sandbox source unavailable")
		}
		var records []user.Record
		if err = json.Unmarshal(body, &records); err != nil {
			return nil, errors.New("sandbox source invalid")
		}
		sources[users.SourceKey{Source: "sandbox", Version: "1"}] = userrepo.SandboxSource{Records: records}
	}
	userService := users.Service{Commands: store, Repo: userrepo.Repository{}, Query: store.DB, Sources: sources, Now: time.Now, ID: newID}
	agentService := agents.Service{Commands: store, Repo: agentrepo.Repository{}, Users: userrepo.Repository{}, Workflow: workflowrepo.Repository{}, Query: store.DB, Now: time.Now, ID: newID}
	workflowService := workflow.Service{Commands: store, Repo: workflowrepo.Repository{}, Query: store.DB, Now: time.Now, Authorize: func(ctx context.Context, tx db.Executor, p principal.Principal, permission string) error {
		return identity.Authorize(ctx, tx, p, p.Tenant, p.App, permission)
	}, Executors: map[string]workflow.Executor{
		"RELATIONSHIP": func(ctx context.Context, tx db.Executor, s foundation.Scope, r approval.Request, at time.Time) error {
			var change agent.RelationChange
			if err := json.Unmarshal(r.Payload, &change); err != nil {
				return err
			}
			return (agentrepo.Repository{}).Move(ctx, tx, s, change, at)
		},
	}}
	mux := http.NewServeMux()
	if err := installEvents(mux, require, store, tenant, app); err != nil {
		return nil, err
	}
	if err := installReports(mux, require, store, tenant, app); err != nil {
		return nil, err
	}
	if err := installPlatform(mux, require, store, &workflowService, tenant, app); err != nil {
		return nil, err
	}
	if err := installIdentityChanges(mux, require, store, &workflowService, tenant, app); err != nil {
		return nil, err
	}
	if err := installRiskAudit(mux, require, store, &workflowService, tenant, app); err != nil {
		return nil, err
	}
	if err := installRecovery(mux, require, store, &workflowService, tenant, app); err != nil {
		return nil, err
	}
	if err := installRefunds(mux, require, store, tenant, app); err != nil {
		return nil, err
	}
	if err := installPayments(mux, require, store, tenant, app); err != nil {
		return nil, err
	}
	if err := installSettlement(mux, require, store, &workflowService, tenant, app); err != nil {
		return nil, err
	}
	if err := installPlans(mux, require, store, &workflowService, tenant, app); err != nil {
		return nil, err
	}
	mux.Handle("POST /v1/user-sync-jobs", require("users:sync", func(w http.ResponseWriter, r *http.Request, p principal.Principal, id string) {
		var input users.SyncRequest
		if err := httpapi.Decode(w, r, &input); err != nil {
			httpapi.Error(w, id, err)
			return
		}
		result, err := userService.Sync(r.Context(), p, input, r.Header.Get("Idempotency-Key"), id)
		if err != nil {
			httpapi.Error(w, id, err)
			return
		}
		httpapi.Write(w, result)
	}))
	mux.Handle("GET /v1/user-sync-jobs/{id}", require("users:read", func(w http.ResponseWriter, r *http.Request, p principal.Principal, id string) {
		data, err := userService.Get(r.Context(), p, r.PathValue("id"))
		if err != nil {
			httpapi.Error(w, id, err)
			return
		}
		result, _ := response.Result(200, r.PathValue("id"), id, data)
		httpapi.Write(w, result)
	}))
	mux.Handle("POST /v1/agents", require("agents:write", func(w http.ResponseWriter, r *http.Request, p principal.Principal, id string) {
		var input agents.CreateRequest
		if err := httpapi.Decode(w, r, &input); err != nil {
			httpapi.Error(w, id, err)
			return
		}
		result, err := agentService.Create(r.Context(), p, input, r.Header.Get("Idempotency-Key"), id)
		if err != nil {
			httpapi.Error(w, id, err)
			return
		}
		httpapi.Write(w, result)
	}))
	mux.Handle("POST /v1/relationship-change-requests", require("relations:request", func(w http.ResponseWriter, r *http.Request, p principal.Principal, id string) {
		var input agent.RelationChange
		if err := httpapi.Decode(w, r, &input); err != nil {
			httpapi.Error(w, id, err)
			return
		}
		result, err := agentService.RequestMove(r.Context(), p, input, r.Header.Get("Idempotency-Key"), id)
		if err != nil {
			httpapi.Error(w, id, err)
			return
		}
		httpapi.Write(w, result)
	}))
	codec := httpapi.CursorCodec{Key: []byte(os.Getenv("LOCAL_CURSOR_KEY"))}
	mux.Handle("GET /v1/agents/{id}/ancestors", require("agents:read", func(w http.ResponseWriter, r *http.Request, p principal.Principal, id string) {
		at, err := time.Parse(time.RFC3339Nano, r.URL.Query().Get("at"))
		if err != nil || at.Nanosecond()%1000 != 0 {
			httpapi.Error(w, id, foundation.ErrValidation)
			return
		}
		limit := 64
		if raw := r.URL.Query().Get("limit"); raw != "" {
			limit, err = strconv.Atoi(raw)
			if err != nil || limit < 1 || limit > 100 {
				httpapi.Error(w, id, foundation.ErrValidation)
				return
			}
		}
		at = at.UTC().Truncate(time.Microsecond)
		cursor := httpapi.Cursor{Tenant: p.Tenant, App: p.App, Root: r.PathValue("id"), At: at.Format(time.RFC3339Nano), Limit: limit}
		if token := r.URL.Query().Get("cursor"); token != "" {
			cursor, err = codec.Decode(token, cursor)
			if err != nil {
				httpapi.Error(w, id, err)
				return
			}
		}
		items, more, err := agentService.Ancestors(r.Context(), p, r.PathValue("id"), at, cursor.Offset, limit)
		if err != nil {
			httpapi.Error(w, id, err)
			return
		}
		var next *string
		if more {
			cursor.Offset += len(items)
			token, err := codec.Encode(cursor)
			if err != nil {
				httpapi.Error(w, id, err)
				return
			}
			next = &token
		}
		result, _ := response.Result(200, r.PathValue("id"), id, map[string]any{"items": items, "next_cursor": next})
		httpapi.Write(w, result)
	}))
	mux.Handle("GET /v1/approvals/{id}", require("workflow:read", func(w http.ResponseWriter, r *http.Request, p principal.Principal, id string) {
		data, err := workflowService.Get(r.Context(), p, r.PathValue("id"))
		if err != nil {
			httpapi.Error(w, id, err)
			return
		}
		result, _ := response.Result(200, r.PathValue("id"), id, data)
		httpapi.Write(w, result)
	}))
	mux.Handle("POST /v1/approvals/{id}/decisions", require("workflow:decide", func(w http.ResponseWriter, r *http.Request, p principal.Principal, id string) {
		var input workflow.Decision
		if err := httpapi.Decode(w, r, &input); err != nil {
			httpapi.Error(w, id, err)
			return
		}
		result, err := workflowService.Decide(r.Context(), p, r.PathValue("id"), input, r.Header.Get("Idempotency-Key"), id)
		if err != nil {
			httpapi.Error(w, id, err)
			return
		}
		httpapi.Write(w, result)
	}))
	return mux, nil
}
