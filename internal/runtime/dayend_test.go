package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	agents "github.com/ex-business/ExCommission/internal/agents/adapters"
	agent "github.com/ex-business/ExCommission/internal/agents/domain"
	calc "github.com/ex-business/ExCommission/internal/calculation/domain"
	dayrepo "github.com/ex-business/ExCommission/internal/dayend/adapters"
	dayend "github.com/ex-business/ExCommission/internal/dayend/application"
	identity "github.com/ex-business/ExCommission/internal/identity/domain"
	event "github.com/ex-business/ExCommission/internal/integration/domain"
	plans "github.com/ex-business/ExCommission/internal/plans/adapters"
	plan "github.com/ex-business/ExCommission/internal/plans/domain"
	catalog "github.com/ex-business/ExCommission/internal/platform/adapters/catalog"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	qualification "github.com/ex-business/ExCommission/internal/qualification/adapters"
	qual "github.com/ex-business/ExCommission/internal/qualification/domain"
	users "github.com/ex-business/ExCommission/internal/users/adapters"
	user "github.com/ex-business/ExCommission/internal/users/domain"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDayEndCommissionPipeline(t *testing.T) {
	for _, c := range []struct {
		name             string
		members, volumes []int
		grade, amount    string
	}{
		{"三层统计", []int{1, 2, 3}, []int{1, 2, 3}, "G2", "300"},
		{"只统计直属有效会员", []int{1}, []int{1, 2, 3}, "G1", "200"},
		{"只统计直属业务量", []int{1, 2, 3}, []int{1}, "G1", "200"},
	} {
		t.Run(c.name, func(t *testing.T) { runDayEndPipeline(t, c.members, c.volumes, c.grade, c.amount) })
	}
}
func runDayEndPipeline(t *testing.T, memberDepths, volumeDepths []int, expectedAGrade, expectedAAmount string) {
	if os.Getenv("TEST_MYSQL_ADDRESS") == "" {
		t.Skip("需要本地MySQL集成环境")
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
	scope := foundation.Scope{Tenant: "test-dayend-" + newID(), App: "commission"}
	defer func() {
		rows, e := store.DB.Query(`SELECT TABLE_NAME FROM information_schema.columns WHERE TABLE_SCHEMA=DATABASE() AND COLUMN_NAME='tenant_id'`)
		if e != nil {
			t.Error(e)
			return
		}
		tables := []string{}
		for rows.Next() {
			var name string
			if e = rows.Scan(&name); e != nil {
				t.Error(e)
				return
			}
			tables = append(tables, name)
		}
		rows.Close()
		for _, name := range tables {
			if _, e = store.DB.Exec("DELETE FROM `"+name+"` WHERE tenant_id=?", scope.Tenant); e != nil {
				t.Error(e)
			}
		}
	}()
	start := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	now := start.Add(12 * time.Hour)
	old := timeSource
	timeSource = func() time.Time { return now }
	defer func() { timeSource = old }()
	for k, v := range map[string]string{"APP_MODE": "local", "LOCAL_TENANT": scope.Tenant, "LOCAL_APP": scope.App, "LOCAL_OPERATOR_TOKEN": strings.Repeat("o", 32), "LOCAL_APPROVER_TOKEN": strings.Repeat("a", 32), "LOCAL_CURSOR_KEY": strings.Repeat("c", 32), "LOCAL_SOURCE_KEY": strings.Repeat("s", 32), "LOCAL_PAYMENT_KEY": strings.Repeat("p", 32), "LOCAL_DAYEND_ENABLED": "true", "LOCAL_REGISTRATION_ENABLED": "false", "LOCAL_QUALIFICATION_ENABLED": "false", "AUTH_LOCAL_ACCOUNTS_FILE": "", "AUTH_EXTERNAL_FILE": ""} {
		t.Setenv(k, v)
	}
	raw, err := os.ReadFile("../../conformance/v1/plan-percent.json")
	if err != nil {
		t.Fatal(err)
	}
	var config plan.Config
	if err = json.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	config.AgentPolicy.GradeWindow = "BUSINESS_DAY"
	config.AgentPolicy.DayEnd = &plan.DayEndPolicy{Timezone: "UTC", SourceID: "sandbox", QualificationPolicyID: "effective", MemberDepths: memberDepths, VolumeDepths: volumeDepths}
	config.Grades = append(config.Grades, plan.Grade{ID: "G1", Rank: 1, Conditions: []plan.GradeCondition{{Metric: "EFFECTIVE_MEMBER_COUNT", Operator: "GTE", Value: "1", Unit: "COUNT"}}}, plan.Grade{ID: "G2", Rank: 2, Conditions: []plan.GradeCondition{{Metric: "EFFECTIVE_MEMBER_COUNT", Operator: "GTE", Value: "2", Unit: "COUNT"}, {Metric: "TEAM_VALID_VOLUME", Operator: "GTE", Value: "100", Unit: "USD"}}})
	config.Rules[0].Algorithm = "INDEPENDENT"
	config.Rules[0].Recipient = "ANCESTORS"
	config.Rules[0].Parameters = plan.Parameters{}
	for g, rate := range map[string]string{"G0": "10000", "G1": "20000", "G2": "30000"} {
		for depth := 1; depth <= 3; depth++ {
			d := depth
			config.Rules[0].Parameters.Rates = append(config.Rules[0].Parameters.Rates, plan.Rate{Product: config.Route.Product, Grade: g, Depth: &d, PPM: rate})
		}
	}
	body, _ := json.Marshal(config)
	validator, err := plans.NewValidator(foundation.CurrencyExponents(), map[string]bool{"UTC-v1": true})
	if err != nil {
		t.Fatal(err)
	}
	config, canonical, err := validator.Decode(body)
	if err != nil {
		t.Fatal(err)
	}
	before := start.Add(-time.Hour)
	// 通过业务仓储建立隔离测试夹具；生产发布仍沿用模拟和独立审批门禁。
	err = store.Within(ctx, func(tx db.Executor) error {
		if e := (catalog.Repository{}).Bootstrap(ctx, tx, scope); e != nil {
			return e
		}
		repo := plans.Repository{}
		hash := plans.Hash(canonical)
		if e := repo.Create(ctx, tx, scope, config, canonical, hash, before); e != nil {
			return e
		}
		if e := repo.SetPending(ctx, tx, scope, config.PlanID, 0, hash); e != nil {
			return e
		}
		if e := repo.Publish(ctx, tx, scope, config.PlanID, 0, hash, "daily-v1", start, before); e != nil {
			return e
		}
		parent := ""
		for _, id := range []string{"A", "B", "C", "X"} {
			uid, e := (users.Repository{}).Upsert(ctx, tx, scope, "sandbox", user.Record{ExternalID: id, DisplayName: id}, before)
			if e != nil {
				return e
			}
			var p *string
			if parent != "" {
				copy := parent
				p = &copy
			}
			if e = (agents.Repository{}).RegisterInitial(ctx, tx, scope, id, uid, id, p, before); e != nil {
				return e
			}
			if e = (agents.Repository{}).ApplyGrade(ctx, tx, scope, agent.GradeChange{AgentID: id, PlanID: config.PlanID, GradeID: "G0", Eligibility: "ELIGIBLE"}, "MANUAL", "verified-fixture", before); e != nil {
				return e
			}
			parent = id
		}
		qr := qualification.Repository{}
		if e := qr.SavePolicy(ctx, tx, scope, qual.Policy{ID: "effective", Source: "sandbox", Currency: "USD", Deposit: "1", Bet: "1", Venues: []string{"SPORTS"}}, before); e != nil {
			return e
		}
		for _, id := range []string{"B", "C"} {
			if e := qr.LockMember(ctx, tx, scope, "effective", "sandbox", id); e != nil {
				return e
			}
			at := start.Add(time.Hour)
			if e := qr.SaveStatus(ctx, tx, scope, qual.Status{PolicyID: "effective", Source: "sandbox", ExternalUser: id, AgentID: id, Currency: "USD", Effective: true, QualifiedAt: &at, Revision: 1}, now); e != nil {
				return e
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	tasks := taskRepository(store)
	create := func(id string) {
		t.Helper()
		yes := true
		e := event.Event{Schema: "1", Source: "sandbox", ID: id, BusinessID: id, Type: config.Route.EventType, Time: start.Add(2 * time.Hour), ExternalUser: "X", Product: config.Route.Product, Scene: config.Route.Scene, Revision: "1", Payload: event.Payload{Basis: "100", Currency: "USD", Confirmed: &yes}}
		raw, _ := json.Marshal(e)
		if err := store.Within(ctx, func(tx db.Executor) error {
			return tasks.Create(ctx, tx, scope, calc.Task{ID: id, EventID: "event-" + id, Source: "sandbox", SourceEvent: id, BusinessID: id, EventType: e.Type, BusinessTime: e.Time, EventBody: raw}, now)
		}); err != nil {
			t.Fatal(err)
		}
	}
	create("order-1")
	processor := buildWorker(store)
	if found, e := processor.RunOnce(ctx, scope); e != nil || !found {
		t.Fatal(found, e)
	}
	task, err := tasks.Get(ctx, store.DB, scope, "order-1")
	if err != nil || task.Status != "SUSPENDED" || task.Reason == nil || *task.Reason != "DAYEND_PENDING" {
		t.Fatalf("未等日终 %+v %v", task, err)
	}
	service := dayend.Service{Commands: store, Repo: dayrepo.Repository{}, Plans: plans.Repository{}, Tasks: tasks, Resolver: businessResolver(), Subjects: agents.Repository{}, Relations: agents.Repository{}, Members: qualification.Repository{}, Policies: qualification.Repository{}, Now: clockUTC}
	p := identity.Principal{Tenant: scope.Tenant, App: scope.App, Subject: "fixture"}
	input := dayend.Request{PlanID: config.PlanID, VersionID: "daily-v1", Day: "2026-10-14", Watermark: "source-close-20261014"}
	if result, err := service.Freeze(ctx, p, input, "early", "early"); err == nil && result.Status < 400 {
		t.Fatal("日终前不得冻结")
	}
	now = start.Add(25 * time.Hour)
	var wg sync.WaitGroup
	failures := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			result, e := service.Freeze(ctx, p, input, fmt.Sprintf("freeze-%d", i), "freeze")
			if e == nil && result.Status != 200 {
				e = fmt.Errorf("冻结失败 %d %s", result.Status, result.Body)
			}
			failures <- e
		}(i)
	}
	wg.Wait()
	close(failures)
	for e := range failures {
		if e != nil {
			t.Fatal(e)
		}
	}
	frozen, err := (dayrepo.Repository{}).Get(ctx, store.DB, scope, config.PlanID, input.Day)
	if err != nil {
		t.Fatal(err)
	}
	if frozen.Agents["A"].Grade != expectedAGrade || frozen.Agents["B"].Grade != "G1" || frozen.Agents["C"].Grade != "G0" {
		t.Fatalf("各受益人等级错误 %+v", frozen.Agents)
	}
	if len(volumeDepths) == 3 && frozen.Agents["A"].Metrics["TEAM_VALID_VOLUME"] != "100.00000000" {
		t.Fatal("未达标用户业务量未累计", frozen)
	}
	if found, e := processor.RunOnce(ctx, scope); e != nil || !found {
		t.Fatal(found, e)
	}
	task, err = tasks.Get(ctx, store.DB, scope, "order-1")
	if err != nil || task.Status != "SUCCEEDED" {
		t.Fatalf("计佣失败 %+v %v", task, err)
	}
	var snapshot calc.Input
	if err = json.Unmarshal(task.Snapshot, &snapshot); err != nil {
		t.Fatal(err)
	}
	var result calc.Result
	if err = json.Unmarshal(task.Result, &result); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"A": expectedAAmount, "B": "200", "C": "100"}
	if len(result.Items) != 3 {
		t.Fatal(result)
	}
	for _, item := range result.Items {
		if item.Minor != want[item.BeneficiaryID] {
			t.Fatal(item)
		}
	}
	// 审批资格证据保留旧等级；计算等级来自独立日终证据；跳过中间受益人不压缩层级。
	if snapshot.QualificationFacts["A"].GradeID != "G0" || snapshot.DayEnd == nil || snapshot.DayEnd.Grades["A"] != expectedAGrade {
		t.Fatal("资格与日终证据混淆", snapshot)
	}
	for i := range snapshot.Ancestors {
		if snapshot.Ancestors[i].ID == "B" {
			no := false
			snapshot.Ancestors[i].Eligible = &no
		}
	}
	skipped := calc.Calculate(snapshot)
	if len(skipped.Items) != 2 {
		t.Fatal(skipped)
	}
	for _, item := range skipped.Items {
		if item.BeneficiaryID == "A" && (item.Depth != 3 || item.Minor != expectedAAmount) {
			t.Fatal("层级压缩", item)
		}
	}
	var count int
	if err = store.DB.QueryRow(`SELECT COUNT(*) FROM ledger_entitlements WHERE tenant_id=? AND app_id=?`, scope.Tenant, scope.App).Scan(&count); err != nil || count != 3 {
		t.Fatal(count, err)
	}
	if _, err = service.Freeze(ctx, p, input, "freeze-again", "replay"); err != nil {
		t.Fatal(err)
	}
	if found, e := processor.RunOnce(ctx, scope); e != nil || found {
		t.Fatal("重放产生重复任务", found, e)
	}
	create("late-order")
	if found, e := processor.RunOnce(ctx, scope); e != nil || !found {
		t.Fatal(found, e)
	}
	late, err := tasks.Get(ctx, store.DB, scope, "late-order")
	if err != nil || late.Reason == nil || *late.Reason != "DAYEND_LATE_FACT" {
		t.Fatal("迟到订单未挂起", late, err)
	}
	// 验证HTTP启用、鉴权和快照查询，凭据只用于随机租户。
	handler, err := businessHandler(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	for _, credential := range []string{"", strings.Repeat("o", 32)} {
		req := httptest.NewRequest("GET", "/v1/day-end-freezes?plan_id=P1&business_day=2026-10-14", bytes.NewReader(nil))
		if credential != "" {
			req.Header.Set("Authorization", "Bearer "+credential)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		expected := 200
		if credential == "" {
			expected = 401
		}
		if rec.Code != expected {
			t.Fatalf("HTTP %d %s", rec.Code, rec.Body.String())
		}
	}
}
