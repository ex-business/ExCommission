package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	repo "github.com/ex-business/ExCommission/internal/executionrouting/adapters"
	model "github.com/ex-business/ExCommission/internal/executionrouting/domain"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	workflow "github.com/ex-business/ExCommission/internal/workflow/application"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestExecutionRoutingApprovalAndPinnedOwnership(t *testing.T) {
	if os.Getenv("TEST_MYSQL_ADDRESS") == "" {
		t.Skip("需要MySQL测试连接")
	}
	ctx := context.Background()
	store, e := mysql.Open(ctx, os.Getenv("TEST_MYSQL_ADDRESS"), "excommission", os.Getenv("MYSQL_PASSWORD"), "excommission")
	if e != nil {
		t.Fatal(e)
	}
	defer store.DB.Close()
	if e = store.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	sc := foundation.Scope{Tenant: "test-routing-" + newID(), App: "commission"}
	defer func() {
		for _, table := range []string{"execution_route_receipts", "execution_ownership", "execution_route_versions", "execution_route_heads", "workflow_requests", "workflow_execution_attempts", "workflow_decision_notes", "command_results", "audit_entries", "identity_grants", "identity_bootstraps"} {
			if _, err := store.DB.Exec("DELETE FROM "+table+" WHERE tenant_id=?", sc.Tenant); err != nil {
				t.Error(err)
			}
		}
	}()
	now := time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)
	oldClock := timeSource
	timeSource = func() time.Time { return now }
	defer func() { timeSource = oldClock }()
	op, checker := strings.Repeat("a", 32), strings.Repeat("b", 32)
	for key, value := range map[string]string{"APP_MODE": "local", "LOCAL_TENANT": sc.Tenant, "LOCAL_APP": sc.App, "LOCAL_OPERATOR_TOKEN": op, "LOCAL_APPROVER_TOKEN": checker, "LOCAL_CURSOR_KEY": strings.Repeat("c", 32), "LOCAL_SOURCE_KEY": strings.Repeat("d", 32), "LOCAL_PAYMENT_KEY": strings.Repeat("e", 32), "LOCAL_USERS_FILE": "", "OPS_IDENTITY_CONFIG_FILE": "", "LOCAL_AFFILIATE_FLOW_ENABLED": "false", "LOCAL_INGESTION_ENABLED": "false"} {
		t.Setenv(key, value)
	}
	handler, e := businessHandler(ctx, store)
	if e != nil {
		t.Fatal(e)
	}
	call := func(method, path, token string, input any, want int) map[string]any {
		t.Helper()
		raw, _ := json.Marshal(input)
		r := httptest.NewRequest(method, path, bytes.NewReader(raw))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Idempotency-Key", newID())
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s got %d want %d: %s", method, path, w.Code, want, w.Body)
		}
		var out map[string]any
		if e := json.Unmarshal(w.Body.Bytes(), &out); e != nil {
			t.Fatal(e)
		}
		if data, ok := out["data"].(map[string]any); ok {
			return data
		}
		return out
	}
	config := model.Config{Rules: []model.Rule{{ID: "route-one", Source: "sandbox", Kind: "BET_SETTLED", Engine: model.General, EffectiveFrom: now.Add(time.Hour)}}}
	change := map[string]any{"expected_revision": 0, "config": config, "reason": "指定业务执行引擎"}
	call("POST", "/v1/execution-routing/config-requests", checker, change, 403)
	preview := call("POST", "/v1/execution-routing/preview", checker, map[string]any{"config": config, "source_id": "sandbox", "event_kind": "BET_SETTLED", "business_time": now.Add(time.Hour)}, 200)
	if preview["matched"] != true {
		t.Fatal("试算未命中")
	}
	head := call("GET", "/v1/execution-routing/config", op, nil, 200)
	if head["revision"] != float64(0) {
		t.Fatal("预览不得发布配置")
	}
	pending := call("POST", "/v1/execution-routing/config-requests", op, change, 202)
	decide := func(token string, want int) {
		call("POST", "/v1/approvals/"+pending["approval_id"].(string)+"/decisions", token, map[string]any{"decision": "APPROVE", "expected_revision": 0, "object_hash": pending["object_hash"], "reason": "独立复核执行路由"}, want)
	}
	// 即使申请人同时具有审批权限，也不能批准自己的路由申请。
	if _, e = store.DB.Exec(`INSERT IGNORE INTO identity_grants VALUES(?,?,?,?)`, sc.Tenant, sc.App, "local-operator", "plans:approve"); e != nil {
		t.Fatal(e)
	}
	decide(op, 403)
	decide(checker, 200)
	head = call("GET", "/v1/execution-routing/config", op, nil, 200)
	if head["revision"] != float64(1) {
		t.Fatal("审批未生效")
	}
	call("POST", "/v1/execution-routing/config-requests", op, change, 409)
	claim := model.Claim{Source: "sandbox", EventID: "routed", BusinessID: "routed", Kind: "BET_SETTLED", Engine: model.Affiliate, At: now.Add(time.Hour)}
	acquire := func(c model.Claim) error {
		return store.Within(ctx, func(tx db.Executor) error { return (repo.Repository{}).Acquire(ctx, tx, sc, c, now) })
	}
	if e = acquire(claim); e == nil {
		t.Fatal("错误引擎未拒绝")
	}
	claim.Engine = model.General
	if e = acquire(claim); e != nil {
		t.Fatal(e)
	}
	// 新路由只影响切流后的新业务；旧已受理业务即便延迟重试也保持原路由版本。
	second := config.Rules[0]
	second.ID = "route-two"
	second.Engine = model.Affiliate
	second.EffectiveFrom = now.Add(2 * time.Hour)
	config.Rules = append(config.Rules, second)
	pending = call("POST", "/v1/execution-routing/config-requests", op, map[string]any{"expected_revision": 1, "config": config, "reason": "未来业务切流"}, 202)
	decide(checker, 200)
	if e = acquire(claim); e != nil {
		t.Fatal("旧业务重放应保持原归属", e)
	}
	var revision uint64
	if e = store.DB.QueryRow(`SELECT config_revision FROM execution_route_receipts WHERE tenant_id=? AND app_id=? AND identity_hash=?`, sc.Tenant, sc.App, claim.Keys()[1]).Scan(&revision); e != nil || revision != 1 {
		t.Fatal("旧业务路由版本被覆盖", revision, e)
	}
	claim.EventID = "after-cutover"
	claim.BusinessID = "after-cutover"
	claim.At = second.EffectiveFrom
	if e = acquire(claim); e == nil {
		t.Fatal("切流点应拒绝旧引擎")
	}
	claim.Engine = model.Affiliate
	if e = acquire(claim); e != nil {
		t.Fatal(e)
	}
	call("GET", "/v1/execution-routing/records", op, nil, 200)
	if workflow.ApprovalPermission("EXECUTION_ROUTING") != "plans:approve" {
		t.Fatal("审批权限落入错误分支")
	}
}
