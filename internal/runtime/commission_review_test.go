package runtime

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	paymentmodel "github.com/ex-business/ExCommission/internal/payments/domain"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	"github.com/ex-business/ExCommission/internal/platform/ports"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type payoutLinkFailure struct{ store *mysql.Store }

func (f payoutLinkFailure) Within(ctx context.Context, fn func(ports.Executor) error) error {
	return f.store.Within(ctx, func(tx ports.Executor) error {
		if err := fn(tx); err != nil {
			return err
		}
		return foundation.ErrValidation
	})
}
func TestCommissionReviewAndBatchPayout(t *testing.T) { commissionReviewFixture(t, false, false) }
func TestMemberCommissionClaims(t *testing.T)         { commissionReviewFixture(t, true, false) }
func TestWalletClaimAtomicCredit(t *testing.T)        { commissionReviewFixture(t, true, true) }
func commissionReviewFixture(t *testing.T, memberMode, walletMode bool) {
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
	memberToken, otherToken := "", ""
	if memberMode {
		var accounts []map[string]string
		salt := make([]byte, 16)
		hash, _ := pbkdf2.Key(sha256.New, "claim-test-password", salt, 600000, 32)
		for _, name := range []string{"parent", "trader"} {
			b, _ := json.Marshal([]string{"sandbox", name})
			sum := sha256.Sum256(b)
			accounts = append(accounts, map[string]string{"username": name, "member_user_id": "usr_" + hex.EncodeToString(sum[:16]), "salt": hex.EncodeToString(salt), "hash": hex.EncodeToString(hash)})
		}
		b, _ := json.Marshal(accounts)
		file := filepath.Join(directory, "members.json")
		if err := os.WriteFile(file, b, 0600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("AUTH_LOCAL_ACCOUNTS_FILE", file)
	}
	handler, err := businessHandler(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	client := server.Client()
	client.Timeout = 15 * time.Second
	if memberMode {
		login := func(name string) string {
			b, _ := json.Marshal(map[string]string{"username": name, "password": "claim-test-password"})
			r, e := client.Post(server.URL+"/v1/auth/login", "application/json", bytes.NewReader(b))
			if e != nil {
				t.Fatal(e)
			}
			defer r.Body.Close()
			var v map[string]any
			if e = json.NewDecoder(r.Body).Decode(&v); e != nil || r.StatusCode != 200 {
				t.Fatal(v, e)
			}
			return v["access_token"].(string)
		}
		memberToken = login("parent")
		otherToken = login("trader")
	}
	transcript := []httpEvidence{}
	t.Cleanup(func() {
		path := os.Getenv("REVIEW_HTTP_EVIDENCE_FILE")
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
		} else if role == "member" {
			r.Header.Set("Authorization", "Bearer "+memberToken)
		} else if role == "other" {
			r.Header.Set("Authorization", "Bearer "+otherToken)
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
	clock.Store(eventAt.Truncate(24 * time.Hour).Add(24*time.Hour + time.Hour).UnixNano())
	run := call("POST", "/v1/statement-runs", "/v1/statement-runs", "operator", map[string]any{"period": eventAt.Format("2006-01-02"), "calendar_version": "UTC-v1"}, 202)
	statement := run["statements"].([]any)[0].(map[string]any)
	sid := field(statement, "statement_id")
	submission := call("POST", "/v1/statements/{id}/submission", "/v1/statements/"+sid+"/submission", "operator", map[string]any{"expected_revision": rev(statement), "content_hash": statement["content_hash"]}, 202)
	if memberMode {
		m := call("GET", "/v1/me/commissions", "/v1/me/commissions", "member", nil, 200)
		if m["rows"].([]any)[0].(map[string]any)["review_status"] != "WAITING_REVIEW" {
			t.Fatal(m)
		}
		o := call("GET", "/v1/me/commissions", "/v1/me/commissions", "other", nil, 200)
		if len(o["rows"].([]any)) != 0 {
			t.Fatal("foreign commission disclosed", o)
		}
		call("GET", "/v1/me/commissions", "/v1/me/commissions", "operator", nil, 403)
		callRaw("GET", "/v1/me/commissions", "/v1/me/commissions", "member", "", nil, map[string]string{"X-Tenant-ID": "foreign"}, 403)
	}
	list := call("GET", "/v1/commission-reviews", "/v1/commission-reviews?approval_status=PENDING&limit=1", "operator", nil, 200)
	rows := list["rows"].([]any)
	if len(rows) != 1 {
		t.Fatal(list)
	}
	review := rows[0].(map[string]any)
	item := map[string]any{"approval_id": submission["approval_id"], "decision": "APPROVE", "expected_revision": rev(review), "object_hash": review["object_hash"], "reason": "批量审核", "remark": "佣金审核备注"}
	missing := map[string]any{"approval_id": "missing", "decision": "APPROVE", "expected_revision": 0, "object_hash": "absent", "reason": "不存在"}
	batch := map[string]any{"items": []any{item, missing}}
	raw, _ = json.Marshal(batch)
	callRaw("POST", "/v1/commission-review-batches", "/v1/commission-review-batches", "operator", "self-batch", raw, nil, 403)
	result := callRaw("POST", "/v1/commission-review-batches", "/v1/commission-review-batches", "approver", "review-batch", raw, nil, 200)
	out := result["results"].([]any)
	if out[0].(map[string]any)["http_status"].(json.Number).String() != "200" || out[1].(map[string]any)["http_status"].(json.Number).String() != "404" {
		t.Fatal(result)
	}
	callRaw("POST", "/v1/commission-review-batches", "/v1/commission-review-batches", "approver", "review-batch", raw, nil, 200)
	changed, _ := json.Marshal(map[string]any{"items": []any{missing}})
	callRaw("POST", "/v1/commission-review-batches", "/v1/commission-review-batches", "approver", "review-batch", changed, nil, 409)
	list = call("GET", "/v1/commission-reviews", "/v1/commission-reviews?approval_status=APPROVED", "operator", nil, 200)
	if list["rows"].([]any)[0].(map[string]any)["remark"] != "佣金审核备注" {
		t.Fatal(list)
	}
	statement = call("GET", "/v1/statements/{id}", "/v1/statements/"+sid, "operator", nil, 200)
	prepare := func(amount string, want int) map[string]any {
		t.Helper()
		mode := "DIRECT"
		if memberMode {
			mode = "MEMBER_CLAIM"
		}
		channel := "sandbox"
		if walletMode {
			channel = "wallet"
		}
		return call("POST", "/v1/payout-orders", "/v1/payout-orders", "operator", map[string]any{"delivery_mode": mode, "statement_id": sid, "amount": map[string]string{"currency": "USD", "minor_units": amount}, "channel_id": channel, "expected_statement_revision": rev(statement), "remark": "本次派发备注"}, want)
	}
	prepare("10001", 422)
	draftOrder := prepare("6000", 201)
	iid := field(draftOrder, "instruction_id")
	edited := call("PUT", "/v1/payout-orders/{id}", "/v1/payout-orders/"+iid, "operator", map[string]any{"expected_revision": rev(draftOrder), "amount": map[string]string{"currency": "USD", "minor_units": "5000"}, "remark": "少派部分保留权益"}, 200)
	action := func(action string, i map[string]any, key string) map[string]any {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"action": action, "items": []any{map[string]any{"instruction_id": i["instruction_id"], "expected_revision": rev(i), "reason": "操作备注"}}})
		return callRaw("POST", "/v1/payout-order-batches", "/v1/payout-order-batches", "operator", key, body, nil, 200)
	}
	action("CANCEL", edited, "cancel-first")
	cancelled := call("GET", "/v1/payout-orders/{id}", "/v1/payout-orders/"+iid, "operator", nil, 200)
	if cancelled["status"] != "CANCELLED" {
		t.Fatal(cancelled)
	}
	if memberMode {
		draftOrder = prepare("5000", 201)
		action("RELEASE", draftOrder, "publish-cancel")
		ready := call("GET", "/v1/payout-orders/{id}", "/v1/payout-orders/"+field(draftOrder, "instruction_id"), "operator", nil, 200)
		action("CANCEL", ready, "cancel-claimable")
		call("POST", "/v1/me/commission-orders/{id}/claim", "/v1/me/commission-orders/"+field(ready, "instruction_id")+"/claim", "member", map[string]any{"expected_revision": 2}, 409)
	}
	draftOrder = prepare("5000", 201)
	iid = field(draftOrder, "instruction_id")
	action("RELEASE", draftOrder, "release-batch")
	action("RELEASE", draftOrder, "release-batch")
	call("PUT", "/v1/payout-orders/{id}", "/v1/payout-orders/"+iid, "operator", map[string]any{"expected_revision": 1, "amount": map[string]string{"currency": "USD", "minor_units": "4000"}, "remark": "不能修改已提交"}, 409)
	if memberMode {
		ready := call("GET", "/v1/payout-orders/{id}", "/v1/payout-orders/"+iid, "operator", nil, 200)
		if ready["status"] != "CLAIMABLE" || ready["claim_status"] != "CLAIMABLE" {
			t.Fatal(ready)
		}
		if err := disbursementService(store).Recover(ctx, scope); err != nil {
			t.Fatal(err)
		}
		var n int
		store.DB.QueryRow("SELECT COUNT(*) FROM payment_orders WHERE tenant_id=?", scope.Tenant).Scan(&n)
		if n != 0 {
			t.Fatal("paid before member claim")
		}
		body, _ := json.Marshal(map[string]any{"expected_revision": rev(ready)})
		path := "/v1/me/commission-orders/" + iid + "/claim"
		callRaw("POST", "/v1/me/commission-orders/{id}/claim", path, "other", "member-claim", body, nil, 404)
		// Ownership failure must not poison the legitimate member's command key.
		callRaw("POST", "/v1/me/commission-orders/{id}/claim", path, "member", "member-claim", body, nil, 202)
		callRaw("POST", "/v1/me/commission-orders/{id}/claim", path, "member", "member-claim", body, nil, 202)
		changed, _ := json.Marshal(map[string]any{"expected_revision": 99})
		callRaw("POST", "/v1/me/commission-orders/{id}/claim", path, "member", "member-claim", changed, nil, 409)
		var claims sync.WaitGroup
		for range 12 {
			claims.Go(func() {
				r, _ := http.NewRequest("POST", server.URL+path, bytes.NewReader(body))
				r.Header.Set("Authorization", "Bearer "+memberToken)
				r.Header.Set("Content-Type", "application/json")
				r.Header.Set("Idempotency-Key", newID())
				res, e := client.Do(r)
				if e != nil {
					t.Error(e)
					return
				}
				defer res.Body.Close()
				b, _ := io.ReadAll(res.Body)
				if res.StatusCode != 202 {
					t.Errorf("claim concurrency: %d %s", res.StatusCode, b)
				}
			})
		}
		claims.Wait()
		queued := call("GET", "/v1/payout-orders/{id}", "/v1/payout-orders/"+iid, "operator", nil, 200)
		cancellation := action("CANCEL", queued, "cannot-cancel-processing")
		if cancellation["results"].([]any)[0].(map[string]any)["http_status"].(json.Number).String() != "409" {
			t.Fatal(cancellation)
		}
		if _, err := store.DB.Exec("DELETE FROM identity_grants WHERE tenant_id=? AND subject_id LIKE 'member-%' AND permission='member:claim'", scope.Tenant); err != nil {
			t.Fatal(err)
		}
		callRaw("POST", "/v1/me/commission-orders/{id}/claim", path, "member", "member-claim", body, nil, 403)
	}
	broken := disbursementService(store)
	broken.UOW = payoutLinkFailure{store}
	if err := broken.Recover(ctx, scope); err == nil {
		t.Fatal("expected link failure")
	}
	queued := call("GET", "/v1/payout-orders/{id}", "/v1/payout-orders/"+iid, "operator", nil, 200)
	if queued["status"] != "QUEUED" {
		t.Fatal("link failure not rolled back", queued)
	}
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			if e := disbursementService(store).Recover(ctx, scope); e != nil {
				t.Error(e)
			}
		})
	}
	wg.Wait()
	linked := call("GET", "/v1/payout-orders/{id}", "/v1/payout-orders/"+iid, "operator", nil, 200)
	if linked["status"] != "LINKED" || linked["payment_id"] == nil {
		t.Fatal(linked)
	}
	count := 0
	if e := store.DB.QueryRow("SELECT COUNT(*) FROM payment_orders WHERE tenant_id=?", scope.Tenant).Scan(&count); e != nil || count != 1 {
		t.Fatal(count, e)
	}
	if walletMode {
		// Credit and its history have been written before injected failure; all must roll back with the commission ledger.
		failing := paymentService(store)
		credit := failing.Credit
		failing.Credit = func(ctx context.Context, tx ports.Executor, scope foundation.Scope, p paymentmodel.Payment) error {
			if e := credit(ctx, tx, scope, p); e != nil {
				return e
			}
			return errors.New("injected wallet transaction failure")
		}
		if e := failing.Dispatch(ctx, scope, linked["payment_id"].(string)); e == nil {
			t.Fatal("expected atomic credit rollback")
		}
		var n int
		if e := store.DB.QueryRow("SELECT COUNT(*) FROM wallet_transactions WHERE tenant_id=?", scope.Tenant).Scan(&n); e != nil || n != 0 {
			t.Fatal("partial wallet commit", n, e)
		}
	}
	if e := paymentService(store).Recover(ctx, scope); e != nil {
		t.Fatal(e)
	}
	payment := call("GET", "/v1/payments/{id}", "/v1/payments/"+linked["payment_id"].(string), "operator", nil, 200)
	expectedPayment := "UNKNOWN"
	if walletMode {
		expectedPayment = "SUCCEEDED"
	}
	if payment["status"] != expectedPayment || payment["amount"].(map[string]any)["minor_units"] != "5000" {
		t.Fatal(payment)
	}
	if memberMode {
		m := call("GET", "/v1/me/commissions", "/v1/me/commissions", "member", nil, 200)
		orders := m["rows"].([]any)[0].(map[string]any)["orders"].([]any)
		found := false
		for _, o := range orders {
			v := o.(map[string]any)
			if v["instruction_id"] == iid {
				found = true
				expected := "PROCESSING"
				if walletMode {
					expected = "CLAIMED"
				}
				if v["claim_status"] != expected {
					t.Fatal(v)
				}
			}
		}
		if !found {
			t.Fatal("claim missing", m)
		}
	}
	callback, _ := json.Marshal(map[string]any{"callback_id": "batch-paid", "merchant_payment_id": payment["merchant_payment_id"], "currency": "USD", "minor_units": "5000", "status": "SUCCEEDED", "receipt_ref": nil})
	if !walletMode {
		for range 2 {
			callRaw("POST", "/v1/payment-callbacks/{channel_id}", "/v1/payment-callbacks/sandbox", "none", "", callback, map[string]string{"X-Payment-Signature": sign(callback, paymentKey)}, 200)
		}
	} else {
		callRaw("POST", "/v1/payment-callbacks/{channel_id}", "/v1/payment-callbacks/wallet", "none", "", callback, map[string]string{"X-Payment-Signature": sign(callback, paymentKey)}, 403)
		for range 3 {
			if e := paymentService(store).Verify(ctx, scope, linked["payment_id"].(string)); e != nil {
				t.Fatal(e)
			}
		}
		for _, kind := range []string{"wallets", "wallet-transactions", "wallet-changes"} {
			m := call("GET", "/v1/me/"+kind, "/v1/me/"+kind, "member", nil, 200)
			rows := m["rows"].([]any)
			if len(rows) != 1 {
				t.Fatal(kind, m)
			}
			v := rows[0].(map[string]any)
			if kind == "wallets" {
				if v["balance_minor"] != "5000" || v["wallet_type"] != "REAL_MONEY" {
					t.Fatal(v)
				}
			} else {
				if v["amount_minor"] != "5000" {
					t.Fatal(v)
				}
				if kind == "wallet-changes" && (v["balance_before_minor"] != "0" || v["balance_after_minor"] != "5000") {
					t.Fatal(v)
				}
			}
			other := call("GET", "/v1/me/"+kind, "/v1/me/"+kind, "other", nil, 200)
			if len(other["rows"].([]any)) != 0 {
				t.Fatal("foreign wallet exposed", other)
			}
		}
	}
	if memberMode {
		m := call("GET", "/v1/me/commissions", "/v1/me/commissions", "member", nil, 200)
		orders := m["rows"].([]any)[0].(map[string]any)["orders"].([]any)
		found := false
		for _, o := range orders {
			v := o.(map[string]any)
			if v["instruction_id"] == iid {
				found = true
				if v["claim_status"] != "CLAIMED" {
					t.Fatal(v)
				}
			}
		}
		if !found {
			t.Fatal("paid claim missing", m)
		}
	}

	if memberMode && !walletMode {
		// Restore the intentionally revoked fixture permission, never a production grant.
		sum := sha256.Sum256([]byte("parent"))
		subject := "member-" + hex.EncodeToString(sum[:])
		if _, e := store.DB.Exec("INSERT INTO identity_grants VALUES(?,?,?,?)", scope.Tenant, scope.App, subject, "member:claim"); e != nil {
			t.Fatal(e)
		}
		statement = call("GET", "/v1/statements/{id}", "/v1/statements/"+sid, "operator", nil, 200)
		nextOrder := prepare("2000", 201)
		action("RELEASE", nextOrder, "publish-final-failure")
		nextID := field(nextOrder, "instruction_id")
		call("POST", "/v1/me/commission-orders/{id}/claim", "/v1/me/commission-orders/"+nextID+"/claim", "member", map[string]any{"expected_revision": 1}, 202)
		if e := disbursementService(store).Recover(ctx, scope); e != nil {
			t.Fatal(e)
		}
		nextLinked := call("GET", "/v1/payout-orders/{id}", "/v1/payout-orders/"+nextID, "operator", nil, 200)
		nextPayment := call("GET", "/v1/payments/{id}", "/v1/payments/"+field(nextLinked, "payment_id"), "operator", nil, 200)
		failed, _ := json.Marshal(map[string]any{"callback_id": "claim-final-failure", "merchant_payment_id": nextPayment["merchant_payment_id"], "currency": "USD", "minor_units": "2000", "status": "FAILED_FINAL", "receipt_ref": nil})
		callRaw("POST", "/v1/payment-callbacks/{channel_id}", "/v1/payment-callbacks/sandbox", "none", "", failed, map[string]string{"X-Payment-Signature": sign(failed, paymentKey)}, 200)
		m := call("GET", "/v1/me/commissions", "/v1/me/commissions", "member", nil, 200)
		found := false
		for _, o := range m["rows"].([]any)[0].(map[string]any)["orders"].([]any) {
			v := o.(map[string]any)
			if v["instruction_id"] == nextID {
				found = true
				if v["claim_status"] != "FAILED" {
					t.Fatal(v)
				}
			}
		}
		if !found {
			t.Fatal(m)
		}
	}

	if memberMode {
		if walletMode {
			sum := sha256.Sum256([]byte("parent"))
			subject := "member-" + hex.EncodeToString(sum[:])
			if _, e := store.DB.Exec("INSERT INTO identity_grants VALUES(?,?,?,?)", scope.Tenant, scope.App, subject, "member:claim"); e != nil {
				t.Fatal(e)
			}
		}
		statement = call("GET", "/v1/statements/{id}", "/v1/statements/"+sid, "operator", nil, 200)
		frozenOrder := prepare("1000", 201)
		action("RELEASE", frozenOrder, "publish-freeze-check")
		var entitlement string
		if e := store.DB.QueryRow("SELECT entitlement_id FROM ledger_entitlements WHERE tenant_id=? LIMIT 1", scope.Tenant).Scan(&entitlement); e != nil {
			t.Fatal(e)
		}
		call("POST", "/v1/risk-holds", "/v1/risk-holds", "operator", map[string]any{"entitlement_id": entitlement, "reason_code": "CLAIM_TEST", "evidence_ref": "test-freeze"}, 201)
		call("POST", "/v1/me/commission-orders/{id}/claim", "/v1/me/commission-orders/"+field(frozenOrder, "instruction_id")+"/claim", "member", map[string]any{"expected_revision": 1}, 409)
	}
	var paid, available, target string
	if e := store.DB.QueryRow("SELECT CAST(SUM(paid) AS CHAR),CAST(SUM(available) AS CHAR),CAST(SUM(target) AS CHAR) FROM ledger_entitlements WHERE tenant_id=?", scope.Tenant).Scan(&paid, &available, &target); e != nil || paid != "5000" || available != "5000" || target != "10000" {
		t.Fatal(paid, available, target, e)
	}
	call("GET", "/v1/payout-orders", "/v1/payout-orders?statement_id="+sid, "operator", nil, 200)
	if _, e := store.DB.Exec("DELETE FROM identity_grants WHERE tenant_id=? AND subject_id='local-approver' AND permission='statements:approve'", scope.Tenant); e != nil {
		t.Fatal(e)
	}
	callRaw("POST", "/v1/commission-review-batches", "/v1/commission-review-batches", "approver", "review-batch", raw, nil, 403)
	t.Log("review list, per-item batch outcome, immutable notes, edit/cancel, concurrent dispatch20, partial payout conservation and fresh authorization PASS")
}
