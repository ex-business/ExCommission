package runtime

import (
	affiliate "github.com/ex-business/ExCommission/internal/affiliate/adapters"
	app "github.com/ex-business/ExCommission/internal/affiliateflow/application"
	auth "github.com/ex-business/ExCommission/internal/identity/application"
	principal "github.com/ex-business/ExCommission/internal/identity/domain"
	"github.com/ex-business/ExCommission/internal/platform/adapters/httpapi"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	response "github.com/ex-business/ExCommission/internal/platform/application"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	"net/http"
)

// 新展示接口与旧agent-rules并存，按已审批配置决定入口；个人停用只暂停奖励，不隐藏站点入口。
func installAffiliatePresentation(mux *http.ServeMux, require func(string, auth.AuthorizedHandler) http.Handler, store *mysql.Store, service app.Service) {
	mux.Handle("GET /v1/me/affiliate/presentation", require("member:read", func(w http.ResponseWriter, r *http.Request, p principal.Principal, rid string) {
		if p.MemberUserID == "" {
			httpapi.Error(w, rid, principal.ErrPermission)
			return
		}
		sc := foundation.Scope{Tenant: p.Tenant, App: p.App}
		config, e := (affiliate.Repository{}).Configuration(r.Context(), store.DB, sc, clockUTC(), false)
		if e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		venues, e := (affiliate.Repository{}).Venues(r.Context(), store.DB, sc)
		if e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		siteEnabled := true
		if service.CanReward != nil {
			if err := service.CanReward(r.Context(), store.DB, sc); err != nil {
				if err.Error() != "SITE_AGENT_DISABLED" {
					httpapi.Error(w, rid, err)
					return
				}
				siteEnabled = false
			}
		}
		visible := true
		if config.Current != nil && (!config.Current.Config.Enabled || !siteEnabled) && config.Current.Config.Extension != nil && config.Current.Config.Extension.DisabledEntry == "HIDE" {
			visible = false
		}
		out, _ := response.Result(200, p.MemberUserID, rid, map[string]any{"configured": config.Configured, "configuration": config.Current, "venues": venues, "entry_visible": visible, "site_enabled": siteEnabled})
		httpapi.Write(w, out)
	}))
}
