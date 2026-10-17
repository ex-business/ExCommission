package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func affiliateTestConfig() map[string]any {
	return map[string]any{"enabled": true, "currency": "MXN", "currency_scale": 2, "timezone": "UTC", "grade_period": "WEEK", "grade_condition": map[string]any{"deposit_minor": "5000", "valid_bet_minor": "10000"}, "referral_condition": map[string]any{"deposit_minor": "5000", "valid_bet_minor": "30000"}, "invitee_condition": map[string]any{"deposit_minor": "10000", "valid_bet_minor": "50000"}, "invitee_reward_minor": "5000", "levels": []any{map[string]any{"level": 0, "qualified_members": 0, "daily_valid_bet_minor": "0", "rates_ppm": []int{2500, 1500, 500}}, map[string]any{"level": 1, "qualified_members": 2, "daily_valid_bet_minor": "50000", "rates_ppm": []int{4000, 2500, 1500}}}, "steps": []any{map[string]any{"step": 1, "additional_members": 1, "reward_minor": "5000"}, map[string]any{"step": 2, "additional_members": 3, "reward_minor": "8000"}}, "venue_type_ids": []string{"SPORTS", "LIVE"}}
}
func TestAffiliateConfigurationHTTPApprovalAndHistory(t *testing.T) {
	if os.Getenv("TEST_MYSQL_ADDRESS") == "" {
		t.Skip("MySQL required")
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
	tenant := "test-affiliate-" + newID()
	defer func() {
		rows, e := store.DB.Query(`SELECT DISTINCT TABLE_NAME FROM information_schema.columns WHERE TABLE_SCHEMA=DATABASE() AND COLUMN_NAME='tenant_id'`)
		if e != nil {
			t.Error(e)
			return
		}
		tables := []string{}
		for rows.Next() {
			var v string
			rows.Scan(&v)
			tables = append(tables, v)
		}
		rows.Close()
		for _, v := range tables {
			if _, e = store.DB.Exec("DELETE FROM `"+v+"` WHERE tenant_id=?", tenant); e != nil {
				t.Error(e)
			}
		}
	}()
	for k, v := range map[string]string{"APP_MODE": "local", "LOCAL_OPERATOR_TOKEN": strings.Repeat("o", 32), "LOCAL_APPROVER_TOKEN": strings.Repeat("a", 32), "LOCAL_CURSOR_KEY": strings.Repeat("c", 32), "LOCAL_SOURCE_KEY": strings.Repeat("s", 32), "LOCAL_PAYMENT_KEY": strings.Repeat("p", 32), "LOCAL_TENANT": tenant, "LOCAL_APP": "app", "AUTH_LOCAL_ACCOUNTS_FILE": "", "AUTH_EXTERNAL_FILE": ""} {
		t.Setenv(k, v)
	}
	var now = time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	previous := timeSource
	timeSource = func() time.Time { return now }
	defer func() { timeSource = previous }()
	handler, e := businessHandler(ctx, store)
	if e != nil {
		t.Fatal(e)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	transcript := []httpEvidence{}
	defer func() {
		if path := os.Getenv("AFFILIATE_HTTP_EVIDENCE_FILE"); path != "" {
			b, _ := json.Marshal(transcript)
			if e := os.WriteFile(path, b, 0600); e != nil {
				t.Error(e)
			}
		}
	}()
	call := func(method, path, token string, in any, want int, key ...string) map[string]any {
		t.Helper()
		var b []byte
		if in != nil {
			b, _ = json.Marshal(in)
		}
		r, _ := http.NewRequest(method, server.URL+path, bytes.NewReader(b))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		id := newID()
		if len(key) > 0 {
			id = key[0]
		}
		r.Header.Set("Idempotency-Key", id)
		res, e := server.Client().Do(r)
		if e != nil {
			t.Fatal(e)
		}
		raw, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != want {
			t.Fatalf("%s %s status=%d want%d %s", method, path, res.StatusCode, want, raw)
		}
		template := path
		if strings.HasPrefix(path, "/v1/approvals/") {
			template = "/v1/approvals/{id}/decisions"
		}
		if strings.HasPrefix(path, "/v1/affiliate/config-requests/") {
			template = "/v1/affiliate/config-requests/{id}"
		}
		transcript = append(transcript, httpEvidence{Method: method, Template: template, URL: path, Request: b, Response: raw, Status: res.StatusCode, ContentType: res.Header.Get("Content-Type")})
		var out struct {
			Data map[string]any `json:"data"`
		}
		d := json.NewDecoder(bytes.NewReader(raw))
		d.UseNumber()
		if e = d.Decode(&out); e != nil {
			t.Fatal(e)
		}
		return out.Data
	}
	op, checker := strings.Repeat("o", 32), strings.Repeat("a", 32)
	current := call("GET", "/v1/affiliate/config", op, nil, 200)
	if current["configured"] != false || current["current"] != nil {
		t.Fatal(current)
	}
	venues := call("GET", "/v1/affiliate/venue-types", checker, nil, 200)
	if len(venues["rows"].([]any)) != 7 {
		t.Fatal(venues)
	}
	in := map[string]any{"config": affiliateTestConfig(), "expected_revision": 0, "reason": "配置推荐返佣业务"}
	call("POST", "/v1/affiliate/config-requests", checker, in, 403)
	c := in["config"].(map[string]any)
	delete(c, "enabled")
	call("POST", "/v1/affiliate/config-requests", op, in, 400)
	c["enabled"] = true
	levels := c["levels"].([]any)
	delete(levels[0].(map[string]any), "level")
	call("POST", "/v1/affiliate/config-requests", op, in, 400)
	levels[0].(map[string]any)["level"] = 0
	levels[0].(map[string]any)["rates_ppm"] = []int{2500}
	call("POST", "/v1/affiliate/config-requests", op, in, 400)
	levels[0].(map[string]any)["rates_ppm"] = []int{2500, 1500, 500}
	c["venue_type_ids"] = []string{"BTC-USDT"}
	call("POST", "/v1/affiliate/config-requests", op, in, 422)
	c["venue_type_ids"] = []string{"SPORTS", "LIVE"}
	pending := call("POST", "/v1/affiliate/config-requests", op, in, 202, "create-profile")
	retry := call("POST", "/v1/affiliate/config-requests", op, in, 202, "create-profile")
	if pending["approval_id"] != retry["approval_id"] {
		t.Fatal("request replay changed resource")
	}
	c["invitee_reward_minor"] = "6000"
	call("POST", "/v1/affiliate/config-requests", op, in, 409, "create-profile")
	c["invitee_reward_minor"] = "5000"
	preview := call("GET", "/v1/affiliate/config-requests/"+pending["approval_id"].(string), checker, nil, 200)
	if preview["change"].(map[string]any)["config"].(map[string]any)["invitee_reward_minor"] != "5000" {
		t.Fatal("reviewer cannot inspect frozen config", preview)
	}
	decision := map[string]any{"decision": "APPROVE", "expected_revision": 0, "object_hash": pending["object_hash"], "reason": "复核三类奖励及等级配置"}
	path := "/v1/approvals/" + pending["approval_id"].(string) + "/decisions"
	call("POST", path, op, decision, 403)
	approved := call("POST", path, checker, decision, 200)
	if approved["execution_status"] != "SUCCEEDED" {
		t.Fatal(approved)
	}
	current = call("GET", "/v1/affiliate/config", op, nil, 200)
	if current["configured"] != true || current["revision"].(json.Number).String() != "1" || current["scheduled"] != nil {
		t.Fatal(current)
	}
	old := current["current"].(map[string]any)
	if old["config"].(map[string]any)["invitee_reward_minor"] != "5000" {
		t.Fatal(old)
	}
	in["expected_revision"] = 1
	c["grade_period"] = "MONTH"
	call("POST", "/v1/affiliate/config-requests", op, in, 409)
	c["grade_period"] = "WEEK"
	c["invitee_reward_minor"] = "6000"
	pending = call("POST", "/v1/affiliate/config-requests", op, in, 202)
	decision["object_hash"] = pending["object_hash"]
	path = "/v1/approvals/" + pending["approval_id"].(string) + "/decisions"
	approved = call("POST", path, checker, decision, 200)
	if approved["execution_status"] != "SUCCEEDED" {
		t.Fatal(approved)
	}
	current = call("GET", "/v1/affiliate/config", op, nil, 200)
	if current["current"].(map[string]any)["config"].(map[string]any)["invitee_reward_minor"] != "5000" || current["scheduled"].(map[string]any)["config"].(map[string]any)["invitee_reward_minor"] != "6000" {
		t.Fatal("scheduled config changed current day", current)
	}
	in["expected_revision"] = 2
	call("POST", "/v1/affiliate/config-requests", op, in, 409)
	now = now.Add(24 * time.Hour)
	current = call("GET", "/v1/affiliate/config", op, nil, 200)
	if current["scheduled"] != nil || current["current"].(map[string]any)["revision"].(json.Number).String() != "2" {
		t.Fatal(current)
	}
	// Remove permission and reinstall the feature: revocation must survive bootstrap.
	if _, e = store.DB.Exec(`DELETE FROM identity_grants WHERE tenant_id=? AND app_id='app' AND subject_id='local-operator' AND permission='affiliate:configure'`, tenant); e != nil {
		t.Fatal(e)
	}
	if _, e = businessHandler(ctx, store); e != nil {
		t.Fatal(e)
	}
	call("POST", "/v1/affiliate/config-requests", op, in, 403)
}
