package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	configs "github.com/ex-business/ExCommission/internal/affiliate/adapters"
	catalog "github.com/ex-business/ExCommission/internal/platform/adapters/catalog"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestAffiliateCrossSiteConfigurationAuthorization(t *testing.T) {
	if os.Getenv("TEST_MYSQL_ADDRESS") == "" {
		t.Skip("需要真实MySQL")
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
	tenant := "test-affiliate-extension-" + newID()
	defer func() {
		rows, e := store.DB.Query(`SELECT DISTINCT TABLE_NAME FROM information_schema.columns WHERE TABLE_SCHEMA=DATABASE() AND COLUMN_NAME='tenant_id'`)
		if e != nil {
			t.Error(e)
			return
		}
		tables := []string{}
		for rows.Next() {
			var table string
			if e = rows.Scan(&table); e != nil {
				t.Error(e)
				break
			}
			if !regexp.MustCompile(`^[a-z_]+$`).MatchString(table) {
				t.Error("非法表名")
				break
			}
			tables = append(tables, table)
		}
		rows.Close()
		for _, table := range tables {
			if _, e = store.DB.Exec("DELETE FROM `"+table+"` WHERE tenant_id=?", tenant); e != nil {
				t.Error(e)
			}
		}
	}()
	op := strings.Repeat("o", 32)
	checker := strings.Repeat("a", 32)
	for k, v := range map[string]string{"APP_MODE": "local", "LOCAL_TENANT": tenant, "LOCAL_APP": "own", "LOCAL_OPERATOR_TOKEN": op, "LOCAL_APPROVER_TOKEN": checker, "LOCAL_CURSOR_KEY": strings.Repeat("c", 32), "LOCAL_SOURCE_KEY": strings.Repeat("s", 32), "LOCAL_PAYMENT_KEY": strings.Repeat("p", 32), "LOCAL_AFFILIATE_FLOW_ENABLED": "true", "LOCAL_AFFILIATE_FLOW_KEY": strings.Repeat("f", 32), "AUTH_LOCAL_ACCOUNTS_FILE": "", "AUTH_EXTERNAL_FILE": "", "AUTH_MEMBER_SOURCE_FILE": "", "LOCAL_USERS_FILE": ""} {
		t.Setenv(k, v)
	}
	handler, e := businessHandler(ctx, store)
	if e != nil {
		t.Fatal(e)
	}
	target := foundation.Scope{Tenant: tenant, App: "target"}
	if e = (catalog.Repository{}).Bootstrap(ctx, store.DB, target); e != nil {
		t.Fatal(e)
	}
	if e = (configs.Repository{}).Ensure(ctx, store.DB, target); e != nil {
		t.Fatal(e)
	}
	file := filepath.Join(t.TempDir(), "access.json")
	t.Setenv("AFFILIATE_SITE_ACCESS_FILE", file)
	writeAccess := func(writable bool) {
		entry := map[string]any{"owner_tenant_id": tenant, "owner_app_id": "own", "subject_id": "local-operator", "targets": []map[string]string{{"tenant_id": tenant, "app_id": "target"}}}
		if writable {
			entry["writable_targets"] = entry["targets"]
		}
		raw, _ := json.Marshal([]any{entry})
		if e := os.WriteFile(file, raw, 0600); e != nil {
			t.Fatal(e)
		}
	}
	call := func(method, path, token string, body any, want int) map[string]any {
		t.Helper()
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest(method, path, bytes.NewReader(raw))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Idempotency-Key", newID())
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s got%d want%d %s", method, path, w.Code, want, w.Body)
		}
		var result map[string]any
		if e := json.Unmarshal(w.Body.Bytes(), &result); e != nil {
			t.Fatal(e)
		}
		if data, ok := result["data"].(map[string]any); ok {
			return data
		}
		return result
	}
	cfg := affiliateTestConfig()
	cfg["currency"] = "USD"
	cfg["extension"] = map[string]any{"disabled_entry": "HIDE", "editable_steps": true, "max_levels": 200, "max_steps": 200}
	body := map[string]any{"config": cfg, "expected_revision": 0, "reason": "验证跨站配置边界"}
	path := "/v1/affiliate/sites/" + tenant + "/target"
	writeAccess(false)
	call("GET", path+"/config", op, nil, 200)
	call("POST", path+"/config-requests", op, body, 403)
	call("GET", "/v1/affiliate/sites/unknown/target/config", op, nil, 403)
	writeAccess(true)
	call("POST", path+"/config-requests", op, body, 403)
	for _, permission := range []string{"affiliate:configure", "affiliate:manage"} {
		if _, e = store.DB.Exec(`INSERT INTO identity_grants VALUES(?,?,?,?)`, tenant, "target", "local-operator", permission); e != nil {
			t.Fatal(e)
		}
	}
	pending := call("POST", path+"/config-requests", op, body, 202)
	// 审批归属必须是目标站点，原站点不能代审批，读写清单也不能授予审批权限。
	call("POST", "/v1/approvals/"+pending["approval_id"].(string)+"/decisions", checker, map[string]any{"decision": "APPROVE", "expected_revision": 0, "object_hash": pending["object_hash"], "reason": "禁止原站审批"}, 404)
	var savedTenant, savedApp string
	if e = store.DB.QueryRow(`SELECT tenant_id,app_id FROM workflow_requests WHERE approval_id=?`, pending["approval_id"]).Scan(&savedTenant, &savedApp); e != nil || savedTenant != tenant || savedApp != "target" {
		t.Fatal(savedTenant, savedApp, e)
	}
	// 原本站配置申请仍走原端口并保留旧行为。
	call("POST", "/v1/affiliate/config-requests", op, body, 202)
	if _, e = store.DB.Exec(`DELETE FROM identity_grants WHERE tenant_id=? AND app_id='target' AND permission='affiliate:manage'`, tenant); e != nil {
		t.Fatal(e)
	}
	call("POST", path+"/config-requests", op, body, 403)
}
