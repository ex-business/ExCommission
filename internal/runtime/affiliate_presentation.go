package runtime

import (
	"database/sql"
	affiliate "github.com/ex-business/ExCommission/internal/affiliate/adapters"
	repo "github.com/ex-business/ExCommission/internal/affiliateflow/adapters"
	app "github.com/ex-business/ExCommission/internal/affiliateflow/application"
	auth "github.com/ex-business/ExCommission/internal/identity/application"
	principal "github.com/ex-business/ExCommission/internal/identity/domain"
	"github.com/ex-business/ExCommission/internal/platform/adapters/httpapi"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	response "github.com/ex-business/ExCommission/internal/platform/application"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	"math/big"
	"net/http"
)

// 新展示接口与旧agent-rules并存，按已审批配置决定入口；个人停用只暂停奖励，不隐藏站点入口。
type affiliateFeedRow struct {
	Avatar   string `json:"avatar_url"`
	Member   string `json:"member"`
	Kind     string `json:"kind"`
	Amount   string `json:"amount_minor"`
	Currency string `json:"currency"`
	At       string `json:"claimed_at"`
}

// 展示端只查询本站已领取记录；不返回原始会员ID、claimIP、交易号和审核备注。
func affiliateFeed(r *http.Request, store *mysql.Store, sc foundation.Scope) (map[string]any, error) {
	profiles, err := repo.ReadMemberProfiles(r.Context(), store.DB, sc)
	if err != nil {
		return nil, err
	}
	totals, err := store.DB.QueryContext(r.Context(), `SELECT currency,CAST(SUM(amount) AS CHAR) FROM affiliate_business_rewards WHERE tenant_id=? AND app_id=? AND status='CLAIMED' GROUP BY currency`, sc.Tenant, sc.App)
	if err != nil {
		return nil, err
	}
	sums := map[string]string{}
	for totals.Next() {
		var currency, value string
		if err = totals.Scan(&currency, &value); err != nil {
			totals.Close()
			return nil, err
		}
		sums[currency] = value
	}
	err = totals.Err()
	totals.Close()
	if err != nil {
		return nil, err
	}
	rows, err := store.DB.QueryContext(r.Context(), `SELECT user_id,kind,CAST(amount AS CHAR),currency,claimed_at FROM affiliate_business_rewards WHERE tenant_id=? AND app_id=? AND status='CLAIMED' AND claimed_at IS NOT NULL ORDER BY claimed_at DESC,reward_id DESC LIMIT 30`, sc.Tenant, sc.App)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := []affiliateFeedRow{}
	for rows.Next() {
		var id string
		var at sql.NullTime
		var row affiliateFeedRow
		if err = rows.Scan(&id, &row.Kind, &row.Amount, &row.Currency, &at); err != nil {
			return nil, err
		}
		profile := profiles[id]
		row.Avatar = profile.Avatar
		if profile.DisplayID != "" {
			id = profile.DisplayID
		}
		chars := []rune(id)
		row.Member = "****"
		if len(chars) > 12 {
			row.Member = string(chars[:4]) + "****" + string(chars[len(chars)-4:])
		} else if len(chars) > 4 {
			left := (len(chars) - 4) / 2
			row.Member = string(chars[:left]) + "****" + string(chars[left+4:])
		}
		if at.Valid {
			row.At = at.Time.UTC().Format("2006-01-02T15:04:05Z")
		}
		records = append(records, row)
	}
	return map[string]any{"totals": sums, "rows": records}, rows.Err()
}
func installAffiliatePresentation(mux *http.ServeMux, require func(string, auth.AuthorizedHandler) http.Handler, store *mysql.Store, service app.Service) {
	// 公开奖励端仍要求服务端站点身份；匿名浏览器只能经固定站点BFF读取明确白名单。
	mux.Handle("GET /v1/affiliate/public-presentation", require("affiliate:read", func(w http.ResponseWriter, r *http.Request, p principal.Principal, rid string) {
		sc := foundation.Scope{Tenant: p.Tenant, App: p.App}
		v, e := (affiliate.Repository{}).Configuration(r.Context(), store.DB, sc, clockUTC(), false)
		if e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		visible := v.Current != nil && v.Current.Config.Enabled
		if visible && service.CanReward != nil {
			if e = service.CanReward(r.Context(), store.DB, sc); e != nil {
				if e.Error() != "SITE_AGENT_DISABLED" {
					httpapi.Error(w, rid, e)
					return
				}
				visible = false
			}
		}
		data := map[string]any{"visible": visible}
		if visible {
			c := v.Current.Config
			sum := big.NewInt(0)
			rate := 0
			for _, step := range c.Steps {
				n, _ := new(big.Int).SetString(step.Amount, 10)
				sum.Add(sum, n)
			}
			for _, level := range c.Levels {
				for _, value := range level.Rates {
					if value > rate {
						rate = value
					}
				}
			}
			feed, err := affiliateFeed(r, store, sc)
			if err != nil {
				httpapi.Error(w, rid, err)
				return
			}
			data["currency"] = c.Currency
			data["currency_scale"] = c.CurrencyScale
			data["invitee_reward_minor"] = c.InviteeReward
			data["maximum_reward_minor"] = sum.String()
			data["maximum_rate_ppm"] = rate
			data["social"] = c.Social
			data["feed"] = feed
		}
		out, _ := response.Result(200, "affiliate-public", rid, data)
		httpapi.Write(w, out)
	}))
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
		feed, e := affiliateFeed(r, store, sc)
		if e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		profiles, e := repo.ReadMemberProfiles(r.Context(), store.DB, sc)
		if e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		profile := profiles[p.MemberUserID]
		profile.LastIP = ""
		out, _ := response.Result(200, p.MemberUserID, rid, map[string]any{"profile": profile, "feed": feed, "configured": config.Configured, "configuration": config.Current, "venues": venues, "entry_visible": visible, "site_enabled": siteEnabled})
		httpapi.Write(w, out)
	}))
}
