package runtime

import (
	"context"
	"errors"
	"net/http"
	"os"
	"sync"
	"time"

	flow "github.com/ex-business/ExCommission/internal/affiliateflow/application"
	identity "github.com/ex-business/ExCommission/internal/identity/adapters"
	auth "github.com/ex-business/ExCommission/internal/identity/application"
	principal "github.com/ex-business/ExCommission/internal/identity/domain"
	repo "github.com/ex-business/ExCommission/internal/ingestion/adapters"
	app "github.com/ex-business/ExCommission/internal/ingestion/application"
	model "github.com/ex-business/ExCommission/internal/ingestion/domain"
	httpapi "github.com/ex-business/ExCommission/internal/platform/adapters/httpapi"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	response "github.com/ex-business/ExCommission/internal/platform/application"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	approvals "github.com/ex-business/ExCommission/internal/workflow/adapters"
	workflow "github.com/ex-business/ExCommission/internal/workflow/application"
)

func ingestionEnabled() bool { return os.Getenv("LOCAL_INGESTION_ENABLED") == "true" }

type ingestionInit struct {
	once sync.Once
	err  error
}

var ingestionSchemas sync.Map

// API和worker共用初始化入口；仅在显式开启模块时创建模块表，不改动已有迁移摘要或旧来源。
func ingestionRegistry(store *mysql.Store) repo.Registry {
	value, _ := ingestionSchemas.LoadOrStore(store.DB, &ingestionInit{})
	init := value.(*ingestionInit)
	init.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		init.err = (repo.Repository{}).Ensure(ctx, store.DB)
	})
	return repo.Registry{Query: store.DB, KeysFile: os.Getenv("AFFILIATE_INGESTION_KEYS_FILE"), InitError: init.err}
}
func configureIngestion(store *mysql.Store, s flow.Service) flow.Service {
	if !ingestionEnabled() {
		return s
	}
	registry := ingestionRegistry(store)
	s.ResolveSource = registry.Resolve
	s.WatermarkGuard = registry.WatermarkGuard
	s.SourceGuard = registry.Guard
	s.SourceReceipt = registry.Receipt
	s.Members = repo.Members{Members: s.Members, Registry: registry}
	s.RequiredSources = func(ctx context.Context, tx db.Executor, sc foundation.Scope, start, end time.Time) ([]string, error) {
		if registry.InitError != nil {
			return nil, registry.InitError
		}
		return (repo.Repository{}).RequiredSources(ctx, tx, sc, start, end)
	}
	return s
}
func ingestionService(store *mysql.Store) app.Service {
	return app.Service{Commands: store, Query: store.DB, Repo: repo.Repository{}, Workflow: approvals.Repository{}, Now: clockUTC, ID: newID, Hash: approvals.Hash, KeysFile: os.Getenv("AFFILIATE_INGESTION_KEYS_FILE"), Authorize: func(ctx context.Context, tx db.Executor, p principal.Principal, permission string) error {
		return identity.Authorize(ctx, tx, p, p.Tenant, p.App, permission)
	}}
}
func installIngestion(ctx context.Context, mux *http.ServeMux, require func(string, auth.AuthorizedHandler) http.Handler, store *mysql.Store, wf *workflow.Service, tenant, application string) error {
	if !ingestionEnabled() {
		return nil
	}
	if !affiliateFlowEnabled() {
		return errors.New("数据接入模块要求启用代理业务事实")
	}
	registry := ingestionRegistry(store)
	if registry.InitError != nil {
		return registry.InitError
	}
	for _, item := range []struct {
		subject     string
		permissions []string
	}{{"local-operator", []string{"ingestion:read", "ingestion:configure"}}, {"local-approver", []string{"ingestion:read", "ingestion:approve"}}} {
		if e := bootstrapFeature(ctx, store, tenant, application, item.subject, "ingestion-config-v1", item.permissions); e != nil {
			return e
		}
	}
	service := ingestionService(store)
	wf.Executors["INGESTION_CONFIG"] = service.Execute
	send := func(w http.ResponseWriter, rid string, data any) {
		out, e := response.Result(200, "ingestion", rid, data)
		if e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		httpapi.Write(w, out)
	}
	mux.Handle("GET /v1/affiliate/ingestion/config", require("ingestion:read", func(w http.ResponseWriter, r *http.Request, p principal.Principal, rid string) {
		data, e := service.Repo.Head(r.Context(), store.DB, foundation.Scope{Tenant: p.Tenant, App: p.App}, false)
		if e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		send(w, rid, data)
	}))
	mux.Handle("GET /v1/affiliate/ingestion/key-refs", require("ingestion:read", func(w http.ResponseWriter, r *http.Request, p principal.Principal, rid string) {
		keys, e := repo.KeyRing(registry.KeysFile)
		if e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		refs := []string{}
		for _, key := range keys {
			if key.Tenant == p.Tenant && key.App == p.App {
				refs = append(refs, key.Ref)
			}
		}
		send(w, rid, map[string]any{"key_refs": refs})
	}))
	mux.Handle("POST /v1/affiliate/ingestion/config-requests", require("ingestion:configure", func(w http.ResponseWriter, r *http.Request, p principal.Principal, rid string) {
		var in app.Change
		if e := httpapi.Decode(w, r, &in); e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		out, e := service.Request(r.Context(), p, in, r.Header.Get("Idempotency-Key"), rid)
		if e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		httpapi.Write(w, out)
	}))
	mux.Handle("GET /v1/affiliate/ingestion/config-requests/{id}", require("ingestion:read", func(w http.ResponseWriter, r *http.Request, p principal.Principal, rid string) {
		a, e := service.Workflow.Get(r.Context(), store.DB, foundation.Scope{Tenant: p.Tenant, App: p.App}, r.PathValue("id"), false)
		if e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		if a.Type != "INGESTION_CONFIG" {
			httpapi.Error(w, rid, errors.New("NOT_FOUND"))
			return
		}
		change, e := app.DecodeChange(a.Payload)
		if e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		send(w, rid, map[string]any{"approval": a, "change": change})
	}))
	mux.Handle("POST /v1/affiliate/ingestion/preview", require("ingestion:configure", func(w http.ResponseWriter, r *http.Request, p principal.Principal, rid string) {
		var in struct {
			Config   model.Config   `json:"config"`
			Envelope model.Envelope `json:"envelope"`
		}
		if e := httpapi.Decode(w, r, &in); e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		if e := in.Config.Validate(); e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		for _, source := range in.Config.Sources {
			if source.ID == in.Envelope.Source {
				f, e := repo.Map(source, in.Envelope)
				if e == nil {
					e = repo.ValidateScale(r.Context(), store.DB, foundation.Scope{Tenant: p.Tenant, App: p.App}, source, in.Envelope, f)
				}
				if e != nil {
					httpapi.Error(w, rid, e)
					return
				}
				send(w, rid, map[string]any{"preview_only": true, "fact": f})
				return
			}
		}
		httpapi.Error(w, rid, model.ErrEvent)
	}))
	mux.Handle("GET /v1/affiliate/ingestion/records", require("ingestion:read", func(w http.ResponseWriter, r *http.Request, p principal.Principal, rid string) {
		rows, e := service.Repo.Records(r.Context(), store.DB, foundation.Scope{Tenant: p.Tenant, App: p.App})
		if e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		send(w, rid, map[string]any{"rows": rows})
	}))
	return nil
}
