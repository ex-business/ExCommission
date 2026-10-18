// Package monitoring 提供站点内只读运行观测，不持有重试、审批、派发或改账权限。
package monitoring

import (
	"context"
	"database/sql"
	"encoding/json"
	auth "github.com/ex-business/ExCommission/internal/identity/application"
	principal "github.com/ex-business/ExCommission/internal/identity/domain"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	"net/http"
	"regexp"
	"time"
)

var requestPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)

func send(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

type count struct {
	Status string     `json:"status"`
	Count  int64      `json:"count"`
	Oldest *time.Time `json:"oldest_at"`
}

// 每个查询都从已认证Principal取得租户和应用，拒绝客户端通过查询参数扩大站点范围。
func grouped(ctx context.Context, db queryer, p principal.Principal, query string) ([]count, error) {
	rows, e := db.QueryContext(ctx, query, p.Tenant, p.App)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []count{}
	for rows.Next() {
		var v count
		var at sql.NullTime
		if e = rows.Scan(&v.Status, &v.Count, &at); e != nil {
			return nil, e
		}
		if at.Valid {
			v.Oldest = &at.Time
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func Install(mux *http.ServeMux, require func(string, auth.AuthorizedHandler) http.Handler, store *mysql.Store) {
	for _, kind := range []string{"tasks", "payments", "trace"} {
		kind := kind
		permission := map[string]string{"tasks": "tasks:read", "payments": "payments:read", "trace": "audit:read"}[kind]
		mux.Handle("GET /v1/monitoring/"+kind, require(permission, func(w http.ResponseWriter, r *http.Request, p principal.Principal, rid string) {
			ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
			defer cancel()
			result := map[string]any{"request_id": rid, "observed_at": time.Now().UTC(), "kind": kind}
			if kind == "trace" {
				id := r.URL.Query().Get("request_id")
				if !requestPattern.MatchString(id) {
					send(w, 400, map[string]string{"error": "请求编号格式无效"})
					return
				}
				rows, e := store.DB.QueryContext(ctx, `SELECT entry_id,request_id,action,resource_ref,occurred_at FROM audit_entries WHERE tenant_id=? AND app_id=? AND request_id=? ORDER BY occurred_at DESC,entry_id DESC LIMIT 101`, p.Tenant, p.App, id)
				if e != nil {
					send(w, 503, map[string]string{"error": "审计追踪暂不可用"})
					return
				}
				defer rows.Close()
				items := []map[string]any{}
				for rows.Next() {
					var entry, request, action, resource string
					var at time.Time
					if rows.Scan(&entry, &request, &action, &resource, &at) != nil {
						send(w, 503, map[string]string{"error": "审计追踪读取失败"})
						return
					}
					items = append(items, map[string]any{"entry_id": entry, "request_id": request, "action": action, "resource_ref": resource, "at": at})
				}
				if rows.Err() != nil {
					send(w, 503, map[string]string{"error": "审计追踪读取失败"})
					return
				}
				more := len(items) > 100
				if more {
					items = items[:100]
				}
				result["items"] = items
				result["truncated"] = more
				result["coverage"] = "仅持久化审计记录；无记录不代表请求未发生"
				send(w, 200, result)
				return
			}
			queries := map[string]string{}
			if kind == "tasks" {
				queries["calculation"] = `SELECT status,COUNT(*),MIN(created_at) FROM calculation_tasks WHERE tenant_id=? AND app_id=? AND status IN ('QUEUED','SUSPENDED','RETRY_WAIT','RUNNING','DEAD') GROUP BY status`
				queries["daily"] = dailyQuery()
			}
			if kind == "payments" {
				queries["orders"] = `SELECT status,COUNT(*),MIN(updated_at) FROM payment_orders WHERE tenant_id=? AND app_id=? AND status IN ('CREATED','SUBMITTED','UNKNOWN') GROUP BY status`
				queries["reconciliation"] = `SELECT reconciliation_status,COUNT(*),MIN(updated_at) FROM payment_orders WHERE tenant_id=? AND app_id=? AND reconciliation_status NOT IN ('PENDING','MATCHED') GROUP BY reconciliation_status`
			}
			for name, q := range queries {
				v, e := grouped(ctx, store.DB, p, q)
				if e != nil {
					send(w, 503, map[string]string{"error": "业务监控采集失败，请检查依赖状态", "request_id": rid})
					return
				}
				result[name] = v
			}
			if kind == "tasks" {
				rows, e := store.DB.QueryContext(ctx, `SELECT task_id,body FROM affiliate_task_runs WHERE tenant_id=? AND app_id=? AND status='WAITING_WATERMARK' ORDER BY updated_at,task_id LIMIT 100`, p.Tenant, p.App)
				if e != nil {
					send(w, 503, map[string]string{"error": "水位明细采集失败"})
					return
				}
				defer rows.Close()
				missing := []map[string]any{}
				for rows.Next() {
					var id string
					var raw []byte
					if rows.Scan(&id, &raw) != nil {
						send(w, 503, map[string]string{"error": "水位明细读取失败"})
						return
					}
					var v struct {
						Missing []map[string]string `json:"missing_watermarks"`
						Day     string              `json:"business_day"`
						Program string              `json:"program_id"`
					}
					if json.Unmarshal(raw, &v) != nil {
						send(w, 503, map[string]string{"error": "任务水位记录格式无效"})
						return
					}
					missing = append(missing, map[string]any{"task_id": id, "business_day": v.Day, "program_id": v.Program, "missing_watermarks": v.Missing})
				}
				if rows.Err() != nil {
					send(w, 503, map[string]string{"error": "水位明细读取失败"})
					return
				}
				result["missing_watermarks"] = missing
				result["detail_limit"] = 100
			}
			send(w, 200, result)
		}))
	}
}
