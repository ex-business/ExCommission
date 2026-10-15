package runtime

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	calc "github.com/ex-business/ExCommission/internal/calculation/domain"
	ledger "github.com/ex-business/ExCommission/internal/ledger/adapters"
	payment "github.com/ex-business/ExCommission/internal/payments/domain"
	catalog "github.com/ex-business/ExCommission/internal/platform/adapters/catalog"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestReportsFrozenExportsCatalogsAndNotifications(t *testing.T) {
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
	scope := foundation.Scope{Tenant: "test-report-" + newID(), App: "app"}
	defer func() {
		for _, table := range []string{"calculation_tasks", "integration_event_keys", "integration_inbox", "integration_raw_payloads", "reporting_exports", "platform_notifications", "platform_component_states", "platform_catalog_versions", "ledger_payout_policies", "ledger_journal_links", "ledger_restrictions", "ledger_debt_ages", "ledger_journals", "ledger_entitlements", "transaction_outbox", "workflow_requests", "identity_grants", "identity_bootstraps", "identity_subject_revisions", "command_results", "audit_entries"} {
			store.DB.Exec("DELETE FROM "+table+" WHERE tenant_id=?", scope.Tenant)
		}
	}()
	token, checker := strings.Repeat("o", 32), strings.Repeat("a", 32)
	for key, value := range map[string]string{"APP_MODE": "local", "LOCAL_OPERATOR_TOKEN": token, "LOCAL_APPROVER_TOKEN": checker, "LOCAL_CURSOR_KEY": strings.Repeat("c", 32), "LOCAL_SOURCE_KEY": strings.Repeat("s", 32), "LOCAL_PAYMENT_KEY": strings.Repeat("p", 32), "LOCAL_TENANT": scope.Tenant, "LOCAL_APP": scope.App, "LOCAL_USERS_FILE": "", "LOCAL_GRADE_STATISTICS_FILE": ""} {
		t.Setenv(key, value)
	}
	handler, err := businessHandler(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	at := clockUTC().Add(-time.Hour)
	post := func(id, currency, amount string) string {
		t.Helper()
		input := calc.Input{TaskID: id, PlanVersionID: "pv-" + currency, Facts: calc.Facts{BusinessTime: at}}
		input.Plan.Currency = currency
		result := calc.Result{TaskID: id, Status: "SUCCEEDED", Items: []calc.Item{{RuleID: "r", BeneficiaryID: "agent", Currency: currency, Minor: amount}}}
		if err := store.Within(ctx, func(tx db.Executor) error {
			return (ledger.Repository{}).Commit(ctx, tx, scope, calc.Task{ID: id, Source: "s", BusinessID: id, EventType: "FEE_CONFIRMED"}, input, result, at)
		}); err != nil {
			t.Fatal(err)
		}
		items, err := (ledger.Repository{}).TaskEntitlements(ctx, store.DB, scope, id)
		if err != nil {
			t.Fatal(err)
		}
		return items[0].ID
	}
	usd, btc := post("usd", "USD", "100"), post("btc", "BTC", "200")
	if err = store.Within(ctx, func(tx db.Executor) error {
		r := ledger.Repository{}
		if err := r.Transfer(ctx, tx, scope, usd, "pending", "available", "100", "settle", at); err != nil {
			return err
		}
		if err := r.Transfer(ctx, tx, scope, usd, "available", "reserved", "40", "reserve", at); err != nil {
			return err
		}
		if err := r.Transfer(ctx, tx, scope, usd, "reserved", "paid", "40", "paid", at); err != nil {
			return err
		}
		return r.Reduce(ctx, tx, scope, usd, "80", "refund", at)
	}); err != nil {
		t.Fatal(err)
	}
	call := func(method, path, credential, key string, input any) *httptest.ResponseRecorder {
		t.Helper()
		raw, _ := json.Marshal(input)
		r := httptest.NewRequest(method, path, bytes.NewReader(raw))
		if credential != "" {
			r.Header.Set("Authorization", "Bearer "+credential)
		}
		r.Header.Set("Idempotency-Key", key)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	data := func(w *httptest.ResponseRecorder) map[string]any {
		t.Helper()
		var answer struct {
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &answer); err != nil {
			t.Fatal(w.Code, w.Body.String())
		}
		return answer.Data
	}
	earnings := call("GET", "/v1/reports/earnings", token, "", nil)
	if earnings.Code != 200 {
		t.Fatal(earnings.Code, earnings.Body.String())
	}
	rows := data(earnings)["rows"].([]any)
	if len(rows) != 2 {
		t.Fatal(rows)
	}
	amounts := map[string]string{}
	for _, row := range rows {
		v := row.(map[string]any)
		amounts[v["currency"].(string)] = v["net_minor"].(string)
	}
	if amounts["USD"] != "80" || amounts["BTC"] != "200" {
		t.Fatal("currency reports mixed", amounts)
	}
	costs := call("GET", "/v1/reports/cost", token, "", nil)
	if costs.Code != 200 {
		t.Fatal(costs.Code, costs.Body.String())
	}
	rows = data(costs)["rows"].([]any)
	for _, row := range rows {
		v := row.(map[string]any)
		if v["net_commission_cost_minor"] != amounts[v["currency"].(string)] {
			t.Fatal("ledger expense disagrees with net target", v)
		}
	}
	finance := call("GET", "/v1/reports/finance?currency=USD", token, "", nil)
	if finance.Code != 200 {
		t.Fatal(finance.Code, finance.Body.String())
	}
	for _, row := range data(finance)["rows"].([]any) {
		v := row.(map[string]any)
		if v["currency"] != "USD" || v["entitlement_id"] != usd {
			t.Fatal("finance lineage missing", v)
		}
	}
	exportKey := newID()
	exportInput := map[string]any{"kind": "earnings", "filters": map[string]any{}, "format": "CSV"}
	created := call("POST", "/v1/report-exports", token, exportKey, exportInput)
	if created.Code != 202 {
		t.Fatal(created.Code, created.Body.String())
	}
	exportID := data(created)["export_id"].(string)
	first := call("GET", "/v1/report-exports/"+exportID+"/download", token, "", nil)
	if first.Code != 200 || first.Header().Get("ETag") == "" {
		t.Fatal(first.Code, first.Body.String())
	}
	csv := append([]byte{}, first.Body.Bytes()...)
	if err = store.Within(ctx, func(tx db.Executor) error {
		return (ledger.Repository{}).Reduce(ctx, tx, scope, btc, "150", "later-refund", clockUTC())
	}); err != nil {
		t.Fatal(err)
	}
	again := call("GET", "/v1/report-exports/"+exportID+"/download", token, "", nil)
	if again.Code != 200 || !bytes.Equal(csv, again.Body.Bytes()) || again.Header().Get("ETag") != first.Header().Get("ETag") {
		t.Fatal("export snapshot regenerated")
	}
	if _, err = store.DB.Exec(`DELETE FROM identity_grants WHERE tenant_id=? AND app_id=? AND subject_id='local-operator' AND permission='reports:read'`, scope.Tenant, scope.App); err != nil {
		t.Fatal(err)
	}
	denied := call("GET", "/v1/report-exports/"+exportID+"/download", token, "", nil)
	if denied.Code != 403 {
		t.Fatal("download authorization stale", denied.Code)
	}
	store.DB.Exec(`INSERT INTO identity_grants VALUES(?,?,'local-operator','reports:read')`, scope.Tenant, scope.App)
	store.DB.Exec(`UPDATE reporting_exports SET expires_at=? WHERE tenant_id=? AND export_id=?`, at, scope.Tenant, exportID)
	expired := call("GET", "/v1/report-exports/"+exportID+"/download", token, "", nil)
	if expired.Code != 410 {
		t.Fatal("expired export not 410", expired.Code, expired.Body.String())
	}
	replay := call("POST", "/v1/report-exports", token, exportKey, exportInput)
	if replay.Code != 202 || data(replay)["export_id"] != exportID {
		t.Fatal("expired idempotency replay regenerated file")
	}
	components := call("GET", "/v1/platform/components", token, "", nil)
	if components.Code != 200 || bytes.Contains(components.Body.Bytes(), []byte(token)) {
		t.Fatal(components.Code, components.Body.String())
	}
	future := clockUTC().UTC().Truncate(24 * time.Hour).Add(48 * time.Hour)
	setting := map[string]any{"kind": "CALENDAR", "expected_revision": 0, "reason": "new calendar version", "configuration": map[string]any{"version_id": "UTC-v2", "effective_from": future, "timezone": "UTC", "period": "DAILY"}}
	request := call("POST", "/v1/platform-setting-requests", token, newID(), setting)
	if request.Code != 202 {
		t.Fatal(request.Code, request.Body.String())
	}
	approval := data(request)
	decision := map[string]any{"decision": "APPROVE", "expected_revision": 0, "object_hash": approval["object_hash"], "reason": "independent settings review"}
	approvalID := approval["approval_id"].(string)
	self := call("POST", "/v1/approvals/"+approvalID+"/decisions", token, newID(), decision)
	if self.Code != 403 {
		t.Fatal("settings self approval", self.Code)
	}
	approved := call("POST", "/v1/approvals/"+approvalID+"/decisions", checker, newID(), decision)
	if approved.Code != 200 || data(approved)["execution_status"] != "SUCCEEDED" {
		t.Fatal(approved.Code, approved.Body.String())
	}
	calendars, err := (catalog.Repository{}).CalendarVersions(ctx, store.DB, scope)
	if err != nil || !calendars["UTC-v1"] || !calendars["UTC-v2"] {
		t.Fatal("calendar replaced history", calendars, err)
	}
	if err = (catalog.Repository{}).ValidateCalendar(ctx, store.DB, scope, "UTC-v1", future.Add(-time.Microsecond)); err != nil {
		t.Fatal(err)
	}
	if err = (catalog.Repository{}).ValidateCalendar(ctx, store.DB, scope, "UTC-v2", future); err != nil {
		t.Fatal(err)
	}

	applySetting := func(kind string, configuration any, revision uint64) {
		t.Helper()
		r := call("POST", "/v1/platform-setting-requests", token, newID(), map[string]any{"kind": kind, "configuration": configuration, "expected_revision": revision, "reason": "explicit setting review"})
		if r.Code != 202 {
			t.Fatal(r.Code, r.Body.String())
		}
		a := data(r)
		decision := map[string]any{"decision": "APPROVE", "expected_revision": 0, "object_hash": a["object_hash"], "reason": "independent review"}
		r = call("POST", "/v1/approvals/"+a["approval_id"].(string)+"/decisions", checker, newID(), decision)
		if r.Code != 200 || data(r)["execution_status"] != "SUCCEEDED" {
			t.Fatal(r.Code, r.Body.String())
		}
	}
	currencies := foundation.CurrencyCatalog{Version: "currency-v2", From: future, Currencies: []foundation.Currency{{Code: "USD", Exponent: 2, Format: "0.00", Channels: []string{"sandbox"}}, {Code: "BTC", Exponent: 8, Format: "0.00000000", Channels: []string{"sandbox"}}, {Code: "POINT", Exponent: 0, Format: "0", Channels: []string{"sandbox"}}}}
	applySetting("CURRENCY_CATALOG", currencies, 0)
	beforeVersion, beforeExponent, err := (catalog.Repository{}).CurrencyAt(ctx, store.DB, scope, "USD", future.Add(-time.Microsecond))
	afterVersion, afterExponent, err2 := (catalog.Repository{}).CurrencyAt(ctx, store.DB, scope, "USD", future)
	if err != nil || err2 != nil || beforeVersion != "currency-v1" || afterVersion != "currency-v2" || beforeExponent != 2 || afterExponent != 2 {
		t.Fatal("currency version boundary", beforeVersion, afterVersion, err, err2)
	}
	applySetting("COMPONENT_STATE", foundation.ComponentState{Type: "SOURCE", ID: "sandbox", Enabled: false}, 0)
	event := map[string]any{"schema_version": "1", "source_id": "sandbox", "event_id": "disabled-source-event", "business_id": "disabled-business", "event_type": "FEE_CONFIRMED", "business_time": clockUTC(), "external_user_id": "user", "product_code": "BTC-USDT", "scene": "SPOT", "source_revision": "1", "payload": map[string]any{"basis_amount": "1", "basis_currency": "USD", "confirmed": true}}
	signedEvent := func() int {
		raw, _ := json.Marshal(event)
		mac := hmac.New(sha256.New, []byte(strings.Repeat("s", 32)))
		mac.Write(raw)
		r := httptest.NewRequest("POST", "/v1/events", bytes.NewReader(raw))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Idempotency-Key", newID())
		r.Header.Set("X-Source-Signature", hex.EncodeToString(mac.Sum(nil)))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Code
	}
	if status := signedEvent(); status != 422 {
		t.Fatal("disabled source accepted new event", status)
	}
	applySetting("COMPONENT_STATE", foundation.ComponentState{Type: "SOURCE", ID: "sandbox", Enabled: true}, 1)
	if status := signedEvent(); status != 202 {
		t.Fatal("source did not resume", status)
	}
	applySetting("COMPONENT_STATE", foundation.ComponentState{Type: "PAYMENT", ID: "sandbox", Enabled: false}, 0)
	provider := paymentService(store).Providers["sandbox"]
	if provider.Enabled() {
		t.Fatal("disabled payment channel enabled")
	}
	outcome, err := provider.Query(ctx, payment.Payment{MerchantID: "original", Currency: "USD", Amount: "1"})
	if err != nil || outcome.MerchantID != "original" || outcome.Status != "UNKNOWN" {
		t.Fatal("disabled channel blocked original query", outcome, err)
	}
	for range 2 {
		if err = (catalog.Repository{}).Send(ctx, store.DB, scope, "event1", "entitlement.created", []byte(`{"private":"not-in-notification"}`), at); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	store.DB.QueryRow(`SELECT COUNT(*) FROM platform_notifications WHERE tenant_id=?`, scope.Tenant).Scan(&count)
	if count != 1 {
		t.Fatal("notification replay", count)
	}
	if err = (catalog.Repository{}).Send(ctx, store.DB, scope, "event1", "entitlement.created", []byte(`{}`), at); err == nil {
		t.Fatal("notification payload conflict ignored")
	}
	notifications := call("GET", "/v1/notifications", token, "", nil)
	if notifications.Code != 200 || bytes.Contains(notifications.Body.Bytes(), []byte("not-in-notification")) {
		t.Fatal("notification leaked raw payload", notifications.Code)
	}
}
