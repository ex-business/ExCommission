package runtime

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	principal "github.com/ex-business/ExCommission/internal/identity/domain"
	milestone "github.com/ex-business/ExCommission/internal/milestones/domain"
	plan "github.com/ex-business/ExCommission/internal/plans/domain"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
)

func TestMilestoneHTTPAllConditionsOnceAndNativeLedger(t *testing.T) {
	if os.Getenv("TEST_MYSQL_ADDRESS") == "" {
		t.Skip("Docker MySQL required")
	}
	ctx := context.Background()
	store, err := mysql.Open(ctx, os.Getenv("TEST_MYSQL_ADDRESS"), "excommission", os.Getenv("MYSQL_PASSWORD"), "excommission")
	if err != nil {
		t.Fatal(err)
	}
	defer store.DB.Close()
	if err = store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	scope := foundation.Scope{Tenant: "test-milestone-" + newID(), App: "app"}
	defer func() {
		rows, e := store.DB.Query("SELECT TABLE_NAME FROM information_schema.columns WHERE TABLE_SCHEMA=DATABASE() AND COLUMN_NAME='tenant_id'")
		if e != nil {
			t.Error(e)
			return
		}
		var tables []string
		for rows.Next() {
			var table string
			_ = rows.Scan(&table)
			tables = append(tables, table)
		}
		rows.Close()
		for _, table := range tables {
			if _, e = store.DB.Exec("DELETE FROM "+table+" WHERE tenant_id=?", scope.Tenant); e != nil {
				t.Error(e)
			}
		}
	}()
	var clock atomic.Int64
	base := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	clock.Store(base.UnixNano())
	old := timeSource
	timeSource = func() time.Time { return time.Unix(0, clock.Load()).UTC() }
	defer func() { timeSource = old }()
	dir := t.TempDir()
	usersFile := filepath.Join(dir, "users.json")
	_ = os.WriteFile(usersFile, []byte(`[{"external_user_id":"parent","display_name":"代理"},{"external_user_id":"member1","display_name":"会员1"},{"external_user_id":"member2","display_name":"会员2"},{"external_user_id":"member3","display_name":"会员3"}]`), 0600)
	token, reviewer, sourceKey, paymentKey := strings.Repeat("o", 32), strings.Repeat("a", 32), strings.Repeat("s", 32), strings.Repeat("p", 32)
	for key, value := range map[string]string{"APP_MODE": "local", "LOCAL_OPERATOR_TOKEN": token, "LOCAL_APPROVER_TOKEN": reviewer, "LOCAL_CURSOR_KEY": strings.Repeat("c", 32), "LOCAL_SOURCE_KEY": sourceKey, "LOCAL_PAYMENT_KEY": paymentKey, "LOCAL_TENANT": scope.Tenant, "LOCAL_APP": scope.App, "LOCAL_USERS_FILE": usersFile, "LOCAL_GRADE_STATISTICS_FILE": "", "LOCAL_PAYMENT_RESULTS_FILE": "", "LOCAL_RECOVERY_EVIDENCE_FILE": "", "AUTH_LOCAL_ACCOUNTS_FILE": "", "AUTH_EXTERNAL_FILE": ""} {
		t.Setenv(key, value)
	}
	handler, err := businessHandler(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	client := server.Client()
	client.Timeout = 20 * time.Second
	var transcriptMu sync.Mutex
	transcript := []httpEvidence{}
	defer func() {
		if path := os.Getenv("MILESTONE_HTTP_EVIDENCE_FILE"); path != "" {
			body, _ := json.Marshal(transcript)
			if bytes.Contains(body, []byte(token)) || bytes.Contains(body, []byte(sourceKey)) {
				t.Error("evidence secrets")
				return
			}
			if e := os.WriteFile(path, body, 0600); e != nil {
				t.Error(e)
			}
		}
	}()
	invoke := func(method, template, path, role, key string, in any, signature string) (int, map[string]any, error) {
		var raw []byte
		if in != nil {
			raw, _ = json.Marshal(in)
		}
		r, e := http.NewRequest(method, server.URL+path, bytes.NewReader(raw))
		if e != nil {
			return 0, nil, e
		}
		r.Header.Set("Content-Type", "application/json")
		if role == "operator" {
			r.Header.Set("Authorization", "Bearer "+token)
		} else if role == "approver" {
			r.Header.Set("Authorization", "Bearer "+reviewer)
		}
		if method != "GET" {
			if key == "" {
				key = newID()
			}
			r.Header.Set("Idempotency-Key", key)
		}
		if signature != "" {
			actual := signature
			if signature == "source" || signature == "payment" {
				secret := sourceKey
				if signature == "payment" {
					secret = paymentKey
				}
				mac := hmac.New(sha256.New, []byte(secret))
				mac.Write(raw)
				actual = hex.EncodeToString(mac.Sum(nil))
			}
			name := "X-Source-Signature"
			if signature == "payment" {
				name = "X-Payment-Signature"
			}
			r.Header.Set(name, actual)
		}
		res, e := client.Do(r)
		if e != nil {
			return 0, nil, e
		}
		body, e := io.ReadAll(res.Body)
		res.Body.Close()
		if e != nil {
			return 0, nil, e
		}
		headerNames := []string{}
		for name := range r.Header {
			headerNames = append(headerNames, name)
		}
		transcriptMu.Lock()
		transcript = append(transcript, httpEvidence{Method: method, Template: template, URL: path, Role: role, HeaderNames: headerNames, Request: raw, Status: res.StatusCode, ContentType: res.Header.Get("Content-Type"), Response: body})
		transcriptMu.Unlock()
		var envelope map[string]any
		if e = json.Unmarshal(body, &envelope); e != nil {
			return res.StatusCode, nil, e
		}
		if data, ok := envelope["data"].(map[string]any); ok {
			return res.StatusCode, data, nil
		}
		return res.StatusCode, envelope, nil
	}
	call := func(method, template, path, role, key string, in any, sig string, want int) map[string]any {
		t.Helper()
		code, data, e := invoke(method, template, path, role, key, in, sig)
		if e != nil || code != want {
			t.Fatalf("%s %s got%d want%d err%v: %+v", method, path, code, want, e, data)
		}
		return data
	}
	approve := func(request map[string]any) {
		t.Helper()
		id := request["approval_id"].(string)
		a := call("GET", "/v1/approvals/{id}", "/v1/approvals/"+id, "operator", "", nil, "", 200)
		decision := map[string]any{"decision": "APPROVE", "expected_revision": a["revision"], "object_hash": a["object_hash"], "reason": "独立审批"}
		done := call("POST", "/v1/approvals/{id}/decisions", "/v1/approvals/"+id+"/decisions", "approver", "", decision, "", 200)
		if done["execution_status"] != "SUCCEEDED" {
			t.Fatal(done)
		}
	}
	call("POST", "/v1/user-sync-jobs", "/v1/user-sync-jobs", "operator", "", map[string]any{"source_id": "sandbox", "mapping_version_id": "1", "cursor": nil}, "", 202)
	ids := map[string]string{}
	for _, external := range []string{"parent", "member1", "member2", "member3"} {
		u := call("GET", "/v1/users/lookup", "/v1/users/lookup?source_id=sandbox&external_user_id="+external, "operator", "", nil, "", 200)
		ids[external] = u["user_id"].(string)
	}
	agent := call("POST", "/v1/agents", "/v1/agents", "operator", "", map[string]any{"user_id": ids["parent"], "display_name": "奖励代理"}, "", 200)["agent_id"].(string)
	fixture, err := os.ReadFile("../../conformance/v1/plan-percent.json")
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	_ = json.Unmarshal(fixture, &config)
	config["route"].(map[string]any)["event_type"] = "MILESTONE_REACHED"
	rule := config["rules"].([]any)[0].(map[string]any)
	rule["algorithm_id"] = "FIXED"
	rule["parameters"] = map[string]any{"amount_minor": "2500", "reward_currency": "USD"}
	rule["funding_limit_ppm"] = nil
	config["event_budget_minor"] = "2500"
	draft := call("POST", "/v1/plans", "/v1/plans", "operator", "", config, "", 200)
	effective := base.Add(time.Second)
	sim := call("POST", "/v1/simulations", "/v1/simulations", "operator", "", map[string]any{"draft_hash": draft["draft_hash"], "business_facts": map[string]any{"basis_amount": "1", "basis_currency": "USD", "product_code": "BTC-USDT", "scene": "SPOT", "confirmed": true, "business_time": effective}, "ancestors": []any{map[string]any{"agent_id": agent, "depth": 1, "grade_id": "G0", "eligible": true, "status": "ACTIVE"}}, "qualification_facts": map[string]any{agent: map[string]any{"eligibility": "ELIGIBLE", "effective_from": base, "effective_to": nil}}}, "", 200)
	approve(call("POST", "/v1/plans/{id}/publication-requests", "/v1/plans/P1/publication-requests", "operator", "", map[string]any{"expected_revision": 0, "draft_hash": draft["draft_hash"], "simulation_id": sim["simulation_id"], "effective_from": effective, "reason": "发布达标奖励方案"}, "", 200))
	clock.Store(effective.UnixNano())
	approve(call("POST", "/v1/grade-change-requests", "/v1/grade-change-requests", "operator", "", map[string]any{"agent_id": agent, "plan_id": "P1", "grade_id": "G0", "eligibility": "ELIGIBLE", "expected_revision": 0, "reason": "允许代理参与"}, "", 200))
	start := base.Add(2 * time.Second)
	end := base.Add(time.Hour)
	program := func(id string) milestone.Program {
		return milestone.Program{ID: id, Name: "可配置组合奖励", Source: "sandbox", Starts: start, Ends: end, Conditions: []milestone.Condition{{Metric: "CONFIRMED_DEPOSIT", Unit: "USD", Threshold: "100"}, {Metric: "VALID_TURNOVER", Unit: "USD", Threshold: "500"}}, Logic: "ALL", Frequency: "ONCE_PER_SUBJECT", PlanID: "P1"}
	}
	for _, id := range []string{"activity1", "activity2"} {
		p := program(id)
		if id == "activity2" {
			p.Conditions = []milestone.Condition{{Metric: "ITEM_COUNT", Unit: "COUNT", Threshold: "3"}}
		}
		request := call("POST", "/v1/milestone-program-requests", "/v1/milestone-program-requests", "operator", "program-"+id, map[string]any{"program": p, "reason": "配置两个累计条件"}, "", 202)
		call("GET", "/v1/milestone-programs/{id}", "/v1/milestone-programs/"+id, "operator", "", nil, "", 404)
		// Even with the domain permission granted, the maker may not approve their own program.
		if _, err = store.DB.Exec("INSERT INTO identity_grants VALUES(?,?,?,?)", scope.Tenant, scope.App, "local-operator", "milestones:approve"); err != nil {
			t.Fatal(err)
		}
		call("POST", "/v1/approvals/{id}/decisions", "/v1/approvals/"+request["approval_id"].(string)+"/decisions", "operator", "", map[string]any{"decision": "APPROVE", "expected_revision": 0, "object_hash": request["object_hash"], "reason": "禁止自审"}, "", 403)
		if _, err = store.DB.Exec("DELETE FROM identity_grants WHERE tenant_id=? AND app_id=? AND subject_id=? AND permission=?", scope.Tenant, scope.App, "local-operator", "milestones:approve"); err != nil {
			t.Fatal(err)
		}
		approve(request)
	}
	call("GET", "/v1/milestone-programs/{id}", "/v1/milestone-programs/activity1", "operator", "", nil, "", 200)
	clock.Store(base.Add(20 * time.Second).UnixNano())
	call("POST", "/v1/milestone-program-requests", "/v1/milestone-program-requests", "operator", "program-activity1", map[string]any{"program": program("activity1"), "reason": "配置两个累计条件"}, "", 202)
	obs := func(member, metric, value string, second int) milestone.Observation {
		return milestone.Observation{Source: "sandbox", EventID: newID(), ProgramID: "activity1", ExternalUser: member, Beneficiary: agent, Metric: metric, Unit: "USD", Value: value, At: start.Add(time.Duration(second) * time.Second), Evidence: "trusted-source-evidence"}
	}
	observe := func(o milestone.Observation, want int) map[string]any {
		t.Helper()
		return call("POST", "/v1/milestone-observations", "/v1/milestone-observations", "operator", "", o, "source", want)
	}
	a := obs("member1", "CONFIRMED_DEPOSIT", "100", 1)
	if d := observe(a, 202); d["status"] != "WAITING" || d["task_id"] != nil {
		t.Fatal(d)
	}
	progress := call("GET", "/v1/milestone-programs/{id}/members/{user_id}", "/v1/milestone-programs/activity1/members/"+ids["member1"], "operator", "", nil, "", 200)
	if progress["conditions"].([]any)[1].(map[string]any)["known"] != false {
		t.Fatal("missing fact treated known")
	}
	bad := obs("member1", "VALID_TURNOVER", "500", 2)
	call("POST", "/v1/milestone-observations", "/v1/milestone-observations", "operator", "", bad, "bad", 403)
	bad.Unit = "BTC"
	observe(bad, 400)
	bad.Unit = "USD"
	bad.At = end
	observe(bad, 400)
	bad.At = start.Add(-time.Second)
	observe(bad, 400)
	self := obs("parent", "CONFIRMED_DEPOSIT", "100", 1)
	observe(self, 403)
	// One semantic event ID cannot be reassigned, even with a different HTTP key.
	conflict := a
	conflict.Value = "101"
	observe(conflict, 409)
	reused := a
	reused.ExternalUser = "member2"
	observe(reused, 409)
	unknown := obs("member1", "CONFIRMED_DEPOSIT", "100", 1)
	unknown.Source = "untrusted"
	observe(unknown, 422)
	again := a
	again.Value = "100.00000000"
	observe(again, 202)
	below := obs("member1", "VALID_TURNOVER", "499.99999999", 2)
	if d := observe(below, 202); d["task_id"] != nil {
		t.Fatal("premature reward")
	}
	var wg sync.WaitGroup
	taskIDs := make(chan string, 100)
	for i := range 100 {
		wg.Go(func() {
			o := obs("member1", "VALID_TURNOVER", "500", 3)
			o.EventID = fmt.Sprintf("concurrent-%d", i)
			code, data, e := invoke("POST", "/v1/milestone-observations", "/v1/milestone-observations", "operator", newID(), o, "source")
			if e != nil || code != 202 {
				t.Errorf("concurrent status%d err%v %+v", code, e, data)
				return
			}
			id, ok := data["task_id"].(string)
			if !ok {
				t.Error("no task")
				return
			}
			taskIDs <- id
		})
	}
	wg.Wait()
	close(taskIDs)
	first := ""
	for id := range taskIDs {
		if first == "" {
			first = id
		}
		if id != first {
			t.Fatal("duplicate reward tasks")
		}
	}
	if first == "" {
		t.Fatal("no trigger")
	}
	// A later higher value or a stale lower value must retain the same one-time task.
	if d := observe(obs("member1", "VALID_TURNOVER", "800", 4), 202); d["task_id"] != first {
		t.Fatal(d)
	}
	if d := observe(obs("member1", "VALID_TURNOVER", "100", 2), 202); d["observation_status"] != "STALE_NOOP" || d["task_id"] != first {
		t.Fatal(d)
	}
	bad = obs("member1", "VALID_TURNOVER", "900", 4)
	observe(bad, 409)
	// Reverse arrival order and partial cumulative deposit.
	observe(obs("member2", "VALID_TURNOVER", "500", 1), 202)
	observe(obs("member2", "CONFIRMED_DEPOSIT", "50", 2), 202)
	if d := observe(obs("member2", "CONFIRMED_DEPOSIT", "100", 3), 202); d["task_id"] == nil {
		t.Fatal(d)
	}
	// A failure after inserting the native task rolls back task, progress and receipt together.
	observe(obs("member3", "CONFIRMED_DEPOSIT", "100", 1), 202)
	service := milestoneService(store)
	dispatch := service.Dispatch
	service.Dispatch = func(ctx context.Context, tx db.Executor, s foundation.Scope, p milestone.Program, m milestone.Progress, o milestone.Observation, v plan.Version, at time.Time) (string, error) {
		if _, e := dispatch(ctx, tx, s, p, m, o, v, at); e != nil {
			return "", e
		}
		return "", foundation.ErrValidation
	}
	rolled := obs("member3", "VALID_TURNOVER", "500", 3)
	failedResult, err := service.Observe(ctx, principal.Principal{Tenant: scope.Tenant, App: scope.App, Subject: "local-operator"}, rolled, newID(), newID())
	if err != nil || failedResult.Status != 400 {
		t.Fatal("unexpected failed command", failedResult.Status, err)
	}
	member, err := service.Repo.Member(ctx, store.DB, scope, "activity1", ids["member3"])
	if err != nil || member.TaskID != nil || len(member.Facts) != 1 {
		t.Fatal("rollback leaked", member, err)
	}
	observe(rolled, 202)
	// Same member in another independently approved activity earns once there too.
	oCount := obs("member1", "ITEM_COUNT", "3", 3)
	oCount.ProgramID = "activity2"
	oCount.Unit = "COUNT"
	observe(oCount, 202)

	// Ordinary external ingress cannot construct a reserved trigger.
	fake := map[string]any{"schema_version": "1", "source_id": "sandbox", "event_id": "forged", "business_id": "forged", "event_type": "MILESTONE_REACHED", "business_time": clockUTC(), "external_user_id": "member1", "product_code": "BTC-USDT", "scene": "SPOT", "source_revision": "1", "payload": map[string]any{"basis_amount": "1", "basis_currency": "USD", "beneficiary_agent_id": agent}}
	call("POST", "/v1/events", "/v1/events", "operator", "", fake, "source", 400)
	// Reconstructing workers simulates restart after the trigger transaction committed.
	for range 4 {
		if found, e := buildWorker(store).RunOnce(ctx, scope); e != nil || !found {
			t.Fatal(found, e)
		}
	}
	if found, e := buildWorker(store).RunOnce(ctx, scope); e != nil || found {
		t.Fatal("unexpected fifth task", found, e)
	}
	progress = call("GET", "/v1/milestone-programs/{id}/members/{user_id}", "/v1/milestone-programs/activity1/members/"+ids["member1"], "operator", "", nil, "", 200)
	if progress["task_status"] != "SUCCEEDED" {
		t.Fatal(progress)
	}
	snapview := call("GET", "/v1/calculation-tasks/{id}/adjustments", "/v1/calculation-tasks/"+first+"/adjustments", "operator", "", nil, "", 200)
	trigger := snapview["effective_snapshot"].(map[string]any)["trigger_evidence"].(map[string]any)
	if trigger["approval_id"] == nil || trigger["config_hash"] == nil {
		t.Fatal("missing approved trigger config")
	}
	task, err := taskRepository(store).Get(ctx, store.DB, scope, first)
	if err != nil || !bytes.Contains(task.Snapshot, []byte("CONFIRMED_DEPOSIT")) || !bytes.Contains(task.Snapshot, []byte("MILESTONE_BINDING")) {
		t.Fatal("missing frozen trigger proof", err, string(task.Snapshot))
	}
	var count int
	var total string
	if err = store.DB.QueryRow("SELECT COUNT(*),CAST(SUM(pending) AS CHAR) FROM ledger_entitlements WHERE tenant_id=?", scope.Tenant).Scan(&count, &total); err != nil || count != 4 || total != "10000" {
		t.Fatal("ledger not exactly once", count, total, err)
	}
	// Settle and pay through existing independent approval and signed sandbox receipt.
	clock.Store(base.Add(24 * time.Hour).UnixNano())
	run := call("POST", "/v1/statement-runs", "/v1/statement-runs", "operator", "", map[string]any{"period": base.Format("2006-01-02"), "calendar_version": "UTC-v1"}, "", 202)
	statement := run["statements"].([]any)[0].(map[string]any)
	sid := statement["statement_id"].(string)
	approve(call("POST", "/v1/statements/{id}/submission", "/v1/statements/"+sid+"/submission", "operator", "", map[string]any{"expected_revision": statement["revision"], "content_hash": statement["content_hash"]}, "", 202))
	statement = call("GET", "/v1/statements/{id}", "/v1/statements/"+sid, "operator", "", nil, "", 200)
	payment := call("POST", "/v1/payments", "/v1/payments", "operator", "", map[string]any{"statement_id": sid, "amount": map[string]string{"currency": "USD", "minor_units": "10000"}, "channel_id": "sandbox", "expected_statement_revision": statement["revision"]}, "", 202)
	paid := call("GET", "/v1/payments/{id}", "/v1/payments/"+payment["payment_id"].(string), "operator", "", nil, "", 200)
	if paid["status"] != "UNKNOWN" {
		t.Fatal("sandbox pretended paid", paid)
	}
	callback := map[string]any{"callback_id": "milestone-paid", "merchant_payment_id": paid["merchant_payment_id"], "currency": "USD", "minor_units": "10000", "status": "SUCCEEDED", "receipt_ref": nil}
	for range 2 {
		call("POST", "/v1/payment-callbacks/{channel_id}", "/v1/payment-callbacks/sandbox", "none", "", callback, "payment", 200)
	}
	var pending, available, reserved, paidTotal string
	if err = store.DB.QueryRow("SELECT CAST(SUM(pending) AS CHAR),CAST(SUM(available) AS CHAR),CAST(SUM(reserved) AS CHAR),CAST(SUM(paid) AS CHAR) FROM ledger_entitlements WHERE tenant_id=?", scope.Tenant).Scan(&pending, &available, &reserved, &paidTotal); err != nil || pending != "0" || available != "0" || reserved != "0" || paidTotal != "10000" {
		t.Fatal(pending, available, reserved, paidTotal, err)
	}
	late := obs("member1", "VALID_TURNOVER", "1000", 5)
	if v := observe(late, 202); v["task_id"] != first {
		t.Fatal("late duplicate trigger", v)
	}
	outside := obs("member1", "VALID_TURNOVER", "1100", 6)
	outside.At = end
	observe(outside, 400)
	if found, e := buildWorker(store).RunOnce(ctx, scope); e != nil || found {
		t.Fatal("late replay created task", found, e)
	}
	t.Log("generic ALL conditions, cumulative snapshots, concurrent100 once, rollback, native ledger and independently approved sandbox payment PASS")
}
