package runtime

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	agents "github.com/ex-business/ExCommission/internal/agents/adapters"
	agent "github.com/ex-business/ExCommission/internal/agents/domain"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	"github.com/ex-business/ExCommission/internal/platform/ports"
	userrepo "github.com/ex-business/ExCommission/internal/users/adapters"
	user "github.com/ex-business/ExCommission/internal/users/domain"
)

func TestBusinessUsersRelationsAndApproval(t *testing.T) {
	address := os.Getenv("TEST_MYSQL_ADDRESS")
	if address == "" {
		t.Skip("requires Docker MySQL")
	}
	store, err := mysql.Open(context.Background(), address, "excommission", os.Getenv("MYSQL_PASSWORD"), "excommission")
	if err != nil {
		t.Fatal(err)
	}
	defer store.DB.Close()
	if err = store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	tenant := "test-" + newID()
	scope := mysql.Scope{Tenant: tenant, App: "test"}
	defer func() {
		for _, table := range []string{"users_external", "user_sync_jobs", "agent_accounts", "agent_relation_spaces", "agent_relations", "agent_contracts", "workflow_requests", "identity_bootstraps", "identity_grants", "command_results"} {
			if _, err := store.DB.Exec("DELETE FROM "+table+" WHERE tenant_id=?", tenant); err != nil {
				t.Error(err)
			}
		}
	}()
	token, approveToken := strings.Repeat("a", 32), strings.Repeat("b", 32)
	t.Setenv("APP_MODE", "local")
	t.Setenv("LOCAL_OPERATOR_TOKEN", token)
	t.Setenv("LOCAL_APPROVER_TOKEN", approveToken)
	t.Setenv("LOCAL_CURSOR_KEY", strings.Repeat("c", 32))
	t.Setenv("LOCAL_TENANT", tenant)
	t.Setenv("LOCAL_APP", "test")
	file := filepath.Join(t.TempDir(), "users.json")
	if err = os.WriteFile(file, []byte(`[{"external_user_id":"A","display_name":"用户A"},{"external_user_id":"B","display_name":"用户B"},{"external_user_id":"C","display_name":"用户C"},{"external_user_id":"","display_name":"非法"}]`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LOCAL_USERS_FILE", file)
	t.Setenv("LOCAL_SOURCE_KEY", strings.Repeat("s", 32))
	handler, err := businessHandler(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path, credential, key string, input any) (int, map[string]any) {
		body, _ := json.Marshal(input)
		request := httptest.NewRequest(method, path, bytes.NewReader(body))
		if credential != "" {
			request.Header.Set("Authorization", "Bearer "+credential)
		}
		request.Header.Set("Idempotency-Key", key)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		var output map[string]any
		if err := json.Unmarshal(recorder.Body.Bytes(), &output); err != nil {
			t.Errorf("invalid response %d %s", recorder.Code, recorder.Body.String())
		}
		return recorder.Code, output
	}
	status, _ := call("POST", "/v1/agents", "", "x", map[string]any{})
	if status != 401 {
		t.Fatal("unauthenticated", status)
	}
	status, output := call("POST", "/v1/user-sync-jobs", token, "sync1", map[string]any{"source_id": "sandbox", "mapping_version_id": "1", "cursor": nil})
	if status != 202 {
		t.Fatalf("sync %d %+v", status, output)
	}
	job := output["data"].(map[string]any)
	if job["success_count"] != float64(3) || job["failure_count"] != float64(1) {
		t.Fatalf("counts %+v", job)
	}
	jobID := job["job_id"].(string)
	status, _ = call("GET", "/v1/user-sync-jobs/"+jobID, token, "", nil)
	if status != 200 {
		t.Fatal("job read", status)
	}
	ids := map[string]string{}
	for _, external := range []string{"A", "B", "C"} {
		var userID string
		if err = store.DB.QueryRow(`SELECT user_id FROM users_external WHERE tenant_id=? AND external_user_id=?`, tenant, external).Scan(&userID); err != nil {
			t.Fatal(err)
		}
		status, output = call("POST", "/v1/agents", token, "create-"+external, map[string]any{"user_id": userID, "display_name": external})
		if status != 200 {
			t.Fatalf("agent %d %+v", status, output)
		}
		ids[external] = output["data"].(map[string]any)["agent_id"].(string)
		status, _ = call("POST", "/v1/agents", token, "duplicate-"+external, map[string]any{"user_id": userID, "display_name": external})
		if status != 409 {
			t.Fatal("duplicate user", status)
		}
	}
	before := time.Now().UTC().Truncate(time.Microsecond)
	requestMove := func(child, parent, key string) string {
		status, out := call("POST", "/v1/relationship-change-requests", token, key, map[string]any{"agent_id": child, "new_parent_id": parent, "expected_revision": 0, "reason": "本地测试迁移"})
		if status != 200 {
			t.Fatalf("move %d %+v", status, out)
		}
		return out["data"].(map[string]any)["approval_id"].(string)
	}
	decision := func(id, credential, key string) (int, map[string]any) {
		status, out := call("GET", "/v1/approvals/"+id, token, "", nil)
		if status != 200 {
			t.Fatal("approval read", status)
		}
		a := out["data"].(map[string]any)
		return call("POST", "/v1/approvals/"+id+"/decisions", credential, key, map[string]any{"decision": "APPROVE", "expected_revision": a["revision"], "object_hash": a["object_hash"], "reason": "独立复核"})
	}
	approval := requestMove(ids["B"], ids["A"], "move-B-A")
	// 即使申请人临时切换到审批角色，也不能自审。
	if _, err = store.DB.Exec(`INSERT INTO identity_grants VALUES(?,?,?,?)`, tenant, "test", "local-operator", "relations:approve"); err != nil {
		t.Fatal(err)
	}
	status, _ = decision(approval, token, "self")
	if status != 403 {
		t.Fatal("self approval", status)
	}
	status, output = decision(approval, approveToken, "approve-B-A")
	if status != 200 || output["data"].(map[string]any)["execution_status"] != "SUCCEEDED" {
		t.Fatalf("approve %d %+v", status, output)
	}
	now := time.Now().UTC()
	repo := agents.Repository{}
	old, _, err := repo.Ancestors(context.Background(), store.DB, scope, ids["B"], before, 0, 64)
	if err != nil || len(old) != 0 {
		t.Fatalf("historical chain %+v %v", old, err)
	}
	current, _, err := repo.Ancestors(context.Background(), store.DB, scope, ids["B"], now, 0, 64)
	if err != nil || len(current) != 1 || current[0].AgentID != ids["A"] {
		t.Fatalf("current %+v %v", current, err)
	}
	// 迁移边界左闭右开。
	at := current[0].EffectiveFrom
	exact, _, err := repo.Ancestors(context.Background(), store.DB, scope, ids["B"], at, 0, 64)
	if err != nil || len(exact) != 1 {
		t.Fatal("inclusive boundary", err)
	}
	previous, _, err := repo.Ancestors(context.Background(), store.DB, scope, ids["B"], at.Add(-time.Microsecond), 0, 64)
	if err != nil || len(previous) != 0 {
		t.Fatal("previous boundary", err)
	}
	cycle := requestMove(ids["A"], ids["B"], "cycle")
	status, output = decision(cycle, approveToken, "cycle-decision")
	if status != 200 || output["data"].(map[string]any)["execution_status"] != "FAILED" {
		t.Fatalf("cycle accepted %+v", output)
	}
	// 新代理两边并发互绑，只允许一边成功。
	for _, id := range []string{"X", "Y"} {
		err = store.Within(context.Background(), func(tx ports.Executor) error {
			return repo.Create(context.Background(), tx, scope, id, "user-"+id, id, time.Now())
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, pair := range [][2]string{{"X", "Y"}, {"Y", "X"}} {
		wg.Go(func() {
			parent := pair[1]
			results <- store.Within(context.Background(), func(tx ports.Executor) error {
				return repo.Move(context.Background(), tx, scope, agent.RelationChange{AgentID: pair[0], NewParentID: &parent}, time.Now())
			})
		})
	}
	wg.Wait()
	close(results)
	success, failed := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else {
			failed++
		}
	}
	if success != 1 || failed != 1 {
		t.Fatalf("concurrent loop guard %d/%d", success, failed)
	}
	// 已接受同步的结果在来源停用后仍可按原键重放。
	t.Setenv("LOCAL_USERS_FILE", "")
	originalHandler := handler
	handler, err = businessHandler(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	status, output = call("POST", "/v1/user-sync-jobs", token, "sync1", map[string]any{"source_id": "sandbox", "mapping_version_id": "1", "cursor": nil})
	if status != 202 || output["data"].(map[string]any)["job_id"] != jobID {
		t.Fatalf("source-disabled replay %d %+v", status, output)
	}
	handler = originalHandler
	// 重新装配不能恢复显式撤销的权限。
	if _, err = store.DB.Exec(`DELETE FROM identity_grants WHERE tenant_id=? AND subject_id='local-operator' AND permission='users:sync'`, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err = businessHandler(context.Background(), store); err != nil {
		t.Fatal(err)
	}
	status, _ = call("POST", "/v1/user-sync-jobs", token, "revoked", map[string]any{"source_id": "sandbox", "mapping_version_id": "1", "cursor": nil})
	if status != 403 {
		t.Fatal("revocation reset by bootstrap", status)
	}
}

func TestDeepRelationPaginationAndContracts(t *testing.T) {
	address := os.Getenv("TEST_MYSQL_ADDRESS")
	if address == "" {
		t.Skip("requires Docker MySQL")
	}
	store, err := mysql.Open(context.Background(), address, "excommission", os.Getenv("MYSQL_PASSWORD"), "excommission")
	if err != nil {
		t.Fatal(err)
	}
	defer store.DB.Close()
	if err = store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	scope := mysql.Scope{Tenant: "test-" + newID(), App: "app"}
	defer func() {
		for _, table := range []string{"agent_accounts", "agent_relations", "agent_relation_spaces", "agent_contracts"} {
			_, _ = store.DB.Exec("DELETE FROM "+table+" WHERE tenant_id=?", scope.Tenant)
		}
	}()
	repo := agents.Repository{}
	ctx := context.Background()
	base := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	if err = store.Within(ctx, func(tx ports.Executor) error {
		for i := 0; i < 71; i++ {
			id := strconv.Itoa(i)
			if err := repo.Create(ctx, tx, scope, id, "user-"+id, id, base); err != nil {
				return err
			}
		}
		for i := 0; i < 70; i++ {
			parent := strconv.Itoa(i + 1)
			if err := repo.Move(ctx, tx, scope, agent.RelationChange{AgentID: strconv.Itoa(i), NewParentID: &parent}, time.Now()); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	first, more, err := repo.Ancestors(ctx, store.DB, scope, "0", at, 0, 64)
	if err != nil || len(first) != 64 || !more {
		t.Fatalf("first page %d %t %v", len(first), more, err)
	}
	rest, more, err := repo.Ancestors(ctx, store.DB, scope, "0", at, 64, 64)
	if err != nil || len(rest) != 6 || more || rest[0].Depth != 65 || rest[5].Depth != 70 {
		t.Fatalf("second page %+v %t %v", rest, more, err)
	}
	end := at.Add(time.Hour)
	contract := agents.Contract{ID: "contract1", AgentID: "0", PlanID: "plan1", Evidence: "local-evidence", From: at, To: &end}
	if err = store.Within(ctx, func(tx ports.Executor) error { return repo.AddContract(ctx, tx, scope, contract) }); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.ContractAt(ctx, store.DB, scope, "0", "plan1", at); err != nil {
		t.Fatal("contract start", err)
	}
	if _, err = repo.ContractAt(ctx, store.DB, scope, "0", "plan1", end); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("contract end must exclude", err)
	}
	if err = store.Within(ctx, func(tx ports.Executor) error {
		contract.ID = "overlap"
		return repo.AddContract(ctx, tx, scope, contract)
	}); err == nil {
		t.Fatal("overlapping contract accepted")
	}
	// 同外部ID在不同来源和应用之间保持独立。
	for _, source := range []string{"source1", "source2"} {
		if err = store.Within(ctx, func(tx ports.Executor) error {
			_, err := userrepo.Upsert(ctx, tx, scope, source, user.Record{ExternalID: "same", DisplayName: "name"}, at)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	store.DB.QueryRow(`SELECT COUNT(*) FROM users_external WHERE tenant_id=?`, scope.Tenant).Scan(&count)
	if count != 2 {
		t.Fatal("source identity merged")
	}
	_, _ = store.DB.Exec(`DELETE FROM users_external WHERE tenant_id=?`, scope.Tenant)
}
