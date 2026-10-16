package runtime

import (
	"database/sql"
	"errors"
	agents "github.com/ex-business/ExCommission/internal/agents/adapters"
	calculation "github.com/ex-business/ExCommission/internal/calculation/application"
	calcports "github.com/ex-business/ExCommission/internal/calculation/ports"
	adapters "github.com/ex-business/ExCommission/internal/dayend/adapters"
	application "github.com/ex-business/ExCommission/internal/dayend/application"
	auth "github.com/ex-business/ExCommission/internal/identity/application"
	principal "github.com/ex-business/ExCommission/internal/identity/domain"
	plans "github.com/ex-business/ExCommission/internal/plans/adapters"
	catalog "github.com/ex-business/ExCommission/internal/platform/adapters/catalog"
	"github.com/ex-business/ExCommission/internal/platform/adapters/httpapi"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	response "github.com/ex-business/ExCommission/internal/platform/application"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	qualification "github.com/ex-business/ExCommission/internal/qualification/adapters"
	users "github.com/ex-business/ExCommission/internal/users/adapters"
	"net/http"
	"os"
	"time"
)

func businessResolver() calcports.SnapshotResolver {
	return calculation.Resolver{Catalog: catalog.Repository{}, Plans: plans.Repository{}, Users: users.Repository{}, Accounts: agents.Repository{}, Relationships: agents.Repository{}, Grades: agents.Repository{}, Contracts: agents.Repository{}, Exponents: foundation.CurrencyExponents()}
}
func dayendResolver(store *mysql.Store) calcports.SnapshotResolver {
	return application.Resolver{Enabled: os.Getenv("LOCAL_DAYEND_ENABLED") == "true", Base: businessResolver(), Repo: adapters.Repository{}, Plans: plans.Repository{}, Tasks: taskRepository(store)}
}
func installDayEnd(mux *http.ServeMux, require func(string, auth.AuthorizedHandler) http.Handler, store *mysql.Store, tenant, app string) error {
	if os.Getenv("LOCAL_DAYEND_ENABLED") != "true" {
		return nil
	}
	// 权限复用现有方案写入/读取能力；不新增公共鉴权或自动授予代理资格。
	service := application.Service{Commands: store, Repo: adapters.Repository{}, Plans: plans.Repository{}, Tasks: taskRepository(store), Resolver: businessResolver(), Subjects: agents.Repository{}, Relations: agents.Repository{}, Members: qualification.Repository{}, Policies: qualification.Repository{}, Now: clockUTC}
	mux.Handle("POST /v1/day-end-freezes", require("plans:write", func(w http.ResponseWriter, r *http.Request, p principal.Principal, id string) {
		var input application.Request
		if err := httpapi.Decode(w, r, &input); err != nil {
			httpapi.Error(w, id, err)
			return
		}
		result, err := service.Freeze(r.Context(), p, input, r.Header.Get("Idempotency-Key"), id)
		if err != nil {
			httpapi.Error(w, id, err)
			return
		}
		httpapi.Write(w, result)
	}))
	mux.Handle("GET /v1/day-end-freezes", require("plans:read", func(w http.ResponseWriter, r *http.Request, p principal.Principal, id string) {
		query := r.URL.Query()
		day, parseErr := time.Parse("2006-01-02", query.Get("business_day"))
		if !foundation.ValidID(query.Get("plan_id")) || parseErr != nil || day.Format("2006-01-02") != query.Get("business_day") {
			httpapi.Error(w, id, foundation.ErrValidation)
			return
		}
		data, err := (adapters.Repository{}).Get(r.Context(), store.DB, foundation.Scope{Tenant: p.Tenant, App: p.App}, r.URL.Query().Get("plan_id"), r.URL.Query().Get("business_day"))
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				err = errors.New("NOT_FOUND")
			}
			httpapi.Error(w, id, err)
			return
		}
		result, err := response.Result(200, data.PlanID, id, data)
		if err != nil {
			httpapi.Error(w, id, err)
			return
		}
		httpapi.Write(w, result)
	}))
	return nil
}
