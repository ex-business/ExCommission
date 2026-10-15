package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	agents "github.com/ex-business/ExCommission/internal/agents/adapters"
	calc "github.com/ex-business/ExCommission/internal/calculation/domain"
	ledger "github.com/ex-business/ExCommission/internal/ledger/adapters"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestRiskReasonsHistoricalStatusAndAtomicAudit(t *testing.T) {
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
	scope := foundation.Scope{Tenant: "test-risk-" + newID(), App: "app"}
	defer func() {
		for _, table := range []string{"identity_subject_revisions", "risk_appeals", "risk_requests", "audit_handling_records", "audit_security_states", "audit_entries", "ledger_payout_policies", "ledger_journal_links", "ledger_restrictions", "ledger_journals", "ledger_entitlements", "transaction_outbox", "agent_status_history", "agent_accounts", "agent_relations", "workflow_requests", "platform_catalog_versions", "platform_component_states", "platform_notifications", "reporting_exports", "identity_subject_revisions", "identity_bootstraps", "identity_grants", "command_results"} {
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
	at := clockUTC().Add(-2 * time.Hour)
	if err = store.Within(ctx, func(tx db.Executor) error {
		if e := (agents.Repository{}).Create(ctx, tx, scope, "a", "u", "name", at); e != nil {
			return e
		}
		input := calc.Input{TaskID: "task", PlanVersionID: "pv", Facts: calc.Facts{BusinessTime: at}}
		input.Plan.Currency = "USD"
		result := calc.Result{TaskID: "task", Status: "SUCCEEDED", Items: []calc.Item{{RuleID: "r", BeneficiaryID: "a", Currency: "USD", Minor: "100"}}}
		return (ledger.Repository{}).Commit(ctx, tx, scope, calc.Task{ID: "task", Source: "s", BusinessID: "b", EventType: "FEE_CONFIRMED"}, input, result, at)
	}); err != nil {
		t.Fatal(err)
	}
	entitlements, err := (ledger.Repository{}).TaskEntitlements(ctx, store.DB, scope, "task")
	if err != nil {
		t.Fatal(err)
	}
	eid := entitlements[0].ID
	call := func(method, path, credential string, input any) (int, map[string]any) {
		t.Helper()
		body, _ := json.Marshal(input)
		r := httptest.NewRequest(method, path, bytes.NewReader(body))
		if credential != "" {
			r.Header.Set("Authorization", "Bearer "+credential)
		}
		r.Header.Set("Idempotency-Key", newID())
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		var output map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &output); err != nil {
			t.Fatalf("invalid response %d %s", w.Code, w.Body.String())
		}
		return w.Code, output
	}
	call("GET", "/v1/audit-logs", "", nil)
	hold := func(reason string) string {
		t.Helper()
		status, out := call("POST", "/v1/risk-holds", token, map[string]string{"entitlement_id": eid, "reason_code": reason, "evidence_ref": "proof"})
		if status != 201 {
			t.Fatal(status, out)
		}
		return out["data"].(map[string]any)["risk_id"].(string)
	}
	approve := func(out map[string]any, want string) {
		t.Helper()
		data := out["data"].(map[string]any)
		id := data["approval_id"].(string)
		decision := map[string]any{"decision": "APPROVE", "expected_revision": 0, "object_hash": data["object_hash"], "reason": "independent review"}
		status, _ := call("POST", "/v1/approvals/"+id+"/decisions", token, decision)
		if status != 403 {
			t.Fatal("self approval allowed", status)
		}
		status, answer := call("POST", "/v1/approvals/"+id+"/decisions", checker, decision)
		if status != 200 || answer["data"].(map[string]any)["execution_status"] != want {
			t.Fatal("execution", status, answer)
		}
	}
	release := func(risk string, revision uint64, want string) {
		t.Helper()
		status, out := call("POST", "/v1/risk-holds/"+risk+"/release-requests", token, map[string]any{"expected_revision": revision, "reason": "reviewed", "evidence_ref": "release-proof"})
		if status != 202 {
			t.Fatal(status, out)
		}
		approve(out, want)
	}
	one, two := hold("ONE"), hold("TWO")
	release(one, 0, "SUCCEEDED")
	money, _ := (ledger.Repository{}).Get(ctx, store.DB, scope, eid, false)
	if !money.Frozen {
		t.Fatal("one release cleared all reasons")
	}
	release(two, 0, "SUCCEEDED")
	money, _ = (ledger.Repository{}).Get(ctx, store.DB, scope, eid, false)
	if money.Frozen {
		t.Fatal("all reasons still frozen")
	}
	move := func(from, to, amount, ref string) {
		t.Helper()
		if err := store.Within(ctx, func(tx db.Executor) error {
			return (ledger.Repository{}).Transfer(ctx, tx, scope, eid, from, to, amount, ref, clockUTC())
		}); err != nil {
			t.Fatal(err)
		}
	}
	move("pending", "available", "100", "settled")
	move("available", "reserved", "40", "submitted")
	three := hold("PAYMENT_RISK")
	release(three, 0, "FAILED")
	money, _ = (ledger.Repository{}).Get(ctx, store.DB, scope, eid, false)
	if money.Reserved != "40" || !money.Frozen {
		t.Fatal("unknown funds released", money)
	}
	move("reserved", "paid", "40", "confirmed")
	release(three, 2, "SUCCEEDED")
	money, _ = (ledger.Repository{}).Get(ctx, store.DB, scope, eid, false)
	if money.Pending != "60" || money.Available != "0" || money.Paid != "40" || money.Frozen {
		t.Fatal("release did not require new approval", money)
	}
	status, out := call("POST", "/v1/agent-status-change-requests", token, map[string]any{"agent_id": "a", "status": "FROZEN", "expected_revision": 0, "reason": "manual freeze", "evidence_ref": "status-proof"})
	if status != 202 {
		t.Fatal(status, out)
	}
	approve(out, "SUCCEEDED")
	old, err := (agents.Repository{}).AccountAt(ctx, store.DB, scope, "a", at.Add(time.Minute))
	current, err2 := (agents.Repository{}).AccountAt(ctx, store.DB, scope, "a", clockUTC())
	if err != nil || err2 != nil || old.Status != "ACTIVE" || current.Status != "FROZEN" {
		t.Fatal("current state replaced history", old, current, err, err2)
	}
	if err = store.Within(ctx, func(tx db.Executor) error { return (agents.Repository{}).PaymentAllowed(ctx, tx, scope, "a") }); err == nil {
		t.Fatal("frozen account payable")
	}
	status, out = call("POST", "/v1/agent-status-change-requests", token, map[string]any{"agent_id": "a", "status": "CLOSED", "expected_revision": 1, "reason": "close", "evidence_ref": "closure-proof"})
	if status != 202 {
		t.Fatal(status, out)
	}
	approve(out, "FAILED")
	current, _ = (agents.Repository{}).AccountAt(ctx, store.DB, scope, "a", clockUTC())
	if current.Status != "FROZEN" {
		t.Fatal("failed close mutated status")
	}
	status, out = call("GET", "/v1/audit-logs?type=SECURITY", token, nil)
	if status != 200 {
		t.Fatal(status, out)
	}
	encoded, _ := json.Marshal(out)
	if bytes.Contains(encoded, []byte(token)) || bytes.Contains(encoded, []byte(checker)) || bytes.Contains(encoded, []byte("closure-proof")) {
		t.Fatal("audit leaked credentials or payload")
	}
	rows := out["data"].(map[string]any)["rows"].([]any)
	if len(rows) == 0 {
		t.Fatal("denials not audited")
	}
	eventID := rows[0].(map[string]any)["entry_id"].(string)
	var original []byte
	store.DB.QueryRow(`SELECT summary FROM audit_entries WHERE tenant_id=? AND entry_id=?`, scope.Tenant, eventID).Scan(&original)
	status, out = call("POST", "/v1/security-events/"+eventID+"/handling-records", token, map[string]any{"expected_revision": 0, "status": "ACKNOWLEDGED", "reason": "investigated"})
	if status != 201 {
		t.Fatal(status, out)
	}
	var unchanged []byte
	store.DB.QueryRow(`SELECT summary FROM audit_entries WHERE tenant_id=? AND entry_id=?`, scope.Tenant, eventID).Scan(&unchanged)
	if !bytes.Equal(original, unchanged) {
		t.Fatal("handling overwrote original audit")
	}

	status, out = call("POST", "/v1/permission-change-requests", token, map[string]any{"subject_id": "local-operator", "grant": []string{"identity:approve"}, "revoke": []string{"payments:read"}, "expected_revision": 0, "reason": "separate authorization", "evidence_ref": "permission-proof"})
	if status != 202 {
		t.Fatal(status, out)
	}
	approve(out, "SUCCEEDED")
	status, _ = call("GET", "/v1/payments/missing", token, nil)
	if status != 403 {
		t.Fatal("revocation did not affect next request", status)
	}
	restarted, err := businessHandler(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	handler = restarted
	status, _ = call("GET", "/v1/payments/missing", token, nil)
	if status != 403 {
		t.Fatal("restart restored revoked grant", status)
	}
	status, out = call("POST", "/v1/permission-change-requests", token, map[string]any{"subject_id": "local-operator", "grant": []string{"payments:read"}, "revoke": []string{"identity:approve"}, "expected_revision": 1, "reason": "restore explicit permission", "evidence_ref": "permission-proof-2"})
	if status != 202 {
		t.Fatal(status, out)
	}
	approve(out, "SUCCEEDED")
	status, _ = call("GET", "/v1/payments/missing", token, nil)
	if status != 404 {
		t.Fatal("approved permission not effective", status)
	}
	// Audit write failure rolls back the business approval request and command result.
	savedAudit := store.Audit
	store.Audit = func(context.Context, db.Executor, foundation.Scope, string, string, string) error {
		return errors.New("injected audit failure")
	}
	var before, after int
	store.DB.QueryRow(`SELECT COUNT(*) FROM workflow_requests WHERE tenant_id=?`, scope.Tenant).Scan(&before)
	status, _ = call("POST", "/v1/agent-status-change-requests", token, map[string]any{"agent_id": "a", "status": "ACTIVE", "expected_revision": 1, "reason": "resume", "evidence_ref": "proof"})
	store.Audit = savedAudit
	store.DB.QueryRow(`SELECT COUNT(*) FROM workflow_requests WHERE tenant_id=?`, scope.Tenant).Scan(&after)
	if status != 503 || before != after {
		t.Fatal("audit failure committed business", status, before, after)
	}
}
