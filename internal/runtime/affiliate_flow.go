package runtime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	affiliate "github.com/ex-business/ExCommission/internal/affiliate/adapters"
	calculation "github.com/ex-business/ExCommission/internal/affiliate/domain"
	repo "github.com/ex-business/ExCommission/internal/affiliateflow/adapters"
	app "github.com/ex-business/ExCommission/internal/affiliateflow/application"
	ports "github.com/ex-business/ExCommission/internal/affiliateflow/ports"
	identity "github.com/ex-business/ExCommission/internal/identity/adapters"
	auth "github.com/ex-business/ExCommission/internal/identity/application"
	principal "github.com/ex-business/ExCommission/internal/identity/domain"
	"github.com/ex-business/ExCommission/internal/platform/adapters/httpapi"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	response "github.com/ex-business/ExCommission/internal/platform/application"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	sitesrepo "github.com/ex-business/ExCommission/internal/sites/adapters"
	wallet "github.com/ex-business/ExCommission/internal/wallet/adapters"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"
)

func affiliateFlowEnabled() bool { return os.Getenv("LOCAL_AFFILIATE_FLOW_ENABLED") == "true" }
func affiliateFlowService(store *mysql.Store) app.Service {
	scope := localScope()
	return app.Service{ClaimContextReady: len(os.Getenv("AFFILIATE_CLIENT_CONTEXT_KEY")) >= 32, Commands: store, Query: store.DB, Repo: repo.Repository{}, DailyRepo: repo.Repository{}, Relations: repo.Relations{}, Members: repo.Members{}, Configs: affiliate.Repository{}, Funds: repo.Funds{Wallet: wallet.Repository{}, Validate: validateAffiliateRewardCurrency}, Now: clockUTC, Sources: map[string]ports.Source{"sandbox": repo.HMACSource{Scope: scope, Source: "sandbox", Key: []byte(os.Getenv("LOCAL_AFFILIATE_FLOW_KEY"))}}, CanReward: siteAllow, CanRewardAt: func(ctx context.Context, tx db.Executor, sc foundation.Scope, at time.Time) error {
		config, e := (sitesrepo.Repository{}).At(ctx, tx, sc, at)
		if e != nil {
			return e
		}
		if config.Configured && !config.Enabled {
			return errors.New("SITE_AGENT_DISABLED")
		}
		return nil
	}, Authorize: func(ctx context.Context, tx db.Executor, p principal.Principal, permission string) error {
		if e := identity.Authorize(ctx, tx, p, p.Tenant, p.App, permission); e != nil {
			return e
		}
		// 来源事实与关闭/取消操作仍须可记录；资金产出另由CanReward统一检查站点开关。
		if permission == "member:claim" {
			return siteAllow(ctx, tx, foundation.Scope{Tenant: p.Tenant, App: p.App})
		}
		return nil
	}, StepUp: func(ctx context.Context, p principal.Principal, action, key string, payload any) error {
		return verifyStepUp(store, ctx, p, action, key, payload)
	}}
}
func installAffiliateFlow(ctx context.Context, mux *http.ServeMux, require func(string, auth.AuthorizedHandler) http.Handler, store *mysql.Store, tenant, application string) error {
	if !affiliateFlowEnabled() {
		return nil
	}
	if len(os.Getenv("LOCAL_AFFILIATE_FLOW_KEY")) < 32 {
		return foundation.ErrValidation
	}
	for _, item := range []struct {
		subject     string
		permissions []string
	}{{"local-operator", []string{"affiliate:ingest", "affiliate:manage", "affiliate:export"}}, {"local-approver", []string{"affiliate:review"}}} {
		if e := bootstrapFeature(ctx, store, tenant, application, item.subject, "affiliate-business-v1", item.permissions); e != nil {
			return e
		}
	}
	service := affiliateFlowService(store)
	// 计算器只读已发布配置，输入为扣除风控后的预估业务量，不创建事实、权益或钱包账变。
	mux.Handle("GET /v1/me/affiliate/calculator", require("member:read", func(w http.ResponseWriter, r *http.Request, p principal.Principal, rid string) {
		if p.MemberUserID == "" {
			httpapi.Error(w, rid, principal.ErrPermission)
			return
		}
		members, e := strconv.Atoi(r.URL.Query().Get("qualified_members"))
		if e != nil {
			httpapi.Error(w, rid, foundation.ErrValidation)
			return
		}
		v, e := service.Configs.At(r.Context(), store.DB, foundation.Scope{Tenant: p.Tenant, App: p.App}, clockUTC())
		if e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		values := [3]string{r.URL.Query().Get("level1_minor"), r.URL.Query().Get("level2_minor"), r.URL.Query().Get("level3_minor")}
		estimate, e := calculation.EstimateCommission(v.Config, members, values)
		if e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		out, e := response.Result(200, "affiliate-calculator", rid, map[string]any{"estimate": estimate, "config_revision": v.Revision, "provisional": true})
		if e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		httpapi.Write(w, out)
	}))
	mux.Handle("POST /v1/affiliate/business-facts", require("affiliate:ingest", func(w http.ResponseWriter, r *http.Request, p principal.Principal, rid string) {
		raw, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 16384))
		if e != nil {
			httpapi.Error(w, rid, foundation.ErrValidation)
			return
		}
		out, e := service.Receive(r.Context(), p, raw, r.Header.Get("X-Affiliate-Signature"), r.Header.Get("Idempotency-Key"), rid)
		if e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		httpapi.Write(w, out)
	}))
	mux.Handle("POST /v1/me/affiliate/refresh", require("member:read", func(w http.ResponseWriter, r *http.Request, p principal.Principal, rid string) {
		var in struct{}
		if e := httpapi.Decode(w, r, &in); e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		out, e := service.RefreshOwn(r.Context(), p, r.Header.Get("Idempotency-Key"), rid)
		if e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		httpapi.Write(w, out)
	}))
	mux.Handle("GET /v1/me/affiliate/overview", require("member:read", func(w http.ResponseWriter, r *http.Request, p principal.Principal, rid string) {
		data, e := service.Overview(r.Context(), p)
		if e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		out, _ := response.Result(200, p.MemberUserID, rid, data)
		httpapi.Write(w, out)
	}))
	mux.Handle("GET /v1/me/affiliate/progress", require("member:read", func(w http.ResponseWriter, r *http.Request, p principal.Principal, rid string) {
		data, e := service.Read(r.Context(), p)
		if e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		out, _ := response.Result(200, p.MemberUserID, rid, data)
		httpapi.Write(w, out)
	}))
	mux.Handle("POST /v1/me/affiliate/claims", require("member:claim", func(w http.ResponseWriter, r *http.Request, p principal.Principal, rid string) {
		raw, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 8192))
		if e != nil {
			httpapi.Error(w, rid, foundation.ErrValidation)
			return
		}
		h := sha256.Sum256(raw)
		if r.Header.Get("X-Affiliate-Body-SHA256") != hex.EncodeToString(h[:]) {
			httpapi.Error(w, rid, principal.ErrPermission)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		var in app.Claim
		if e := httpapi.Decode(w, r, &in); e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		// 领取来源必须由受控BFF签名证明；未配置时拒绝，而不是记录代理连接地址冒充会员IP。
		ip, e := repo.ClaimClientIP(r, p, []byte(os.Getenv("AFFILIATE_CLIENT_CONTEXT_KEY")), clockUTC())
		if e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		out, e := service.Claim(r.Context(), p, in, ip, r.Header.Get("Idempotency-Key"), rid)
		if e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		httpapi.Write(w, out)
	}))
	mux.Handle("POST /v1/affiliate/agents/{user_id}/reward-switch", require("affiliate:manage", func(w http.ResponseWriter, r *http.Request, p principal.Principal, rid string) {
		var in app.GateChange
		if e := httpapi.Decode(w, r, &in); e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		out, e := service.Toggle(r.Context(), p, r.PathValue("user_id"), in, r.Header.Get("Idempotency-Key"), rid)
		if e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		httpapi.Write(w, out)
	}))
	mux.Handle("POST /v1/affiliate/source-watermarks", require("affiliate:ingest", func(w http.ResponseWriter, r *http.Request, p principal.Principal, rid string) {
		raw, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 16384))
		if e != nil {
			httpapi.Error(w, rid, foundation.ErrValidation)
			return
		}
		out, e := service.ReceiveWatermark(r.Context(), p, raw, r.Header.Get("X-Affiliate-Signature"), r.Header.Get("Idempotency-Key"), rid)
		if e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		httpapi.Write(w, out)
	}))
	mux.Handle("POST /v1/affiliate/commissions/{id}/review", require("affiliate:review", func(w http.ResponseWriter, r *http.Request, p principal.Principal, rid string) {
		var in app.Review
		if e := httpapi.Decode(w, r, &in); e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		out, e := service.Review(r.Context(), p, r.PathValue("id"), in, r.Header.Get("Idempotency-Key"), rid)
		if e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		httpapi.Write(w, out)
	}))
	installAffiliateSiteConfig(mux, require, store)
	installAffiliatePresentation(mux, require, store, service)
	installAffiliateRecords(mux, require, store, service)
	installAffiliateAgentReports(mux, require, store)
	return nil
}
