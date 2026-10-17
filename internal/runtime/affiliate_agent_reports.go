package runtime

import (
	"bytes"
	"crypto/rand"
	"database/sql"
	"encoding/csv"
	"encoding/hex"
	"errors"
	affiliate "github.com/ex-business/ExCommission/internal/affiliate/adapters"
	repo "github.com/ex-business/ExCommission/internal/affiliateflow/adapters"
	model "github.com/ex-business/ExCommission/internal/affiliateflow/domain"
	identity "github.com/ex-business/ExCommission/internal/identity/adapters"
	auth "github.com/ex-business/ExCommission/internal/identity/application"
	principal "github.com/ex-business/ExCommission/internal/identity/domain"
	"github.com/ex-business/ExCommission/internal/platform/adapters/httpapi"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	response "github.com/ex-business/ExCommission/internal/platform/application"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	"net/http"
	"strconv"
	"time"
)

func installAffiliateAgentReports(mux *http.ServeMux, require func(string, auth.AuthorizedHandler) http.Handler, store *mysql.Store) {
	for _, member := range []bool{false, true} {
		path := "/v1/affiliate/agent-report"
		permission := "affiliate:read"
		if member {
			path = "/v1/me/affiliate/team-report"
			permission = "member:read"
		}
		mux.Handle("GET "+path, require(permission, func(w http.ResponseWriter, r *http.Request, p principal.Principal, rid string) {
			sc := foundation.Scope{Tenant: p.Tenant, App: p.App}
			repository := repo.AgentReports{DB: store.DB}
			now := clockUTC()
			q := r.URL.Query()
			id := q.Get("snapshot_id")
			offset, limit := 0, 25
			var e error
			if q.Get("offset") != "" {
				offset, e = strconv.Atoi(q.Get("offset"))
				if e != nil {
					httpapi.Error(w, rid, foundation.ErrValidation)
					return
				}
			}
			if q.Get("limit") != "" {
				limit, e = strconv.Atoi(q.Get("limit"))
				if e != nil {
					httpapi.Error(w, rid, foundation.ErrValidation)
					return
				}
			}
			if offset < 0 || limit < 1 || limit > 100 {
				httpapi.Error(w, rid, foundation.ErrValidation)
				return
			}
			var report model.AgentReport
			if id != "" {
				report, e = repository.Get(r.Context(), store.DB, sc, id, p.Subject, now)
			} else {
				version, err := (affiliate.Repository{}).At(r.Context(), store.DB, sc, now)
				if err != nil {
					httpapi.Error(w, rid, err)
					return
				}
				start, end := version.Config.Day(now)
				filter := model.AgentReportFilter{Search: q.Get("search"), State: q.Get("state"), LoginIP: q.Get("login_ip"), From: start, To: end, User: q.Get("user_id"), Sort: "became_agent_at", Descending: true}
				if q.Get("period") == "yesterday" {
					filter.To = start
					filter.From = start.In(version.Config.Location()).AddDate(0, 0, -1).UTC()
				}
				if filter.LoginIP != "" && (member || identity.Authorize(r.Context(), store.DB, p, p.Tenant, p.App, "agents:sensitive:read") != nil) {
					httpapi.Error(w, rid, principal.ErrPermission)
					return
				}
				if q.Get("sort") != "" {
					filter.Sort = q.Get("sort")
				}
				if q.Get("descending") != "" {
					filter.Descending, e = strconv.ParseBool(q.Get("descending"))
					if e != nil {
						httpapi.Error(w, rid, foundation.ErrValidation)
						return
					}
				}
				for key, target := range map[string]*time.Time{"from": &filter.From, "to": &filter.To} {
					if q.Get(key) != "" {
						date, err := time.ParseInLocation("2006-01-02", q.Get(key), version.Config.Location())
						if err != nil {
							httpapi.Error(w, rid, foundation.ErrValidation)
							return
						}
						*target = date.UTC()
					}
				}
				if member {
					if p.MemberUserID == "" {
						httpapi.Error(w, rid, principal.ErrPermission)
						return
					}
					filter.User = p.MemberUserID
				}
				if e = filter.Validate(); e != nil {
					httpapi.Error(w, rid, e)
					return
				}
				if filter.From.After(now) || filter.To.After(end) {
					httpapi.Error(w, rid, foundation.ErrValidation)
					return
				}
				report, e = repository.Build(r.Context(), sc, filter, now)
				// 站点首次启用前没有历史配置，历史报表返回空集合，不以当前费率虚构旧日统计。
				if errors.Is(e, sql.ErrNoRows) {
					report = model.AgentReport{Filter: filter, Currency: version.Config.Currency, Scale: version.Config.CurrencyScale, Timezone: version.Config.Timezone, AsOf: now, Rows: []model.AgentReportRow{}}
					e = nil
				}
				if e == nil {
					var random [32]byte
					if _, e = rand.Read(random[:]); e == nil {
						id = hex.EncodeToString(random[:])
						e = repository.Save(r.Context(), store.DB, sc, id, p.Subject, report, now)
					}
				}
			}
			if e != nil {
				httpapi.Error(w, rid, e)
				return
			}
			if member && report.Filter.User != p.MemberUserID {
				httpapi.Error(w, rid, principal.ErrPermission)
				return
			}
			// 登录IP只供有敏感权限的后台读取，会员和普通读取身份不得经快照或导出获得。
			sensitive := !member && identity.Authorize(r.Context(), store.DB, p, p.Tenant, p.App, "agents:sensitive:read") == nil
			if !sensitive {
				report.Filter.LoginIP = ""
				for i := range report.Rows {
					report.Rows[i].Profile.LastIP = ""
					if member {
						report.Rows[i].Profile.LastAt = nil
					}
					for n := range report.Rows[i].Members {
						report.Rows[i].Members[n].Profile.LastIP = ""
						if member {
							report.Rows[i].Members[n].Profile.LastAt = nil
						}
					}
				}
			}
			page, e := model.AgentReportPage(report, id, offset, limit)
			if e != nil {
				httpapi.Error(w, rid, e)
				return
			}
			out, _ := response.Result(200, id, rid, page)
			httpapi.Write(w, out)
		}))
	}
	mux.Handle("GET /v1/affiliate/agent-report/export", require("affiliate:export", func(w http.ResponseWriter, r *http.Request, p principal.Principal, rid string) {
		id := r.URL.Query().Get("snapshot_id")
		if id == "" {
			httpapi.Error(w, rid, foundation.ErrValidation)
			return
		}
		data, e := (repo.AgentReports{}).Get(r.Context(), store.DB, foundation.Scope{Tenant: p.Tenant, App: p.App}, id, p.Subject, clockUTC())
		if e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		if len(data.Rows) > 10000 {
			httpapi.Error(w, rid, errors.New("REPORT_TOO_LARGE"))
			return
		}
		var buffer bytes.Buffer
		buffer.WriteString("\xef\xbb\xbf")
		writer := csv.NewWriter(&buffer)
		headers := []string{"会员编号", "代理编号", "成为代理时间", "直属人数", "团队人数", "有效直属", "有效团队", "直属充值（最小单位）", "团队充值（最小单位）", "团队提现（最小单位）", "存提差（最小单位）", "直属有效业务量（最小单位）", "团队有效业务量（最小单位）", "冻结计算佣金（最小单位）", "已领推荐（最小单位）", "已领佣金（最小单位）", "今日预估佣金（最小单位）", "币种", "账号名", "展示ID", "VIP", "最近登录时间", "最近登录IP", "直属投注（最小单位）", "团队投注（最小单位）"}
		if e = writer.Write(headers); e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		for _, row := range data.Rows {
			cells := []string{row.UserID, row.AgentID, row.BecameAgentAt.UTC().Format(time.RFC3339Nano), strconv.Itoa(row.Direct), strconv.Itoa(row.Team), strconv.Itoa(row.EffectiveDirect), strconv.Itoa(row.EffectiveTeam), row.DirectDeposit, row.TeamDeposit, row.TeamWithdrawal, row.NetDeposit, row.DirectValidBet, row.TeamValidBet, row.CalculatedCommission, row.ClaimedReferral, row.ClaimedCommission, row.Estimate, data.Currency}
			vip := ""
			at := ""
			ip := ""
			if row.Profile.VIP != nil {
				vip = strconv.Itoa(*row.Profile.VIP)
			}
			if row.Profile.LastAt != nil {
				at = row.Profile.LastAt.UTC().Format(time.RFC3339Nano)
			}
			if identity.Authorize(r.Context(), store.DB, p, p.Tenant, p.App, "agents:sensitive:read") == nil {
				ip = row.Profile.LastIP
			}
			cells = append(cells, row.Profile.Name, row.Profile.DisplayID, vip, at, ip, row.DirectBet, row.TeamBet)
			for n := range cells {
				cells[n] = csvSafe(cells[n])
			}
			if e = writer.Write(cells); e != nil {
				httpapi.Error(w, rid, e)
				return
			}
		}
		writer.Flush()
		if e = writer.Error(); e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="affiliate-agents.csv"`)
		_, _ = w.Write(buffer.Bytes())
	}))
}
