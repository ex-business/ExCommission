package runtime

import (
	"database/sql"
	"errors"
	auth "github.com/ex-business/ExCommission/internal/identity/application"
	principal "github.com/ex-business/ExCommission/internal/identity/domain"
	"github.com/ex-business/ExCommission/internal/platform/adapters/httpapi"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	response "github.com/ex-business/ExCommission/internal/platform/application"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	adapters "github.com/ex-business/ExCommission/internal/qualification/adapters"
	application "github.com/ex-business/ExCommission/internal/qualification/application"
	model "github.com/ex-business/ExCommission/internal/qualification/domain"
	ports "github.com/ex-business/ExCommission/internal/qualification/ports"
	"io"
	"net/http"
	"os"
	"time"
)

func installQualification(mux *http.ServeMux, require func(string, auth.AuthorizedHandler) http.Handler, store *mysql.Store, tenant, app string) error {
	// 新模块显式启用，不改变原计佣事件、用户同步、等级评估或公共鉴权策略。
	if os.Getenv("LOCAL_QUALIFICATION_ENABLED") != "true" {
		return nil
	}
	key := []byte(os.Getenv("LOCAL_QUALIFICATION_KEY"))
	if len(key) < 32 {
		return foundation.ErrValidation
	}
	scope := foundation.Scope{Tenant: tenant, App: app}
	service := application.Service{Commands: store, Query: store.DB, Repo: adapters.Repository{}, Members: adapters.Members{}, Sources: map[string]ports.Source{"sandbox": adapters.HMACSource{Scope: scope, SourceID: "sandbox", Key: key}}, Currencies: foundation.CurrencyExponents(), Now: clockUTC}
	mux.Handle("POST /v1/member-qualification-policies", require("agents:write", func(w http.ResponseWriter, r *http.Request, p principal.Principal, id string) {
		var input model.Policy
		if err := httpapi.Decode(w, r, &input); err != nil {
			httpapi.Error(w, id, err)
			return
		}
		result, err := service.CreatePolicy(r.Context(), p, input, r.Header.Get("Idempotency-Key"), id)
		if err != nil {
			httpapi.Error(w, id, err)
			return
		}
		httpapi.Write(w, result)
	}))
	mux.Handle("GET /v1/member-qualification-policies/{id}", require("agents:read", func(w http.ResponseWriter, r *http.Request, p principal.Principal, id string) {
		policyID := r.PathValue("id")
		if !foundation.ValidID(policyID) {
			httpapi.Error(w, id, foundation.ErrValidation)
			return
		}
		data, err := (adapters.Repository{}).Policy(r.Context(), store.DB, foundation.Scope{Tenant: p.Tenant, App: p.App}, policyID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				err = errors.New("NOT_FOUND")
			}
			httpapi.Error(w, id, err)
			return
		}
		result, err := response.Result(200, policyID, id, data)
		if err != nil {
			httpapi.Error(w, id, err)
			return
		}
		httpapi.Write(w, result)
	}))
	mux.Handle("POST /v1/member-qualification-facts", require("users:sync", func(w http.ResponseWriter, r *http.Request, p principal.Principal, id string) {
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if err != nil {
			httpapi.Error(w, id, foundation.ErrValidation)
			return
		}
		result, err := service.Receive(r.Context(), p, raw, r.Header.Get("X-Qualification-Signature"), r.Header.Get("Idempotency-Key"), id)
		if err != nil {
			httpapi.Error(w, id, err)
			return
		}
		httpapi.Write(w, result)
	}))
	mux.Handle("GET /v1/member-qualifications", require("users:read", func(w http.ResponseWriter, r *http.Request, p principal.Principal, id string) {
		data, err := service.Get(r.Context(), p, r.URL.Query().Get("policy_id"), r.URL.Query().Get("source_id"), r.URL.Query().Get("external_user_id"))
		if err != nil {
			httpapi.Error(w, id, err)
			return
		}
		result, err := response.Result(200, data.AgentID, id, data)
		if err != nil {
			httpapi.Error(w, id, err)
			return
		}
		httpapi.Write(w, result)
	}))
	mux.Handle("GET /v1/member-qualification-statistics", require("users:read", func(w http.ResponseWriter, r *http.Request, p principal.Principal, id string) {
		q := r.URL.Query()
		at, err := time.Parse(time.RFC3339Nano, q.Get("as_of"))
		if err != nil || at.After(clockUTC()) || at.Year() < 1000 || at.Nanosecond()%1000 != 0 || !foundation.ValidID(q.Get("policy_id")) || !foundation.ValidID(q.Get("source_id")) {
			httpapi.Error(w, id, foundation.ErrValidation)
			return
		}
		data, err := (adapters.Repository{}).EffectiveStatisticsAt(r.Context(), store.DB, foundation.Scope{Tenant: p.Tenant, App: p.App}, q.Get("policy_id"), q.Get("source_id"), at)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				err = errors.New("NOT_FOUND")
			}
			httpapi.Error(w, id, err)
			return
		}
		result, err := response.Result(200, q.Get("policy_id"), id, data)
		if err != nil {
			httpapi.Error(w, id, err)
			return
		}
		httpapi.Write(w, result)
	}))
	return nil
}
