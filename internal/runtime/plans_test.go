package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestPlanHTTPPublicationRejectionAndRollback(t *testing.T) {
	if os.Getenv("TEST_MYSQL_ADDRESS") == "" {
		t.Skip("Docker MySQL required")
	}
	store, err := mysql.Open(context.Background(), os.Getenv("TEST_MYSQL_ADDRESS"), "excommission", os.Getenv("MYSQL_PASSWORD"), "excommission")
	if err != nil {
		t.Fatal(err)
	}
	defer store.DB.Close()
	if err = store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	tenant := "test-http-plan-" + newID()
	defer func() {
		for _, table := range []string{"plan_drafts", "plan_versions", "plan_route_spaces", "plan_simulations", "workflow_requests", "command_results", "identity_grants", "identity_bootstraps"} {
			_, _ = store.DB.Exec("DELETE FROM "+table+" WHERE tenant_id=?", tenant)
		}
	}()
	token, reviewer := strings.Repeat("d", 32), strings.Repeat("e", 32)
	for key, value := range map[string]string{"APP_MODE": "local", "LOCAL_TENANT": tenant, "LOCAL_APP": "app", "LOCAL_OPERATOR_TOKEN": token, "LOCAL_APPROVER_TOKEN": reviewer, "LOCAL_CURSOR_KEY": strings.Repeat("f", 32), "LOCAL_USERS_FILE": ""} {
		t.Setenv(key, value)
	}
	t.Setenv("LOCAL_SOURCE_KEY", strings.Repeat("s", 32))
	handler, err := businessHandler(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path, credential, key string, input any) (int, map[string]any) {
		body, _ := json.Marshal(input)
		request := httptest.NewRequest(method, path, bytes.NewReader(body))
		request.Header.Set("Authorization", "Bearer "+credential)
		request.Header.Set("Idempotency-Key", key)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		var envelope map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("response %d %s", response.Code, response.Body.String())
		}
		return response.Code, envelope
	}
	must := func(status int, output map[string]any) map[string]any {
		if status >= 400 {
			t.Fatalf("HTTP %d %+v", status, output)
		}
		return output["data"].(map[string]any)
	}
	raw, err := os.ReadFile("../../conformance/v1/plan-percent.json")
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err = json.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	status, out := call("POST", "/v1/plans", token, "create", config)
	draft := must(status, out)
	hash := draft["draft_hash"].(string)
	at := time.Now().UTC().Truncate(time.Microsecond)
	simulate := func(planHash, key string) string {
		status, out := call("POST", "/v1/simulations", token, key, map[string]any{"draft_hash": planHash, "business_facts": map[string]any{"basis_amount": "1000", "basis_currency": "USD", "product_code": "BTC-USDT", "scene": "SPOT", "confirmed": true, "business_time": at}, "ancestors": []any{map[string]any{"agent_id": "A1", "depth": 1, "grade_id": "G0", "eligible": true, "status": "ACTIVE"}}, "qualification_facts": map[string]any{"A1": map[string]any{"eligibility": "ELIGIBLE", "effective_from": at.Add(-time.Hour), "effective_to": nil}}})
		data := must(status, out)
		result := data["result"].(map[string]any)
		if result["status"] != "SUCCEEDED" {
			t.Fatalf("trial %+v", result)
		}
		return data["simulation_id"].(string)
	}
	simulation := simulate(hash, "simulation")
	requestPublication := func(planID, hash, simulation, key string, revision any) string {
		status, out := call("POST", "/v1/plans/"+planID+"/publication-requests", token, key, map[string]any{"expected_revision": revision, "draft_hash": hash, "simulation_id": simulation, "effective_from": at.Add(time.Hour), "reason": "本地发布测试"})
		return must(status, out)["approval_id"].(string)
	}
	approval := requestPublication("P1", hash, simulation, "publication", 0)
	status, _ = call("PUT", "/v1/plans/P1/draft", token, "pending-edit", map[string]any{"draft": config, "expected_revision": 0})
	if status != 409 {
		t.Fatal("pending draft editable", status)
	}
	decide := func(approvalID, decision, key string) map[string]any {
		status, out := call("GET", "/v1/approvals/"+approvalID, token, "", nil)
		record := must(status, out)
		status, out = call("POST", "/v1/approvals/"+approvalID+"/decisions", reviewer, key, map[string]any{"decision": decision, "expected_revision": record["revision"], "object_hash": record["object_hash"], "reason": "独立复核"})
		return must(status, out)
	}
	if decided := decide(approval, "APPROVE", "decision"); decided["execution_status"] != "SUCCEEDED" {
		t.Fatalf("publication failed %+v", decided)
	}
	status, out = call("GET", "/v1/plans/P1/versions", token, "", nil)
	versions := must(status, out)["items"].([]any)
	if len(versions) != 1 {
		t.Fatal("missing published version")
	}
	firstID := versions[0].(map[string]any)["version_id"].(string)
	config["name"] = "修改后方案"
	status, out = call("PUT", "/v1/plans/P1/draft", token, "edit", map[string]any{"draft": config, "expected_revision": 0})
	edited := must(status, out)
	newHash := edited["draft_hash"].(string)
	newSimulation := simulate(newHash, "sim-2")
	rejection := requestPublication("P1", newHash, newSimulation, "request-reject", 1)
	decide(rejection, "REJECT", "reject")
	status, out = call("GET", "/v1/plans/P1", token, "", nil)
	rejected := must(status, out)
	if rejected["status"] != "DRAFT" || rejected["revision"] != float64(2) {
		t.Fatalf("rejected draft %+v", rejected)
	}
	status, out = call("POST", "/v1/plans/P1/rollback-requests", token, "rollback", map[string]any{"target_version_id": firstID, "effective_from": at.Add(2 * time.Hour), "reason": "回退生成新版本", "expected_revision": 2})
	rollback := must(status, out)["approval_id"].(string)
	if data := decide(rollback, "APPROVE", "rollback-decision"); data["execution_status"] != "SUCCEEDED" {
		t.Fatalf("rollback %+v", data)
	}
	status, out = call("GET", "/v1/plans/P1/versions?limit=1", token, "", nil)
	page := must(status, out)
	cursor, ok := page["next_cursor"].(string)
	if !ok || len(page["items"].([]any)) != 1 {
		t.Fatal("version pagination")
	}
	status, out = call("GET", "/v1/plans/P1/versions?limit=1&cursor="+cursor, token, "", nil)
	page = must(status, out)
	if page["next_cursor"] != nil || len(page["items"].([]any)) != 1 {
		t.Fatal("version second page")
	}
	// 另一个方案不能占用相同路由，执行失败后草稿可修正且不冒充已发布。
	config["plan_id"] = "P2"
	config["revision"] = 0
	status, out = call("POST", "/v1/plans", token, "create-p2", config)
	p2 := must(status, out)
	p2hash := p2["draft_hash"].(string)
	p2sim := simulate(p2hash, "sim-p2")
	p2approval := requestPublication("P2", p2hash, p2sim, "publish-p2", 0)
	if data := decide(p2approval, "APPROVE", "p2-decision"); data["execution_status"] != "FAILED" {
		t.Fatal("overlapping route published")
	}
	status, out = call("GET", "/v1/plans/P2", token, "", nil)
	if data := must(status, out); data["status"] != "DRAFT" {
		t.Fatal("failed publication stranded draft")
	}
	config["route"].(map[string]any)["product_code"] = "ETH-USDT"
	status, out = call("PUT", "/v1/plans/P2/draft", token, "repair-p2", map[string]any{"draft": config, "expected_revision": 0})
	must(status, out)
	// 显式非祖先加权候选冻结资格，失格份额保留给平台。
	config["plan_id"] = "W1"
	config["revision"] = 0
	config["route"].(map[string]any)["product_code"] = "BTC-USDT"
	config["event_budget_minor"] = "100"
	config["rules"] = []any{map[string]any{"id": "W", "algorithm_id": "WEIGHTED", "algorithm_version": "1", "recipient": "EXPLICIT", "condition": map[string]any{"logic": "AND", "children": []any{}}, "parameters": map[string]any{"pool_minor": "100", "reward_currency": "USD", "beneficiaries": []any{map[string]any{"beneficiary_id": "A", "weight": "1"}, map[string]any{"beneficiary_id": "B", "weight": "1"}, map[string]any{"beneficiary_id": "C", "weight": "1"}}}, "funding_limit_ppm": nil, "per_beneficiary_cap_minor": nil, "per_event_total_cap_minor": nil, "frequency": "ONCE_PER_BUSINESS_RULE"}}
	status, out = call("POST", "/v1/plans", token, "weighted-create", config)
	weightedHash := must(status, out)["draft_hash"].(string)
	qualification := map[string]any{}
	for _, id := range []string{"A", "B", "C"} {
		eligible := "ELIGIBLE"
		if id == "A" {
			eligible = "INELIGIBLE"
		}
		qualification[id] = map[string]any{"eligibility": eligible, "effective_from": at.Add(-time.Hour), "effective_to": nil, "status": "ACTIVE"}
	}
	status, out = call("POST", "/v1/simulations", token, "weighted-sim", map[string]any{"draft_hash": weightedHash, "business_facts": map[string]any{"basis_amount": "1000", "basis_currency": "USD", "product_code": "BTC-USDT", "scene": "SPOT", "confirmed": true, "business_time": at}, "ancestors": []any{}, "qualification_facts": qualification})
	weighted := must(status, out)
	weightedResult := weighted["result"].(map[string]any)
	items := weightedResult["items"].([]any)
	if weightedResult["status"] != "SUCCEEDED" || len(items) != 2 || items[0].(map[string]any)["minor_units"] != "33" || items[1].(map[string]any)["minor_units"] != "33" || items[0].(map[string]any)["depth"] != float64(0) {
		t.Fatalf("weighted HTTP result %+v", weightedResult)
	}
	// 模拟允许虚构事实，但正式发布仍要求显式代理在作用域内存在。
	status, _ = call("POST", "/v1/plans/W1/publication-requests", token, "weighted-publish-missing-agents", map[string]any{"expected_revision": 0, "draft_hash": weightedHash, "simulation_id": weighted["simulation_id"], "effective_from": at.Add(time.Hour), "reason": "候选存在性门禁"})
	if status != 404 {
		t.Fatalf("published nonexistent explicit beneficiaries: %d", status)
	}

}
