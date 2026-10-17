package runtime

import (
	"database/sql"
	"errors"
	affiliate "github.com/ex-business/ExCommission/internal/affiliate/adapters"
	model "github.com/ex-business/ExCommission/internal/affiliate/domain"
	repo "github.com/ex-business/ExCommission/internal/affiliateflow/adapters"
	identity "github.com/ex-business/ExCommission/internal/identity/adapters"
	auth "github.com/ex-business/ExCommission/internal/identity/application"
	principal "github.com/ex-business/ExCommission/internal/identity/domain"
	"github.com/ex-business/ExCommission/internal/platform/adapters/httpapi"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	response "github.com/ex-business/ExCommission/internal/platform/application"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	"log/slog"
	"net/http"
	"os"
)

// 目标站点来自明确跨站清单；改写作用域前先验证白名单，再验证目标业务权限，绝不使用同名账号推断跨站授权。
func affiliateSiteTarget(r *http.Request, p principal.Principal, write bool, store *mysql.Store) (principal.Principal, error) {
	target := repo.SiteTarget{Tenant: r.PathValue("tenant"), App: r.PathValue("app")}
	if !foundation.ValidID(target.Tenant) || !foundation.ValidID(target.App) {
		return p, foundation.ErrValidation
	}
	catalog := repo.SiteCatalog{AccessFile: os.Getenv("AFFILIATE_SITE_ACCESS_FILE")}
	targets, e := catalog.Targets(p)
	if e != nil {
		return p, e
	}
	found := false
	for _, v := range targets {
		if v == target {
			found = true
		}
	}
	if !found {
		return p, principal.ErrPermission
	}
	if write {
		allowed, e := catalog.Writable(p, target)
		if e != nil {
			return p, e
		}
		if !allowed {
			return p, principal.ErrPermission
		}
	}
	scoped := p
	scoped.Tenant = target.Tenant
	scoped.App = target.App
	if write {
		for _, permission := range []string{"affiliate:manage", "affiliate:configure"} {
			if e := identity.Authorize(r.Context(), store.DB, scoped, target.Tenant, target.App, permission); e != nil {
				return p, e
			}
		}
	}
	return scoped, nil
}
func installAffiliateSiteConfig(mux *http.ServeMux, require func(string, auth.AuthorizedHandler) http.Handler, store *mysql.Store) {
	service := affiliateConfiguration(store)
	mux.Handle("GET /v1/affiliate/sites/{tenant}/{app}/config", require("affiliate:read", func(w http.ResponseWriter, r *http.Request, p principal.Principal, rid string) {
		target, e := affiliateSiteTarget(r, p, false, store)
		if e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		sc := foundation.Scope{Tenant: target.Tenant, App: target.App}
		config, e := service.Repo.Configuration(r.Context(), store.DB, sc, clockUTC(), false)
		if errors.Is(e, sql.ErrNoRows) {
			config = model.Configuration{}
			e = nil
		}
		if e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		venues, e := service.Repo.Venues(r.Context(), store.DB, sc)
		if e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		if len(venues) == 0 && !config.Configured {
			venues = affiliate.DefaultVenues()
		}
		currencies, e := affiliateCurrencies(r.Context(), store.DB, sc, clockUTC())
		if e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		out, _ := response.Result(200, "affiliate-site-config", rid, map[string]any{"configuration": config, "venues": venues, "currencies": currencies, "tenant_id": target.Tenant, "app_id": target.App})
		httpapi.Write(w, out)
	}))
	mux.Handle("POST /v1/affiliate/sites/{tenant}/{app}/config-requests", require("affiliate:configure", func(w http.ResponseWriter, r *http.Request, p principal.Principal, rid string) {
		target, e := affiliateSiteTarget(r, p, true, store)
		if e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		var in model.Change
		if e := httpapi.Decode(w, r, &in); e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		// 新站点仅初始化独立返佣空间和默认目录，不写奖励、钱包或公共站点配置。
		if e := (affiliate.Repository{}).Ensure(r.Context(), store.DB, foundation.Scope{Tenant: target.Tenant, App: target.App}); e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		out, e := service.Request(r.Context(), target, in, r.Header.Get("Idempotency-Key"), rid)
		if e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		if !out.Replayed {
			slog.InfoContext(r.Context(), "跨站返佣配置申请已提交", "request_id", rid, "origin_tenant", p.Tenant, "origin_app", p.App, "target_tenant", target.Tenant, "target_app", target.App, "applicant_id", p.Subject, "approval_id", out.ResourceID, "result", "PENDING_REVIEW")
		}
		httpapi.Write(w, out)
	}))
}
