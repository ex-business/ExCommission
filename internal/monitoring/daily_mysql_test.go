package monitoring

import (
	"context"
	"encoding/json"
	"fmt"
	principal "github.com/ex-business/ExCommission/internal/identity/domain"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	"os"
	"testing"
	"time"
)

// 使用真实MySQL解析JSON时间并按作用域聚合；测试数据只在回滚事务中，后台任务不可见。
func TestDailyBacklogStableAcrossInspectionMySQL(t *testing.T) {
	address := os.Getenv("TEST_MYSQL_ADDRESS")
	if address == "" {
		t.Skip("需要显式提供测试MySQL地址")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	user := os.Getenv("TEST_MYSQL_USER")
	if user == "" {
		user = "excommission"
	}
	store, e := mysql.Open(ctx, address, user, os.Getenv("MYSQL_PASSWORD"), "excommission")
	if e != nil {
		t.Fatal(e)
	}
	defer store.DB.Close()
	tx, e := store.DB.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	now := time.Now().UTC().Truncate(time.Second)
	old := now.Add(-2 * time.Hour)
	scope := principal.Principal{Tenant: fmt.Sprintf("monitor-test-%d", now.UnixNano()), App: "test"}
	for i, status := range []string{"READY", "RUNNING", "FAILED", "WAITING_WATERMARK", "BLOCKED"} {
		body, _ := json.Marshal(map[string]any{"due_at": old, "created_at": old.Add(-time.Hour), "started_at": old, "finished_at": old, "waiting_since": old})
		_, e = tx.ExecContext(ctx, `INSERT INTO affiliate_task_runs(tenant_id,app_id,task_id,program_id,program_revision,business_day,status,updated_at,body) VALUES(?,?,?,?,1,?,?,?,?)`, scope.Tenant, scope.App, fmt.Sprintf("%064d", i), fmt.Sprintf("monitor-%d", i), now.Format("2006-01-02"), status, now, body)
		if e != nil {
			t.Fatal(e)
		}
	}
	for i := 0; i < 3; i++ {
		if _, e = tx.ExecContext(ctx, `UPDATE affiliate_task_runs SET updated_at=? WHERE tenant_id=? AND app_id=?`, now.Add(time.Duration(i)*time.Minute), scope.Tenant, scope.App); e != nil {
			t.Fatal(e)
		}
		counts, e := grouped(ctx, tx, scope, dailyQuery())
		if e != nil {
			t.Fatal(e)
		}
		if len(counts) != 5 {
			t.Fatalf("统计缺失 %d", len(counts))
		}
		for _, v := range counts {
			if v.Count != 1 || v.Oldest == nil || !v.Oldest.Equal(old) {
				t.Fatalf("巡检重置了积压时间 status=%s oldest=%v", v.Status, v.Oldest)
			}
		}
	}
	other := scope
	other.Tenant += "-other"
	rows, e := grouped(ctx, tx, other, dailyQuery())
	if e != nil || len(rows) != 0 {
		t.Fatal("监控作用域隔离失败", e)
	}
	// 新索引由本地演示API已安装，核验其真实存在，不在测试中执行DDL。
	for table, name := range map[string]string{"audit_entries": "monitor_request", "payment_orders": "monitor_payment_state", "calculation_tasks": "monitor_task_state"} {
		var n int
		if e = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name=? AND index_name=?`, table, name).Scan(&n); e != nil || n == 0 {
			t.Fatalf("索引未生效 %s %s", table, name)
		}
	}
}
