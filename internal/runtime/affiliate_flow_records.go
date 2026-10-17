package runtime

import (
	"bytes"
	"encoding/csv"
	"errors"
	affiliate "github.com/ex-business/ExCommission/internal/affiliate/adapters"
	repo "github.com/ex-business/ExCommission/internal/affiliateflow/adapters"
	app "github.com/ex-business/ExCommission/internal/affiliateflow/application"
	model "github.com/ex-business/ExCommission/internal/affiliateflow/domain"
	identity "github.com/ex-business/ExCommission/internal/identity/adapters"
	auth "github.com/ex-business/ExCommission/internal/identity/application"
	principal "github.com/ex-business/ExCommission/internal/identity/domain"
	"github.com/ex-business/ExCommission/internal/platform/adapters/httpapi"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	response "github.com/ex-business/ExCommission/internal/platform/application"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

func affiliateRecordFilter(r *http.Request, now time.Time) (model.RecordFilter, error) {
	q := r.URL.Query()
	f := model.RecordFilter{Kind: q.Get("kind"), Status: q.Get("status"), UserID: q.Get("user_id"), After: q.Get("after_id"), Limit: 25, AsOf: now, From: now.AddDate(0, 0, -7), To: now.Add(time.Microsecond)}
	var e error
	for name, target := range map[string]*time.Time{"from": &f.From, "to": &f.To, "as_of": &f.AsOf} {
		if q.Get(name) != "" {
			*target, e = time.Parse(time.RFC3339Nano, q.Get(name))
			if e != nil {
				return f, foundation.ErrValidation
			}
		}
	}
	if q.Get("limit") != "" {
		f.Limit, e = strconv.Atoi(q.Get("limit"))
		if e != nil {
			return f, foundation.ErrValidation
		}
	}
	if q.Get("snapshot_revision") != "" {
		f.Snapshot, e = strconv.ParseUint(q.Get("snapshot_revision"), 10, 64)
		if e != nil {
			return f, foundation.ErrValidation
		}
	}
	return f, f.Validate(now)
}

// csvSafe 防止可维护备注、名称或标识被Excel当成公式执行，不改变页面/数据库原始数据。
func csvSafe(value string) string {
	if value != "" && strings.ContainsRune("=+-@\t\r\n", rune(value[0])) {
		return "'" + value
	}
	return value
}
func installAffiliateRecords(mux *http.ServeMux, require func(string, auth.AuthorizedHandler) http.Handler, store *mysql.Store, service app.Service) {
	mux.Handle("GET /v1/affiliate/sites", require("affiliate:read", func(w http.ResponseWriter, r *http.Request, p principal.Principal, rid string) {
		rows, e := (repo.SiteCatalog{AccessFile: os.Getenv("AFFILIATE_SITE_ACCESS_FILE")}).Read(r.Context(), store.DB, p, clockUTC())
		if e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		filtered := []repo.SiteConfiguration{}
		search := r.URL.Query().Get("search")
		for _, row := range rows {
			if search == "" || strings.Contains(row.Tenant, search) || strings.Contains(row.App, search) {
				filtered = append(filtered, row)
			}
		}
		out, _ := response.Result(200, "affiliate-sites", rid, map[string]any{"rows": filtered, "read_only": true})
		httpapi.Write(w, out)
	}))

	mux.Handle("GET /v1/affiliate/currencies", require("affiliate:read", func(w http.ResponseWriter, r *http.Request, p principal.Principal, rid string) {
		data, e := affiliateCurrencies(r.Context(), store.DB, foundation.Scope{Tenant: p.Tenant, App: p.App}, clockUTC())
		if e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		out, _ := response.Result(200, "affiliate-currencies", rid, map[string]any{"rows": data})
		httpapi.Write(w, out)
	}))

	for _, path := range []string{"/v1/affiliate/rewards", "/v1/me/affiliate/records"} {
		member := strings.HasPrefix(path, "/v1/me/")
		permission := "affiliate:read"
		if member {
			permission = "member:read"
		}
		mux.Handle("GET "+path, require(permission, func(w http.ResponseWriter, r *http.Request, p principal.Principal, rid string) {
			f, e := affiliateRecordFilter(r, clockUTC())
			if e != nil {
				httpapi.Error(w, rid, e)
				return
			}
			if member {
				if p.MemberUserID == "" {
					httpapi.Error(w, rid, principal.ErrPermission)
					return
				}
				f.UserID = p.MemberUserID
			}
			sensitive := member || identity.Authorize(r.Context(), store.DB, p, p.Tenant, p.App, "agents:sensitive:read") == nil
			data, e := (repo.Repository{}).Records(r.Context(), store.DB, foundation.Scope{Tenant: p.Tenant, App: p.App}, f, sensitive)
			if e != nil {
				httpapi.Error(w, rid, e)
				return
			}
			out, _ := response.Result(200, "affiliate-records", rid, data)
			httpapi.Write(w, out)
		}))
	}
	mux.Handle("GET /v1/affiliate/rewards/{id}", require("affiliate:read", func(w http.ResponseWriter, r *http.Request, p principal.Principal, rid string) {
		sc := foundation.Scope{Tenant: p.Tenant, App: p.App}
		reward, e := service.Repo.Reward(r.Context(), store.DB, sc, r.PathValue("id"))
		if e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		if identity.Authorize(r.Context(), store.DB, p, p.Tenant, p.App, "agents:sensitive:read") != nil {
			reward.ClaimIP = ""
		}
		config, e := (affiliate.Repository{}).Version(r.Context(), store.DB, sc, reward.ConfigRevision)
		if e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		data := map[string]any{"reward": reward, "configuration": config, "daily_calculation": nil}
		if reward.Kind == model.Commission {
			d, e := service.DailyRepo.Daily(r.Context(), store.DB, sc, reward.Day)
			if e != nil {
				httpapi.Error(w, rid, e)
				return
			}
			for _, f := range d.Results {
				if f.Result.UserID == reward.UserID {
					data["daily_calculation"] = f.Result
				}
			}
		}
		out, _ := response.Result(200, reward.ID, rid, data)
		httpapi.Write(w, out)
	}))
	mux.Handle("GET /v1/affiliate/rewards/export", require("affiliate:export", func(w http.ResponseWriter, r *http.Request, p principal.Principal, rid string) {
		f, e := affiliateRecordFilter(r, clockUTC())
		if e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		f.Limit = 100
		f.After = ""
		sc := foundation.Scope{Tenant: p.Tenant, App: p.App}
		sensitive := identity.Authorize(r.Context(), store.DB, p, p.Tenant, p.App, "agents:sensitive:read") == nil
		page, e := (repo.Repository{}).Records(r.Context(), store.DB, sc, f, sensitive)
		if e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		count := 0
		for _, sum := range page.Summary {
			count += sum.Count
		}
		if count > 10000 {
			httpapi.Error(w, rid, errors.New("REPORT_TOO_LARGE"))
			return
		}
		f.AsOf = page.AsOf
		f.Snapshot = page.Snapshot
		var buffer bytes.Buffer
		buffer.WriteString("\xef\xbb\xbf")
		writer := csv.NewWriter(&buffer)
		if e = writer.Write([]string{"奖励编号", "会员编号", "代理编号", "类型", "阶梯", "业务日", "币种", "金额（最小单位）", "原计算金额（最小单位）", "状态", "解锁时间", "审核人", "审核时间", "领取批次", "领取时间", "领取IP", "备注"}); e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		stamp := func(t *time.Time) string {
			if t == nil {
				return ""
			}
			return t.UTC().Format(time.RFC3339Nano)
		}
		for {
			for _, row := range page.Rows {
				values := []string{row.ID, row.UserID, row.AgentID, row.Kind, strconv.Itoa(row.Step), row.Day, row.Currency, row.Amount, row.Original, row.Status, row.UnlockedAt.UTC().Format(time.RFC3339Nano), row.Reviewer, stamp(row.ReviewedAt), row.ClaimID, stamp(row.ClaimedAt), row.ClaimIP, row.Reason}
				for n := range values {
					values[n] = csvSafe(values[n])
				}
				if e = writer.Write(values); e != nil {
					httpapi.Error(w, rid, e)
					return
				}
			}
			if page.Next == nil {
				break
			}
			f.After = *page.Next
			page, e = (repo.Repository{}).Records(r.Context(), store.DB, sc, f, sensitive)
			if e != nil {
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
		w.Header().Set("Content-Disposition", `attachment; filename="affiliate-rewards.csv"`)
		w.Header().Set("X-Request-ID", rid)
		_, _ = w.Write(buffer.Bytes())
	}))
	mux.Handle("GET /v1/affiliate/daily/{day}", require("affiliate:read", func(w http.ResponseWriter, r *http.Request, p principal.Principal, rid string) {
		data, e := service.DailyRepo.Daily(r.Context(), store.DB, foundation.Scope{Tenant: p.Tenant, App: p.App}, r.PathValue("day"))
		if e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		out, _ := response.Result(200, data.Day, rid, data)
		httpapi.Write(w, out)
	}))
	mux.Handle("GET /v1/affiliate/agents/{user_id}/reward-switch", require("affiliate:read", func(w http.ResponseWriter, r *http.Request, p principal.Principal, rid string) {
		sc := foundation.Scope{Tenant: p.Tenant, App: p.App}
		m, e := service.Members.Own(r.Context(), store.DB, sc, r.PathValue("user_id"))
		if e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		data, e := service.Repo.Gate(r.Context(), store.DB, sc, m.UserID, m.RegisteredAt)
		if e != nil {
			httpapi.Error(w, rid, e)
			return
		}
		out, _ := response.Result(200, m.UserID, rid, data)
		httpapi.Write(w, out)
	}))
}
