package runtime

import (
	agents "github.com/ex-business/ExCommission/internal/agents/adapters"
	auth "github.com/ex-business/ExCommission/internal/identity/application"
	principal "github.com/ex-business/ExCommission/internal/identity/domain"
	"github.com/ex-business/ExCommission/internal/platform/adapters/httpapi"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	adapters "github.com/ex-business/ExCommission/internal/registration/adapters"
	registration "github.com/ex-business/ExCommission/internal/registration/application"
	model "github.com/ex-business/ExCommission/internal/registration/domain"
	ports "github.com/ex-business/ExCommission/internal/registration/ports"
	users "github.com/ex-business/ExCommission/internal/users/adapters"
	"io"
	"net/http"
	"os"
)

func installRegistration(mux *http.ServeMux, require func(string, auth.AuthorizedHandler) http.Handler, store *mysql.Store, tenant, app string) error {
	// 显式启用以保持已有同步/手工代理流程兼容；来源适配器按作用域装配，不扩展公共鉴权。
	if os.Getenv("LOCAL_REGISTRATION_ENABLED") != "true" {
		return nil
	}
	key := []byte(os.Getenv("LOCAL_REGISTRATION_KEY"))
	if len(key) < 32 {
		return foundation.ErrValidation
	}
	scope := foundation.Scope{Tenant: tenant, App: app}
	service := registration.Service{Commands: store, Repo: adapters.Repository{}, Users: users.Repository{}, Agents: agents.Repository{}, Sources: map[string]ports.Source{"sandbox": adapters.HMACSource{Scope: scope, SourceID: "sandbox", Key: key}}, RegistrationURLs: map[string]string{"sandbox": os.Getenv("LOCAL_REGISTRATION_URL")}, Now: clockUTC, ID: newID}
	mux.Handle("POST /v1/registration-invitations", require("agents:write", func(w http.ResponseWriter, r *http.Request, p principal.Principal, id string) {
		var input model.InvitationRequest
		if err := httpapi.Decode(w, r, &input); err != nil {
			httpapi.Error(w, id, err)
			return
		}
		result, err := service.Invite(r.Context(), p, input, r.Header.Get("Idempotency-Key"), id)
		if err != nil {
			httpapi.Error(w, id, err)
			return
		}
		httpapi.Write(w, result)
	}))
	mux.Handle("POST /v1/registrations", require("users:sync", func(w http.ResponseWriter, r *http.Request, p principal.Principal, id string) {
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if err != nil {
			httpapi.Error(w, id, foundation.ErrValidation)
			return
		}
		result, err := service.Register(r.Context(), p, raw, r.Header.Get("X-Registration-Signature"), r.Header.Get("Idempotency-Key"), id)
		if err != nil {
			httpapi.Error(w, id, err)
			return
		}
		httpapi.Write(w, result)
	}))
	return nil
}
