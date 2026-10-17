package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDailySettlementIsRegisteredAndStatusIsAuthorized(t *testing.T) {
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
	tenant := "test-scheduled-runtime-" + newID()
	defer func() {
		for _, table := range []string{"settlement_schedule", "settlement_runs", "settlement_spaces", "audit_entries", "identity_grants", "identity_bootstraps", "platform_catalog_versions", "platform_component_states"} {
			if _, e := store.DB.Exec("DELETE FROM "+table+" WHERE tenant_id=?", tenant); e != nil {
				t.Error(e)
			}
		}
	}()
	for key, value := range map[string]string{"APP_MODE": "local", "LOCAL_TENANT": tenant, "LOCAL_APP": "app", "LOCAL_OPERATOR_TOKEN": strings.Repeat("o", 32), "LOCAL_APPROVER_TOKEN": strings.Repeat("a", 32), "LOCAL_CURSOR_KEY": strings.Repeat("c", 32), "LOCAL_SOURCE_KEY": strings.Repeat("s", 32), "LOCAL_PAYMENT_KEY": strings.Repeat("p", 32), "AUTH_LOCAL_ACCOUNTS_FILE": "", "AUTH_EXTERNAL_FILE": "", "LOCAL_USERS_FILE": ""} {
		t.Setenv(key, value)
	}
	var clock atomic.Int64
	clock.Store(time.Date(2026, 10, 3, 0, 59, 59, 0, time.UTC).UnixNano())
	old := timeSource
	timeSource = func() time.Time { return time.Unix(0, clock.Load()).UTC() }
	defer func() { timeSource = old }()
	handler, err := businessHandler(ctx, store)
	if err != nil {
		t.Fatal(err)
	}

	spec, err := os.ReadFile("../../contracts/v1/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(spec))
	if err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	if err = compiler.AddResource("urn:excommission:openapi", document); err != nil {
		t.Fatal(err)
	}
	statusSchema, err := compiler.Compile("urn:excommission:openapi#/paths/~1v1~1settlement-schedule/get/responses/200/content/application~1json/schema")
	if err != nil {
		t.Fatal(err)
	}
	get := func(auth bool, want int) map[string]any {
		t.Helper()
		r := httptest.NewRequest("GET", "/v1/settlement-schedule", nil)
		if auth {
			r.Header.Set("Authorization", "Bearer "+strings.Repeat("o", 32))
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatal(w.Code, w.Body.String())
		}

		if want == 200 {
			instance, e := jsonschema.UnmarshalJSON(bytes.NewReader(w.Body.Bytes()))
			if e != nil {
				t.Fatal(e)
			}
			if e = statusSchema.Validate(instance); e != nil {
				t.Fatal("status contract mismatch", e)
			}
		}
		var out struct {
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out.Data
	}
	get(false, 401)
	if v := get(true, 200); v["initialized"] != false || v["next_period"] != nil {
		t.Fatal(v)
	}
	jobs := maintenanceJobs(store)
	var run func(context.Context) error
	for _, job := range jobs {
		if job.name == "settlement.daily" {
			run = job.run
		}
	}
	if run == nil {
		t.Fatal("scheduler missing from worker jobs")
	}
	if err = run(ctx); err != nil {
		t.Fatal(err)
	}
	if v := get(true, 200); v["next_period"] != "2026-10-02" || v["next_due_at"] != "2026-10-03T01:00:00Z" {
		t.Fatal(v)
	}
	clock.Add(int64(time.Second))
	if err = run(ctx); err != nil {
		t.Fatal(err)
	}
	v := get(true, 200)
	if v["next_period"] != "2026-10-03" || v["last_result"] == nil {
		t.Fatal(v)
	}
	for _, job := range maintenanceJobs(store) {
		if job.name == "settlement.daily" {
			if err = job.run(ctx); err != nil {
				t.Fatal(err)
			}
		}
	}
	var count int
	if err = store.DB.QueryRow("SELECT COUNT(*) FROM settlement_runs WHERE tenant_id=?", tenant).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	if err = store.DB.QueryRow("SELECT COUNT(*) FROM audit_entries WHERE tenant_id=? AND action='settlement.daily'", tenant).Scan(&count); err != nil || count != 1 {
		t.Fatal("polling generated duplicate audit", count, err)
	}
	t.Log("registered worker task, scoped status, 01:00 boundary and restart replay PASS")
}
