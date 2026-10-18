package monitoring

import (
	"context"
	"database/sql"
)

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// 状态巡检和租约心跳会刷新updated_at；积压只能按稳定业务时间计算，不能随巡检重新计时。
func dailyQuery() string {
	stamp := func(key string) string {
		return "CAST(REPLACE(LEFT(NULLIF(JSON_UNQUOTE(JSON_EXTRACT(body,'$." + key + "')),'null'),19),'T',' ') AS DATETIME)"
	}
	fallback := "COALESCE(" + stamp("due_at") + "," + stamp("created_at") + ")"
	oldest := "CASE status WHEN 'WAITING_WATERMARK' THEN COALESCE(" + stamp("waiting_since") + "," + fallback + ") WHEN 'RUNNING' THEN COALESCE(" + stamp("started_at") + "," + fallback + ") WHEN 'FAILED' THEN COALESCE(" + stamp("finished_at") + "," + fallback + ") ELSE " + fallback + " END"
	return "SELECT status,COUNT(*),MIN(" + oldest + ") FROM affiliate_task_runs WHERE tenant_id=? AND app_id=? AND status IN ('READY','RUNNING','FAILED','WAITING_WATERMARK','BLOCKED') GROUP BY status"
}
