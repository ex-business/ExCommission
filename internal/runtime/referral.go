package runtime

import (
	agents "github.com/ex-business/ExCommission/internal/agents/adapters"
	auth "github.com/ex-business/ExCommission/internal/identity/application"
	principal "github.com/ex-business/ExCommission/internal/identity/domain"
	ledger "github.com/ex-business/ExCommission/internal/ledger/adapters"
	plans "github.com/ex-business/ExCommission/internal/plans/adapters"
	catalog "github.com/ex-business/ExCommission/internal/platform/adapters/catalog"
	"github.com/ex-business/ExCommission/internal/platform/adapters/httpapi"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	response "github.com/ex-business/ExCommission/internal/platform/application"
	qualification "github.com/ex-business/ExCommission/internal/qualification/adapters"
	adapters "github.com/ex-business/ExCommission/internal/referral/adapters"
	application "github.com/ex-business/ExCommission/internal/referral/application"
	model "github.com/ex-business/ExCommission/internal/referral/domain"
	"net/http"
	"os"
)

func referralEnabled() bool { return os.Getenv("LOCAL_REFERRAL_REWARDS_ENABLED") == "true" }
func referralService(store *mysql.Store) application.Service {
	return application.Service{Commands: store, Query: store.DB, Repo: adapters.Repository{}, Inbox: adapters.Repository{}, Members: qualification.Repository{}, Policies: qualification.Repository{}, Plans: plans.Repository{}, Accounts: agents.Repository{}, Subjects: agents.Repository{}, Eligibility: agents.Repository{}, Contracts: agents.Repository{}, Ledger: ledger.Repository{}, Catalog: catalog.Repository{}, Now: clockUTC}
}
func installReferral(mux *http.ServeMux, require func(string, auth.AuthorizedHandler) http.Handler, store *mysql.Store) error {
	if !referralEnabled() {
		return nil
	}
	service := referralService(store)
	mux.Handle("POST /v1/referral-reward-rules", require("plans:write", func(w http.ResponseWriter, r *http.Request, p principal.Principal, id string) {
		var input model.Definition
		if err := httpapi.Decode(w, r, &input); err != nil {
			httpapi.Error(w, id, err)
			return
		}
		result, err := service.Create(r.Context(), p, input, r.Header.Get("Idempotency-Key"), id)
		if err != nil {
			httpapi.Error(w, id, err)
			return
		}
		httpapi.Write(w, result)
	}))
	mux.Handle("GET /v1/referral-reward-rules/{id}", require("plans:read", func(w http.ResponseWriter, r *http.Request, p principal.Principal, id string) {
		data, err := service.GetRule(r.Context(), p, r.PathValue("id"))
		if err != nil {
			httpapi.Error(w, id, err)
			return
		}
		result, err := response.Result(200, data.Definition.ID, id, data)
		if err != nil {
			httpapi.Error(w, id, err)
			return
		}
		httpapi.Write(w, result)
	}))
	// 独立奖励配置使用现有方案审批权限和双人复核，不改变全局鉴权或原审批类型分发。
	mux.Handle("POST /v1/referral-reward-rules/{id}/activation", require("plans:approve", func(w http.ResponseWriter, r *http.Request, p principal.Principal, id string) {
		var input application.Activation
		if err := httpapi.Decode(w, r, &input); err != nil {
			httpapi.Error(w, id, err)
			return
		}
		result, err := service.Activate(r.Context(), p, r.PathValue("id"), input, r.Header.Get("Idempotency-Key"), id)
		if err != nil {
			httpapi.Error(w, id, err)
			return
		}
		httpapi.Write(w, result)
	}))
	mux.Handle("GET /v1/referral-reward-rules/{id}/progress", require("agents:read", func(w http.ResponseWriter, r *http.Request, p principal.Principal, id string) {
		data, err := service.View(r.Context(), p, r.PathValue("id"), r.URL.Query().Get("agent_id"))
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
	mux.Handle("POST /v1/referral-reward-rules/{id}/progress-refresh", require("agents:write", func(w http.ResponseWriter, r *http.Request, p principal.Principal, id string) {
		var input struct {
			AgentID string `json:"agent_id"`
		}
		if err := httpapi.Decode(w, r, &input); err != nil {
			httpapi.Error(w, id, err)
			return
		}
		result, err := service.Refresh(r.Context(), p, r.PathValue("id"), input.AgentID, r.Header.Get("Idempotency-Key"), id)
		if err != nil {
			httpapi.Error(w, id, err)
			return
		}
		httpapi.Write(w, result)
	}))
	// 目前是受控管理/服务端领取入口，agent_id不是会员身份凭证；未来C端须通过所属代理解析端口接入。
	mux.Handle("POST /v1/referral-reward-claims", require("agents:write", func(w http.ResponseWriter, r *http.Request, p principal.Principal, id string) {
		var input application.Claim
		if err := httpapi.Decode(w, r, &input); err != nil {
			httpapi.Error(w, id, err)
			return
		}
		result, err := service.Claim(r.Context(), p, input, r.Header.Get("Idempotency-Key"), id)
		if err != nil {
			httpapi.Error(w, id, err)
			return
		}
		httpapi.Write(w, result)
	}))
	mux.Handle("GET /v1/referral-reward-claims/{id}", require("agents:read", func(w http.ResponseWriter, r *http.Request, p principal.Principal, id string) {
		data, err := service.GetClaim(r.Context(), p, r.PathValue("id"))
		if err != nil {
			httpapi.Error(w, id, err)
			return
		}
		result, err := response.Result(200, data.Reward.ClaimID, id, data)
		if err != nil {
			httpapi.Error(w, id, err)
			return
		}
		httpapi.Write(w, result)
	}))
	return nil
}
