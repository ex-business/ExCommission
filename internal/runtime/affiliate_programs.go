package runtime

import (
	"context"
	configrepo "github.com/ex-business/ExCommission/internal/affiliate/adapters"
	calculation "github.com/ex-business/ExCommission/internal/affiliate/domain"
	flowrepo "github.com/ex-business/ExCommission/internal/affiliateflow/adapters"
	flowapp "github.com/ex-business/ExCommission/internal/affiliateflow/application"
	flow "github.com/ex-business/ExCommission/internal/affiliateflow/domain"
	programs "github.com/ex-business/ExCommission/internal/affiliateprograms"
	auth "github.com/ex-business/ExCommission/internal/identity/application"
	principal "github.com/ex-business/ExCommission/internal/identity/domain"
	"github.com/ex-business/ExCommission/internal/platform/adapters/httpapi"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	response "github.com/ex-business/ExCommission/internal/platform/application"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	wallet "github.com/ex-business/ExCommission/internal/wallet/adapters"
	approvalrepo "github.com/ex-business/ExCommission/internal/workflow/adapters"
	workflow "github.com/ex-business/ExCommission/internal/workflow/application"
	"net/http"
	"time"
)

func programService(store *mysql.Store, base flowapp.Service) programs.Service {
	// 共享真实站点钱包；新权益用自己冻结的币种配置校验，旧权益保持原验证路径。
	base.Funds = flowrepo.Funds{Wallet: wallet.Repository{}, Validate: func(ctx context.Context, tx db.Executor, sc foundation.Scope, r flow.Reward, at time.Time) error {
		if r.ProgramID == "" {
			return validateAffiliateRewardCurrency(ctx, tx, sc, r, at)
		}
		if r.ProgramConfig == nil || r.ProgramConfig.ProgramID != r.ProgramID || r.ProgramConfig.Revision != r.ConfigRevision || r.ProgramConfig.Config.Currency != r.Currency {
			return foundation.ErrValidation
		}
		return validateAffiliateCurrency(ctx, tx, sc, r.ProgramConfig.Config, at)
	}}
	return programs.Service{Flow: base, Repo: programs.Repository{}, Workflow: approvalrepo.Repository{}, ID: newID, Hash: approvalrepo.Hash, ValidateConfig: func(ctx context.Context, tx db.Executor, sc foundation.Scope, c calculation.Config, at time.Time) error {
		if e := validateAffiliateCurrency(ctx, tx, sc, c, at); e != nil {
			return e
		}
		venues, e := (configrepo.Repository{}).Venues(ctx, tx, sc)
		if e != nil {
			return e
		}
		known := map[string]bool{}
		for _, v := range venues {
			known[v.ID] = true
		}
		for _, id := range c.VenueIDs {
			if !known[id] {
				return calculation.ErrValidation
			}
		}
		return nil
	}}
}
func configurePrograms(store *mysql.Store, base flowapp.Service) flowapp.Service {
	base = configureAffiliateTaskRuns(store, base)
	p := programService(store, base)
	base.Funds = p.Flow.Funds
	base.ProgramAccept = p.Accept
	base.ProgramValidate = p.ValidateEvent
	base.ProgramRun = p.RunOnce
	base.ProgramReversal = p.Reverse
	return base
}
func installAffiliatePrograms(mux *http.ServeMux, require func(string, auth.AuthorizedHandler) http.Handler, store *mysql.Store, approvals *workflow.Service) {
	p := programService(store, affiliateFlowService(store))
	approvals.Executors["AFFILIATE_PROGRAM"] = p.Execute
	mux.Handle("GET /v1/affiliate/programs", require("affiliate:read", func(w http.ResponseWriter, r *http.Request, user principal.Principal, rid string) {
		data, e := p.Summary(r.Context(), foundation.Scope{Tenant: user.Tenant, App: user.App})
		if e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		out, _ := response.Result(200, "affiliate-programs", rid, data)
		httpapi.Write(w, out)
	}))
	mux.Handle("POST /v1/affiliate/programs/preview", require("affiliate:configure", func(w http.ResponseWriter, r *http.Request, user principal.Principal, rid string) {
		var in programs.Change
		if e := httpapi.Decode(w, r, &in); e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		data, e := p.Preview(r.Context(), foundation.Scope{Tenant: user.Tenant, App: user.App}, in)
		if e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		out, _ := response.Result(200, in.ID, rid, data)
		httpapi.Write(w, out)
	}))
	mux.Handle("POST /v1/affiliate/programs/requests", require("affiliate:configure", func(w http.ResponseWriter, r *http.Request, user principal.Principal, rid string) {
		var in programs.Change
		if e := httpapi.Decode(w, r, &in); e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		out, e := p.Request(r.Context(), user, in, r.Header.Get("Idempotency-Key"), rid)
		if e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		httpapi.Write(w, out)
	}))
	mux.Handle("GET /v1/me/affiliate/program-rewards", require("member:read", func(w http.ResponseWriter, r *http.Request, user principal.Principal, rid string) {
		if user.MemberUserID == "" {
			httpapi.Error(w, rid, principal.ErrPermission)
			return
		}
		sc := foundation.Scope{Tenant: user.Tenant, App: user.App}
		rewards, e := p.Flow.Repo.Rewards(r.Context(), store.DB, sc, user.MemberUserID)
		if e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		rows := []flow.Reward{}
		for _, reward := range rewards {
			if reward.ProgramID != "" {
				rows = append(rows, reward)
			}
		}
		out, _ := response.Result(200, user.MemberUserID, rid, map[string]any{"rewards": rows})
		httpapi.Write(w, out)
	}))

	// 试算仅读取指定方案版本，禁止通过预览创建任何账簿或领取记录。
	mux.Handle("POST /v1/affiliate/programs/simulate", require("affiliate:read", func(w http.ResponseWriter, r *http.Request, user principal.Principal, rid string) {
		var in struct {
			Config  programs.Change `json:"program"`
			Members int             `json:"qualified_members"`
			Layers  []string        `json:"layer_minor"`
		}
		if e := httpapi.Decode(w, r, &in); e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		if e := in.Config.Validate(); e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		result, e := calculation.EstimateCommissionLayers(in.Config.Config, in.Members, in.Layers)
		if e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		out, _ := response.Result(200, in.Config.ID, rid, map[string]any{"estimate": result, "provisional": true})
		httpapi.Write(w, out)
	}))
}
