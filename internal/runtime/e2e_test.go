package runtime

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type httpEvidence struct {
	Method      string          `json:"method"`
	Template    string          `json:"path_template"`
	URL         string          `json:"url"`
	Role        string          `json:"role"`
	HeaderNames []string        `json:"header_names"`
	Request     json.RawMessage `json:"request"`
	Status      int             `json:"status"`
	ContentType string          `json:"content_type"`
	Response    json.RawMessage `json:"response,omitempty"`
	Text        string          `json:"text,omitempty"`
}

func TestCompleteHTTPCommissionLifecycleAndContracts(t *testing.T) {
	if os.Getenv("TEST_MYSQL_ADDRESS") == "" {
		t.Skip("Docker MySQL required")
	}
	ctx := context.Background()
	store, err := mysql.Open(ctx, os.Getenv("TEST_MYSQL_ADDRESS"), "excommission", os.Getenv("MYSQL_PASSWORD"), "excommission")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.DB.Close() })
	if err = store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	scope := foundation.Scope{Tenant: "test-e2e-" + newID(), App: "app"}
	t.Cleanup(func() {
		rows, err := store.DB.Query(`SELECT TABLE_NAME FROM information_schema.columns WHERE TABLE_SCHEMA=DATABASE() AND COLUMN_NAME='tenant_id' ORDER BY TABLE_NAME`)
		if err != nil {
			t.Error(err)
			return
		}
		tables := []string{}
		for rows.Next() {
			var table string
			if err = rows.Scan(&table); err != nil {
				t.Error(err)
				rows.Close()
				return
			}
			if !regexp.MustCompile(`^[a-z_]+$`).MatchString(table) {
				t.Error("invalid table")
				rows.Close()
				return
			}
			tables = append(tables, table)
		}
		rows.Close()
		for _, table := range tables {
			if _, err = store.DB.Exec("DELETE FROM `"+table+"` WHERE tenant_id=?", scope.Tenant); err != nil {
				t.Error(err)
			}
		}
	})
	// The clock can only be injected by this in-process test, never by an HTTP client.
	var clock atomic.Int64
	clock.Store(time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC).UnixNano())
	oldClock := timeSource
	timeSource = func() time.Time { return time.Unix(0, clock.Load()).UTC() }
	defer func() { timeSource = oldClock }()
	nextDay := func() { clock.Store(clockUTC().Truncate(24 * time.Hour).Add(24*time.Hour + time.Second).UnixNano()) }
	directory := t.TempDir()
	usersFile := filepath.Join(directory, "users.json")
	evidenceFile := filepath.Join(directory, "cash.json")
	resultsFile := filepath.Join(directory, "payments.json")
	statsFile := filepath.Join(directory, "statistics.json")
	os.WriteFile(statsFile, []byte(`{}`), 0600)
	os.WriteFile(usersFile, []byte(`[{"external_user_id":"trader","display_name":"闭环交易用户"},{"external_user_id":"parent","display_name":"闭环直属代理"}]`), 0600)
	os.WriteFile(evidenceFile, []byte(`{}`), 0600)
	os.WriteFile(resultsFile, []byte(`{"queries":{},"reconciliations":{}}`), 0600)
	token, checker := strings.Repeat("o", 32), strings.Repeat("a", 32)
	sourceKey, paymentKey := strings.Repeat("s", 32), strings.Repeat("p", 32)
	for key, value := range map[string]string{"APP_MODE": "local", "LOCAL_OPERATOR_TOKEN": token, "LOCAL_APPROVER_TOKEN": checker, "LOCAL_CURSOR_KEY": strings.Repeat("c", 32), "LOCAL_SOURCE_KEY": sourceKey, "LOCAL_PAYMENT_KEY": paymentKey, "LOCAL_TENANT": scope.Tenant, "LOCAL_APP": scope.App, "LOCAL_USERS_FILE": usersFile, "LOCAL_GRADE_STATISTICS_FILE": statsFile, "LOCAL_PAYMENT_RESULTS_FILE": resultsFile, "LOCAL_RECOVERY_EVIDENCE_FILE": evidenceFile} {
		t.Setenv(key, value)
	}
	handler, err := businessHandler(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	client := server.Client()
	client.Timeout = 15 * time.Second
	transcript := []httpEvidence{}
	t.Cleanup(func() {
		path := os.Getenv("HTTP_EVIDENCE_FILE")
		if path == "" {
			return
		}
		body, err := json.MarshalIndent(transcript, "", "  ")
		if err != nil {
			t.Error(err)
			return
		}
		if bytes.Contains(body, []byte(token)) || bytes.Contains(body, []byte(checker)) || bytes.Contains(body, []byte(sourceKey)) || bytes.Contains(body, []byte(paymentKey)) {
			t.Error("credentials entered transcript")
			return
		}
		if err = os.WriteFile(path, append(body, '\n'), 0600); err != nil {
			t.Error(err)
		}
	})
	callRaw := func(method, template, path, role, key string, raw []byte, headers map[string]string, want int) map[string]any {
		t.Helper()
		clock.Add(1000)
		r, err := http.NewRequest(method, server.URL+path, bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Content-Type", "application/json")
		if role == "operator" {
			r.Header.Set("Authorization", "Bearer "+token)
		} else if role == "approver" {
			r.Header.Set("Authorization", "Bearer "+checker)
		}
		if method != "GET" {
			if key == "" {
				key = newID()
			}
			r.Header.Set("Idempotency-Key", key)
		}
		for k, v := range headers {
			r.Header.Set(k, v)
		}
		response, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		entry := httpEvidence{Method: method, Template: template, URL: path, Role: role, Request: json.RawMessage(raw), Status: response.StatusCode, ContentType: response.Header.Get("Content-Type"), HeaderNames: []string{}}
		for name := range r.Header {
			entry.HeaderNames = append(entry.HeaderNames, name)
		}
		if strings.HasPrefix(entry.ContentType, "application/json") {
			entry.Response = body
		} else {
			entry.Text = string(body)
		}
		transcript = append(transcript, entry)
		if response.StatusCode != want {
			t.Fatalf("%s %s status %d want %d: %s", method, path, response.StatusCode, want, body)
		}
		if !strings.HasPrefix(entry.ContentType, "application/json") {
			return map[string]any{"text": string(body)}
		}
		var answer struct {
			Data map[string]any `json:"data"`
		}
		decoder := json.NewDecoder(bytes.NewReader(body))
		decoder.UseNumber()
		if err = decoder.Decode(&answer); err != nil {
			t.Fatal(err)
		}
		return answer.Data
	}
	call := func(method, template, path, role string, input any, want int) map[string]any {
		t.Helper()
		var raw []byte
		if input != nil {
			raw, _ = json.Marshal(input)
		}
		return callRaw(method, template, path, role, "", raw, nil, want)
	}
	field := func(data map[string]any, name string) string {
		t.Helper()
		value, ok := data[name].(string)
		if !ok {
			t.Fatalf("missing string %s: %+v", name, data)
		}
		return value
	}
	rev := func(data map[string]any) uint64 {
		t.Helper()
		n, ok := data["revision"].(json.Number)
		if !ok {
			t.Fatal("missing revision", data)
		}
		v, err := strconv.ParseUint(n.String(), 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	approve := func(request map[string]any) {
		t.Helper()
		id := field(request, "approval_id")
		a := call("GET", "/v1/approvals/{id}", "/v1/approvals/"+id, "operator", nil, 200)
		decision := map[string]any{"decision": "APPROVE", "expected_revision": rev(a), "object_hash": a["object_hash"], "reason": "独立复核"}
		call("POST", "/v1/approvals/{id}/decisions", "/v1/approvals/"+id+"/decisions", "operator", decision, 403)
		done := call("POST", "/v1/approvals/{id}/decisions", "/v1/approvals/"+id+"/decisions", "approver", decision, 200)
		if done["execution_status"] != "SUCCEEDED" {
			t.Fatal("approval failed", done)
		}
	}
	sign := func(raw []byte, key string) string {
		mac := hmac.New(sha256.New, []byte(key))
		mac.Write(raw)
		return hex.EncodeToString(mac.Sum(nil))
	}
	call("GET", "/v1/reports/{kind}", "/v1/reports/earnings", "none", nil, 401)
	synced := call("POST", "/v1/user-sync-jobs", "/v1/user-sync-jobs", "operator", map[string]any{"source_id": "sandbox", "mapping_version_id": "1", "cursor": nil}, 202)
	call("GET", "/v1/user-sync-jobs/{id}", "/v1/user-sync-jobs/"+field(synced, "job_id"), "operator", nil, 200)
	agents := map[string]string{}
	for _, external := range []string{"trader", "parent"} {
		u := call("GET", "/v1/users/lookup", "/v1/users/lookup?source_id=sandbox&external_user_id="+external, "operator", nil, 200)
		a := call("POST", "/v1/agents", "/v1/agents", "operator", map[string]any{"user_id": u["user_id"], "display_name": external}, 200)
		agents[external] = field(a, "agent_id")
	}
	call("GET", "/v1/agents/{id}", "/v1/agents/"+agents["parent"], "operator", nil, 200)
	move := call("POST", "/v1/relationship-change-requests", "/v1/relationship-change-requests", "operator", map[string]any{"agent_id": agents["trader"], "new_parent_id": agents["parent"], "expected_revision": 0, "reason": "建立直属关系"}, 200)
	approve(move)
	call("GET", "/v1/agents/{id}/ancestors", "/v1/agents/"+agents["trader"]+"/ancestors?at="+url.QueryEscape(clockUTC().Format(time.RFC3339Nano)), "operator", nil, 200)
	fixture, err := os.ReadFile("../../conformance/v1/plan-percent.json")
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	decoder := json.NewDecoder(bytes.NewReader(fixture))
	decoder.UseNumber()
	decoder.Decode(&config)
	draft := call("POST", "/v1/plans", "/v1/plans", "operator", config, 200)
	call("GET", "/v1/plans/{id}/draft", "/v1/plans/P1/draft", "operator", nil, 200)
	effective := clockUTC().Add(time.Second)
	simulation := call("POST", "/v1/simulations", "/v1/simulations", "operator", map[string]any{"draft_hash": draft["draft_hash"], "business_facts": map[string]any{"basis_amount": "1000", "basis_currency": "USD", "product_code": "BTC-USDT", "scene": "SPOT", "confirmed": true, "business_time": effective.Add(time.Second)}, "ancestors": []any{map[string]any{"agent_id": agents["parent"], "depth": 1, "grade_id": "G0", "eligible": true, "status": "ACTIVE"}}, "qualification_facts": map[string]any{agents["parent"]: map[string]any{"eligibility": "ELIGIBLE", "effective_from": clockUTC(), "effective_to": nil}}}, 200)
	publish := call("POST", "/v1/plans/{id}/publication-requests", "/v1/plans/P1/publication-requests", "operator", map[string]any{"expected_revision": 0, "draft_hash": draft["draft_hash"], "simulation_id": simulation["simulation_id"], "effective_from": effective, "reason": "发布首版方案"}, 200)
	approve(publish)
	clock.Store(effective.Add(time.Second).UnixNano())
	versions := call("GET", "/v1/plans/{id}/versions", "/v1/plans/P1/versions", "operator", nil, 200)
	versionID := versions["items"].([]any)[0].(map[string]any)["version_id"].(string)
	call("GET", "/v1/plans/{id}/versions/{version_id}", "/v1/plans/P1/versions/"+versionID, "operator", nil, 200)
	grade := call("POST", "/v1/grade-change-requests", "/v1/grade-change-requests", "operator", map[string]any{"agent_id": agents["parent"], "plan_id": "P1", "grade_id": "G0", "eligibility": "ELIGIBLE", "expected_revision": 0, "reason": "确认参与资格"}, 200)
	approve(grade)
	eventAt := clockUTC().Add(time.Microsecond)
	eventBody := map[string]any{"schema_version": "1", "source_id": "sandbox", "event_id": "original", "business_id": "order", "event_type": "FEE_CONFIRMED", "business_time": eventAt, "external_user_id": "trader", "product_code": "BTC-USDT", "scene": "SPOT", "source_revision": "1", "payload": map[string]any{"basis_amount": "1000", "basis_currency": "USD", "confirmed": true}}
	raw, _ := json.Marshal(eventBody)
	accepted := callRaw("POST", "/v1/events", "/v1/events", "operator", "original-event", raw, map[string]string{"X-Source-Signature": sign(raw, sourceKey)}, 202)
	taskID := field(accepted, "task_id")
	alias := map[string]any{}
	for k, v := range eventBody {
		alias[k] = v
	}
	alias["event_id"] = "alias"
	aliasRaw, _ := json.Marshal(alias)
	again := callRaw("POST", "/v1/events", "/v1/events", "operator", "alias-event", aliasRaw, map[string]string{"X-Source-Signature": sign(aliasRaw, sourceKey)}, 202)
	if again["task_id"] != taskID {
		t.Fatal("business alias duplicated task")
	}
	call("GET", "/v1/calculation-tasks/{id}/adjustments", "/v1/calculation-tasks/"+taskID+"/adjustments", "operator", nil, 409)
	if done, err := buildWorker(store).RunOnce(ctx, scope); err != nil || !done {
		t.Fatal(err)
	}
	task := call("GET", "/v1/calculation-tasks/{id}", "/v1/calculation-tasks/"+taskID, "operator", nil, 200)
	if task["status"] != "SUCCEEDED" {
		t.Fatal(task)
	}
	snapshotHash := task["snapshot_hash"]
	call("GET", "/v1/calculation-tasks/{id}/trace", "/v1/calculation-tasks/"+taskID+"/trace", "operator", nil, 200)
	call("GET", "/v1/events/{id}", "/v1/events/"+field(accepted, "event_id"), "operator", nil, 200)
	earnings := call("GET", "/v1/reports/{kind}", "/v1/reports/earnings?agent_id="+agents["parent"], "operator", nil, 200)
	entitlementID := earnings["rows"].([]any)[0].(map[string]any)["entitlement_id"].(string)
	settle := func(period string) map[string]any {
		t.Helper()
		run := call("POST", "/v1/statement-runs", "/v1/statement-runs", "operator", map[string]any{"period": period, "calendar_version": "UTC-v1"}, 202)
		statements := run["statements"].([]any)
		if len(statements) != 1 {
			t.Fatal("unexpected statement batch", run)
		}
		statement := statements[0].(map[string]any)
		id := field(statement, "statement_id")
		request := call("POST", "/v1/statements/{id}/submission", "/v1/statements/"+id+"/submission", "operator", map[string]any{"expected_revision": rev(statement), "content_hash": statement["content_hash"]}, 202)
		approve(request)
		statement = call("GET", "/v1/statements/{id}", "/v1/statements/"+id, "operator", nil, 200)
		if statement["approval_valid"] != true {
			t.Fatal("new statement approval invalid", statement)
		}
		return statement
	}
	nextDay()
	statement := settle(eventAt.Format("2006-01-02"))
	createPayment := func(statement map[string]any, amount string) map[string]any {
		t.Helper()
		created := call("POST", "/v1/payments", "/v1/payments", "operator", map[string]any{"statement_id": statement["statement_id"], "amount": map[string]string{"currency": "USD", "minor_units": amount}, "channel_id": "sandbox", "expected_statement_revision": rev(statement)}, 202)
		payment := call("GET", "/v1/payments/{id}", "/v1/payments/"+field(created, "payment_id"), "operator", nil, 200)
		if payment["status"] != "UNKNOWN" {
			t.Fatal("sandbox asserted delivery", payment)
		}
		return payment
	}
	payment := createPayment(statement, "6000")
	call("POST", "/v1/payments/{id}/verification", "/v1/payments/"+field(payment, "payment_id")+"/verification", "operator", map[string]any{}, 200)
	refundRaw, _ := json.Marshal(map[string]any{"source_id": "sandbox", "refund_id": "refund", "original_business_id": "order", "original_event_type": "FEE_CONFIRMED", "refund_basis_total": "800", "currency": "USD", "business_time": clockUTC(), "reason": "确认累计退款"})
	refunded := callRaw("POST", "/v1/refunds", "/v1/refunds", "operator", "refund", refundRaw, map[string]string{"X-Source-Signature": sign(refundRaw, sourceKey)}, 202)
	if err = refundService(store).Recover(ctx, scope); err != nil {
		t.Fatal(err)
	}
	waiting := call("GET", "/v1/refunds/{id}", "/v1/refunds/"+field(refunded, "refund_task_id"), "operator", nil, 200)
	if waiting["status"] != "WAITING_PAYMENT" {
		t.Fatal(waiting)
	}
	held := call("GET", "/v1/entitlements/{id}", "/v1/entitlements/"+entitlementID, "operator", nil, 200)
	if held["reserved_minor"] != "6000" || held["payment_hold"] != true {
		t.Fatal("unknown refund released money", held)
	}
	callback := func(p map[string]any, id, status string) {
		t.Helper()
		amount := p["amount"].(map[string]any)
		body, _ := json.Marshal(map[string]any{"callback_id": id, "merchant_payment_id": p["merchant_payment_id"], "currency": amount["currency"], "minor_units": amount["minor_units"], "status": status, "receipt_ref": nil})
		callRaw("POST", "/v1/payment-callbacks/{channel_id}", "/v1/payment-callbacks/sandbox", "none", "", body, map[string]string{"X-Payment-Signature": sign(body, paymentKey)}, 200)
	}
	callback(payment, "success", "SUCCEEDED")
	callback(payment, "success", "SUCCEEDED")
	if err = refundService(store).Recover(ctx, scope); err != nil {
		t.Fatal(err)
	}
	money := call("GET", "/v1/debts/{id}", "/v1/debts/"+entitlementID, "operator", nil, 200)
	if money["target_minor"] != "2000" || money["debt_minor"] != "4000" || money["paid_minor"] != "6000" {
		t.Fatal("refund debt conservation", money)
	}
	oldStatement := call("GET", "/v1/statements/{id}", "/v1/statements/"+field(statement, "statement_id"), "operator", nil, 200)
	if oldStatement["approval_valid"] != false {
		t.Fatal("refund retained old approval", oldStatement)
	}
	futureAt := clockUTC()
	eventBody["event_id"] = "future"
	eventBody["business_id"] = "future-order"
	eventBody["business_time"] = futureAt
	raw, _ = json.Marshal(eventBody)
	callRaw("POST", "/v1/events", "/v1/events", "operator", "future-event", raw, map[string]string{"X-Source-Signature": sign(raw, sourceKey)}, 202)
	if done, err := buildWorker(store).RunOnce(ctx, scope); err != nil || !done {
		t.Fatal(err)
	}
	nextDay()
	futureStatement := settle(futureAt.Format("2006-01-02"))
	recovery := call("POST", "/v1/recovery-requests", "/v1/recovery-requests", "operator", map[string]any{"debt_id": entitlementID, "method": "FUTURE_OFFSET", "amount": map[string]string{"currency": "USD", "minor_units": "3000"}, "evidence_ref": "offset-proof", "reason": "独立授权抵债", "expected_revision": rev(money)}, 202)
	approve(recovery)
	money = call("GET", "/v1/debts/{id}", "/v1/debts/"+entitlementID, "operator", nil, 200)
	if money["debt_minor"] != "1000" || money["recovered_minor"] != "3000" {
		t.Fatal(money)
	}
	futureStatement = call("GET", "/v1/statements/{id}", "/v1/statements/"+field(futureStatement, "statement_id"), "operator", nil, 200)
	if futureStatement["status"] != "PARTIALLY_PAID" {
		t.Fatal("noncash settlement progress missing", futureStatement)
	}
	proof, _ := json.Marshal(map[string]any{"cash-proof": map[string]any{"tenant_id": scope.Tenant, "app_id": scope.App, "agent_id": agents["parent"], "currency": "USD", "minor_units": "1000", "confirmed": true}})
	os.WriteFile(evidenceFile, proof, 0600)
	cash := call("POST", "/v1/recovery-requests", "/v1/recovery-requests", "operator", map[string]any{"debt_id": entitlementID, "method": "CASH_CONFIRMED", "amount": map[string]string{"currency": "USD", "minor_units": "1000"}, "evidence_ref": "cash-proof", "reason": "确认现金追回", "expected_revision": rev(money)}, 202)
	approve(cash)
	effectiveSnapshot := call("GET", "/v1/calculation-tasks/{id}/adjustments", "/v1/calculation-tasks/"+taskID+"/adjustments", "operator", nil, 200)
	facts := effectiveSnapshot["effective_snapshot"].(map[string]any)["facts"].(map[string]any)
	facts["basis_amount"] = "1200"
	adjusted := call("POST", "/v1/adjustment-requests", "/v1/adjustment-requests", "operator", map[string]any{"original_task_id": taskID, "corrected_facts": facts, "expected_revision": rev(effectiveSnapshot), "reason": "来源事实更正", "evidence_ref": "corrected-source-proof"}, 202)
	approve(adjusted)
	task = call("GET", "/v1/calculation-tasks/{id}", "/v1/calculation-tasks/"+taskID, "operator", nil, 200)
	if task["snapshot_hash"] != snapshotHash {
		t.Fatal("correction rewrote original snapshot")
	}
	money = call("GET", "/v1/entitlements/{id}", "/v1/entitlements/"+entitlementID, "operator", nil, 200)
	if money["target_minor"] != "4000" || money["pending_minor"] != "2000" || money["recovered_minor"] != "4000" {
		t.Fatal("adjustment conservation", money)
	}
	period := clockUTC().Format("2006-01-02")
	nextDay()
	correctedStatement := settle(period)
	failedPayment := createPayment(correctedStatement, "2000")
	callback(failedPayment, "definite-failure", "FAILED_FINAL")
	correctedStatement = call("GET", "/v1/statements/{id}", "/v1/statements/"+field(correctedStatement, "statement_id"), "operator", nil, 200)
	paid := createPayment(correctedStatement, "2000")
	if paid["merchant_payment_id"] == failedPayment["merchant_payment_id"] {
		t.Fatal("new attempt reused failed merchant ID")
	}
	callback(paid, "second-success", "SUCCEEDED")
	callback(paid, "late-conflict", "FAILED_FINAL")
	conflict := call("GET", "/v1/payments/{id}", "/v1/payments/"+field(paid, "payment_id"), "operator", nil, 200)
	if conflict["status"] != "SUCCEEDED" || conflict["reconciliation_status"] != "RECONCILIATION_EXCEPTION" {
		t.Fatal(conflict)
	}
	statementFile, _ := json.Marshal(map[string]any{"queries": map[string]any{}, "reconciliations": map[string]any{"statement-proof": []any{map[string]any{"callback_id": "bank-confirmed", "merchant_payment_id": paid["merchant_payment_id"], "currency": "USD", "minor_units": "2000", "status": "SUCCEEDED", "receipt_ref": nil}}}})
	os.WriteFile(resultsFile, statementFile, 0600)
	call("POST", "/v1/reconciliation-runs", "/v1/reconciliation-runs", "operator", map[string]any{"channel_id": "sandbox", "date_range": map[string]any{"from": eventAt, "to": clockUTC().Add(time.Second)}, "statement_file_ref": "statement-proof"}, 200)
	matched := call("GET", "/v1/payments/{id}", "/v1/payments/"+field(paid, "payment_id"), "operator", nil, 200)
	if matched["reconciliation_status"] != "MATCHED" || matched["status"] != "SUCCEEDED" {
		t.Fatal("verified statement did not resolve exception", matched)
	}
	riskID := "payment-" + field(paid, "payment_id")
	risk := call("GET", "/v1/risk-holds/{id}", "/v1/risk-holds/"+riskID, "operator", nil, 200)
	release := call("POST", "/v1/risk-holds/{id}/release-requests", "/v1/risk-holds/"+riskID+"/release-requests", "operator", map[string]any{"expected_revision": rev(risk), "reason": "对账凭证已复核", "evidence_ref": "statement-proof"}, 202)
	approve(release)
	money = call("GET", "/v1/entitlements/{id}", "/v1/entitlements/"+entitlementID, "operator", nil, 200)
	if money["target_minor"] != "4000" || money["paid_minor"] != "8000" || money["recovered_minor"] != "4000" || money["debt_minor"] != "0" || money["pending_minor"] != "0" || money["available_minor"] != "0" || money["reserved_minor"] != "0" || money["frozen"] != false {
		t.Fatal("final conservation failed", money)
	}
	for _, kind := range []string{"earnings", "finance", "cost"} {
		call("GET", "/v1/reports/{kind}", "/v1/reports/"+kind, "operator", nil, 200)
	}
	exported := call("POST", "/v1/report-exports", "/v1/report-exports", "operator", map[string]any{"kind": "finance", "filters": map[string]any{"currency": "USD"}, "format": "CSV"}, 202)
	exportID := field(exported, "export_id")
	call("GET", "/v1/report-exports/{id}", "/v1/report-exports/"+exportID, "operator", nil, 200)
	call("GET", "/v1/report-exports/{id}/download", "/v1/report-exports/"+exportID+"/download", "operator", nil, 200)
	for _, path := range []string{"/v1/platform/components", "/v1/platform/catalogs", "/v1/notifications", "/v1/audit-logs", "/v1/identity/subjects/local-operator/permissions"} {
		template := path
		if strings.Contains(path, "/subjects/") {
			template = "/v1/identity/subjects/{id}/permissions"
		}
		call("GET", template, path, "operator", nil, 200)
	}

	// Exercise the remaining public operations through the same real HTTP server.
	allEarnings := call("GET", "/v1/reports/{kind}", "/v1/reports/earnings", "operator", nil, 200)
	futureEntitlement := ""
	for _, row := range allEarnings["rows"].([]any) {
		v := row.(map[string]any)
		if v["entitlement_id"] != entitlementID {
			futureEntitlement = v["entitlement_id"].(string)
		}
	}
	if futureEntitlement == "" {
		t.Fatal("missing future entitlement")
	}
	manual := call("POST", "/v1/risk-holds", "/v1/risk-holds", "operator", map[string]any{"entitlement_id": futureEntitlement, "reason_code": "IDENTITY_REVIEW", "evidence_ref": "risk-proof"}, 201)
	manualID := field(manual, "risk_id")
	call("POST", "/v1/risk-holds/{id}/appeals", "/v1/risk-holds/"+manualID+"/appeals", "operator", map[string]any{"reason": "提交申诉证据", "evidence_ref": "appeal-proof"}, 201)
	pendingRelease := call("POST", "/v1/risk-holds/{id}/release-requests", "/v1/risk-holds/"+manualID+"/release-requests", "operator", map[string]any{"expected_revision": 0, "reason": "复核解除", "evidence_ref": "release-proof"}, 202)
	cancelID := field(pendingRelease, "approval_id")
	cancelApproval := call("GET", "/v1/approvals/{id}", "/v1/approvals/"+cancelID, "operator", nil, 200)
	call("POST", "/v1/approvals/{id}/cancellation", "/v1/approvals/"+cancelID+"/cancellation", "operator", map[string]any{"expected_revision": rev(cancelApproval), "object_hash": cancelApproval["object_hash"], "reason": "撤回后补充复核"}, 200)
	manualState := call("GET", "/v1/risk-holds/{id}", "/v1/risk-holds/"+manualID, "operator", nil, 200)
	approve(call("POST", "/v1/risk-holds/{id}/release-requests", "/v1/risk-holds/"+manualID+"/release-requests", "operator", map[string]any{"expected_revision": rev(manualState), "reason": "重新独立复核", "evidence_ref": "release-proof-2"}, 202))
	grants := call("GET", "/v1/identity/subjects/{id}/permissions", "/v1/identity/subjects/local-operator/permissions", "operator", nil, 200)
	approve(call("POST", "/v1/permission-change-requests", "/v1/permission-change-requests", "operator", map[string]any{"subject_id": "local-operator", "grant": []string{"workflow:recover", "agents:status:approve"}, "revoke": []string{}, "expected_revision": rev(grants), "reason": "验证角色组合不能绕过自审", "evidence_ref": "grant-proof"}, 202))
	remainingPeriod := clockUTC().Format("2006-01-02")
	nextDay()
	remainingStatement := settle(remainingPeriod)
	lastPayment := createPayment(remainingStatement, "7000")
	parentState := call("GET", "/v1/agents/{id}", "/v1/agents/"+agents["parent"], "operator", nil, 200)
	closing := call("POST", "/v1/agent-status-change-requests", "/v1/agent-status-change-requests", "operator", map[string]any{"agent_id": agents["parent"], "status": "CLOSED", "expected_revision": rev(parentState), "reason": "验证关闭前处理余额", "evidence_ref": "closure-proof"}, 202)
	closingID := field(closing, "approval_id")
	closingA := call("GET", "/v1/approvals/{id}", "/v1/approvals/"+closingID, "operator", nil, 200)
	closeDecision := map[string]any{"decision": "APPROVE", "expected_revision": rev(closingA), "object_hash": closingA["object_hash"], "reason": "独立检查账户关闭"}
	call("POST", "/v1/approvals/{id}/decisions", "/v1/approvals/"+closingID+"/decisions", "operator", closeDecision, 403)
	failedClose := call("POST", "/v1/approvals/{id}/decisions", "/v1/approvals/"+closingID+"/decisions", "approver", closeDecision, 200)
	if failedClose["execution_status"] != "FAILED" {
		t.Fatal("account closed with unknown funds", failedClose)
	}
	callback(lastPayment, "final-future-success", "SUCCEEDED")
	closingA = call("GET", "/v1/approvals/{id}", "/v1/approvals/"+closingID, "operator", nil, 200)
	retry := map[string]any{"expected_revision": rev(closingA), "object_hash": closingA["object_hash"], "reason": "已核清原付款及全部余额", "evidence_ref": "closed-balances-proof"}
	call("POST", "/v1/approvals/{id}/execution-retries", "/v1/approvals/"+closingID+"/execution-retries", "operator", retry, 403)
	recoveredExecution := call("POST", "/v1/approvals/{id}/execution-retries", "/v1/approvals/"+closingID+"/execution-retries", "approver", retry, 200)
	if recoveredExecution["execution_status"] != "SUCCEEDED" {
		t.Fatal(recoveredExecution)
	}
	parentState = call("GET", "/v1/agents/{id}", "/v1/agents/"+agents["parent"], "operator", nil, 200)
	if parentState["status"] != "CLOSED" {
		t.Fatal(parentState)
	}
	// Draft changes, cancellation and rollback append a new published version.
	config["name"] = "最终验收修订"
	edited := call("PUT", "/v1/plans/{id}/draft", "/v1/plans/P1/draft", "operator", map[string]any{"draft": config, "expected_revision": 0}, 200)
	trial := func(hash, product string, at time.Time) map[string]any {
		return call("POST", "/v1/simulations", "/v1/simulations", "operator", map[string]any{"draft_hash": hash, "business_facts": map[string]any{"basis_amount": "1000", "basis_currency": "USD", "product_code": product, "scene": "SPOT", "confirmed": true, "business_time": at}, "ancestors": []any{map[string]any{"agent_id": agents["parent"], "depth": 1, "grade_id": "G0", "eligible": true, "status": "ACTIVE"}}, "qualification_facts": map[string]any{agents["parent"]: map[string]any{"eligibility": "ELIGIBLE", "effective_from": eventAt, "effective_to": nil}}}, 200)
	}
	newEffective := clockUTC().Add(time.Second)
	newTrial := trial(field(edited, "draft_hash"), "BTC-USDT", newEffective.Add(time.Second))
	cancelPublish := call("POST", "/v1/plans/{id}/publication-requests", "/v1/plans/P1/publication-requests", "operator", map[string]any{"expected_revision": rev(edited), "draft_hash": edited["draft_hash"], "simulation_id": newTrial["simulation_id"], "effective_from": newEffective, "reason": "验证发布申请可撤回"}, 200)
	cpID := field(cancelPublish, "approval_id")
	cp := call("GET", "/v1/approvals/{id}", "/v1/approvals/"+cpID, "operator", nil, 200)
	call("POST", "/v1/approvals/{id}/cancellation", "/v1/approvals/"+cpID+"/cancellation", "operator", map[string]any{"expected_revision": rev(cp), "object_hash": cp["object_hash"], "reason": "撤回保留旧版"}, 200)
	currentDraft := call("GET", "/v1/plans/{id}", "/v1/plans/P1", "operator", nil, 200)
	rollback := call("POST", "/v1/plans/{id}/rollback-requests", "/v1/plans/P1/rollback-requests", "operator", map[string]any{"target_version_id": versionID, "effective_from": clockUTC().Add(time.Second), "reason": "回退生成新版本", "expected_revision": rev(currentDraft)}, 200)
	approve(rollback)
	// A task awaiting a future effective route resumes the original task, never a new payout.
	resumeAt := clockUTC().Add(time.Hour)
	eventBody["event_id"] = "awaiting-plan"
	eventBody["business_id"] = "awaiting-plan-order"
	eventBody["product_code"] = "OTHER"
	eventBody["business_time"] = resumeAt
	raw, _ = json.Marshal(eventBody)
	awaiting := callRaw("POST", "/v1/events", "/v1/events", "operator", "awaiting-plan", raw, map[string]string{"X-Source-Signature": sign(raw, sourceKey)}, 202)
	awaitID := field(awaiting, "task_id")
	if done, err := buildWorker(store).RunOnce(ctx, scope); err != nil || !done {
		t.Fatal(err)
	}
	suspended := call("GET", "/v1/calculation-tasks/{id}", "/v1/calculation-tasks/"+awaitID, "operator", nil, 200)
	if suspended["status"] != "SUSPENDED" {
		t.Fatal(suspended)
	}
	var secondConfig map[string]any
	dec := json.NewDecoder(bytes.NewReader(fixture))
	dec.UseNumber()
	dec.Decode(&secondConfig)
	secondConfig["plan_id"] = "P2"
	secondConfig["route"].(map[string]any)["product_code"] = "OTHER"
	secondDraft := call("POST", "/v1/plans", "/v1/plans", "operator", secondConfig, 200)
	secondEffective := clockUTC().Add(time.Second)
	secondTrial := trial(field(secondDraft, "draft_hash"), "OTHER", resumeAt)
	approve(call("POST", "/v1/plans/{id}/publication-requests", "/v1/plans/P2/publication-requests", "operator", map[string]any{"expected_revision": 0, "draft_hash": secondDraft["draft_hash"], "simulation_id": secondTrial["simulation_id"], "effective_from": secondEffective, "reason": "未来业务路由独立发布"}, 200))
	clock.Store(resumeAt.Add(time.Microsecond).UnixNano())
	call("POST", "/v1/calculation-tasks/{id}/resume", "/v1/calculation-tasks/"+awaitID+"/resume", "operator", map[string]any{"expected_revision": rev(suspended), "evidence_ref": "verified-future-route", "reason": "路由和历史事实已核实"}, 202)
	if done, err := buildWorker(store).RunOnce(ctx, scope); err != nil || !done {
		t.Fatal(err)
	}
	resumed := call("GET", "/v1/calculation-tasks/{id}", "/v1/calculation-tasks/"+awaitID, "operator", nil, 200)
	if resumed["status"] != "SUCCEEDED" || len(resumed["items"].([]any)) != 0 {
		t.Fatal("closed/not-joined agent earned commission", resumed)
	}
	call("GET", "/v1/calculation-tasks/{id}/adjustments", "/v1/calculation-tasks/"+awaitID+"/adjustments", "operator", nil, 200)
	nextDay()
	cutoff := time.Date(clockUTC().Year(), clockUTC().Month(), 1, 0, 0, 0, 0, time.UTC)
	periodMonth := cutoff.AddDate(0, -1, 0).Format("2006-01")
	statistics, _ := json.Marshal(map[string]any{"plan_id": "P2", "calendar_period": periodMonth, "cutoff": cutoff, "source_watermark": "e2e-frozen-statistics", "agents": map[string]any{agents["trader"]: map[string]string{}}})
	os.WriteFile(statsFile, statistics, 0600)
	call("POST", "/v1/grade-evaluations", "/v1/grade-evaluations", "operator", map[string]any{"plan_id": "P2", "calendar_period": periodMonth, "cutoff": cutoff, "expected_previous_evaluation": nil}, 200)
	for _, enabled := range []bool{false, true} {
		revision := 0
		if enabled {
			revision = 1
		}
		approve(call("POST", "/v1/platform-setting-requests", "/v1/platform-setting-requests", "operator", map[string]any{"kind": "COMPONENT_STATE", "configuration": map[string]any{"component_type": "NOTIFICATION", "component_id": "local-inbox", "enabled": enabled}, "expected_revision": revision, "reason": "核验通知连接开关"}, 202))
	}
	if err = deliverOutbox(ctx, store, scope); err != nil {
		t.Fatal(err)
	}
	call("GET", "/v1/notifications", "/v1/notifications", "operator", nil, 200)
	security := call("GET", "/v1/audit-logs", "/v1/audit-logs?type=SECURITY", "operator", nil, 200)
	securityRows := security["rows"].([]any)
	if len(securityRows) == 0 {
		t.Fatal("missing security audit")
	}
	securityID := securityRows[0].(map[string]any)["entry_id"].(string)
	call("POST", "/v1/security-events/{id}/handling-records", "/v1/security-events/"+securityID+"/handling-records", "operator", map[string]any{"expected_revision": 0, "status": "ACKNOWLEDGED", "reason": "安全事件已核实"}, 201)
	finalEarnings := call("GET", "/v1/reports/{kind}", "/v1/reports/earnings", "operator", nil, 200)
	for _, row := range finalEarnings["rows"].([]any) {
		v := row.(map[string]any)
		n := func(key string) *big.Int {
			value, ok := new(big.Int).SetString(v[key].(string), 10)
			if !ok {
				t.Fatal("invalid report money", v)
			}
			return value
		}
		left := new(big.Int).Add(n("net_minor"), n("debt_minor"))
		right := new(big.Int).Sub(n("paid_minor"), n("recovered_minor"))
		right.Add(right, n("pending_minor"))
		right.Add(right, n("available_minor"))
		right.Add(right, n("reserved_minor"))
		if left.Cmp(right) != 0 {
			t.Fatal("final per-entitlement conservation", v)
		}
	}
	clock.Add(int64(48 * time.Hour))
	call("GET", "/v1/report-exports/{id}/download", "/v1/report-exports/"+exportID+"/download", "operator", nil, 410)
	t.Logf("HTTP lifecycle passed: %d requests; original event -> approved payout -> unknown refund -> debt -> approved offset/cash -> append-only correction -> final failure/retry -> conflict reconciliation -> export; original target4000=paid8000-recovered4000; remaining future commission fully paid; all public operations exercised", len(transcript))
}
