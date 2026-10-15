package adapters_test

import (
	"context"
	"encoding/json"
	agents "github.com/ex-business/ExCommission/internal/agents/adapters"
	agent "github.com/ex-business/ExCommission/internal/agents/domain"
	calculation "github.com/ex-business/ExCommission/internal/calculation/domain"
	identity "github.com/ex-business/ExCommission/internal/identity/domain"
	repo "github.com/ex-business/ExCommission/internal/plans/adapters"
	service "github.com/ex-business/ExCommission/internal/plans/application"
	plan "github.com/ex-business/ExCommission/internal/plans/domain"
	planports "github.com/ex-business/ExCommission/internal/plans/ports"
	db "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	"github.com/ex-business/ExCommission/internal/platform/ports"
	workflowrepo "github.com/ex-business/ExCommission/internal/workflow/adapters"
	workflow "github.com/ex-business/ExCommission/internal/workflow/application"
	approval "github.com/ex-business/ExCommission/internal/workflow/domain"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPlanLifecycleAndGrades(t *testing.T) {
	if os.Getenv("TEST_MYSQL_ADDRESS") == "" {
		t.Skip("Docker MySQL required")
	}
	ctx := context.Background()
	store, err := db.Open(ctx, os.Getenv("TEST_MYSQL_ADDRESS"), "excommission", os.Getenv("MYSQL_PASSWORD"), "excommission")
	if err != nil {
		t.Fatal(err)
	}
	defer store.DB.Close()
	if err = store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	scope := foundation.Scope{Tenant: "test-plan-" + time.Now().UTC().Format("150405.000000000"), App: "app"}
	defer func() {
		for _, table := range []string{"plan_drafts", "plan_versions", "plan_simulations", "plan_route_spaces", "workflow_requests", "command_results", "agent_accounts", "agent_relations", "agent_grade_history", "agent_plan_states", "grade_evaluations"} {
			_, _ = store.DB.Exec("DELETE FROM "+table+" WHERE tenant_id=?", scope.Tenant)
		}
	}()
	body, err := os.ReadFile("../../../conformance/v1/plan-percent.json")
	if err != nil {
		t.Fatal(err)
	}
	validator, err := repo.NewValidator(map[string]int{"USD": 2}, map[string]bool{"UTC-v1": true})
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	nextID := 0
	id := func() string { nextID++; return time.Unix(int64(nextID), 0).UTC().Format("150405") }
	plans := service.Service{Commands: store, Query: store.DB, Repo: repo.Repository{}, Validator: validator, Simulations: repo.Simulations{}, Workflow: workflowrepo.Repository{}, Exponents: map[string]int{"USD": 2}, Now: func() time.Time { return clock }, ID: id}
	operator := identity.Principal{Tenant: scope.Tenant, App: scope.App, Subject: "operator"}
	approver := operator
	approver.Subject = "reviewer"
	decode := func(result foundation.CommandResult) map[string]any {
		var envelope struct {
			Data map[string]any `json:"data"`
		}
		if err = json.Unmarshal(result.Body, &envelope); err != nil {
			t.Fatal(err)
		}
		if result.Status >= 400 {
			t.Fatalf("command %d %s", result.Status, result.Body)
		}
		return envelope.Data
	}
	result, err := plans.Create(ctx, operator, body, "create", "request")
	if err != nil {
		t.Fatal(err)
	}
	created := decode(result)
	hash := created["draft_hash"].(string)
	var config plan.Config
	if err = json.Unmarshal(body, &config); err != nil {
		t.Fatal(err)
	}
	config.Grades = append(config.Grades, plan.Grade{ID: "G1", Rank: 1, Conditions: []plan.GradeCondition{{Metric: "PERSONAL_NET_BASIS", Operator: "GTE", Value: "100", Unit: "USD"}}})
	changed, _ := json.Marshal(config)
	result, err = plans.Update(ctx, operator, "P1", changed, 0, "edit", "request")
	if err != nil {
		t.Fatal(err)
	}
	edited := decode(result)
	if edited["draft_hash"] == hash {
		t.Fatal("edited hash unchanged")
	}
	hash = edited["draft_hash"].(string)
	confirmed := true
	eligible := true
	facts := calculation.Facts{Basis: "1000", Currency: "USD", Product: "BTC-USDT", Scene: "SPOT", Confirmed: &confirmed, BusinessTime: clock}
	trial := service.SimulationRequest{DraftHash: &hash, Facts: facts, Ancestors: []calculation.Actor{{ID: "A1", Depth: 1, Grade: "G0", Eligible: &eligible, Status: "ACTIVE"}}, Qualification: map[string]service.Qualification{"A1": {Eligibility: "ELIGIBLE", From: clock.Add(-time.Hour)}}}
	result, err = plans.Simulate(ctx, operator, trial, "simulate", "request")
	if err != nil {
		t.Fatal(err)
	}
	sim := decode(result)
	simulationID := sim["simulation_id"].(string)
	effective := clock.Add(24 * time.Hour)
	result, err = plans.RequestPublication(ctx, operator, "P1", service.Publication{Revision: 1, DraftHash: hash, SimulationID: simulationID, EffectiveFrom: effective, Reason: "发布首版"}, "publish", "request")
	if err != nil {
		t.Fatal(err)
	}
	approvalID := decode(result)["approval_id"].(string)
	workflowService := workflow.Service{Commands: store, Query: store.DB, Repo: workflowrepo.Repository{}, Now: func() time.Time { return clock }, Executors: map[string]workflow.Executor{}, Rejections: map[string]workflow.Executor{}}
	publish := func(ctx context.Context, tx ports.Executor, s foundation.Scope, r approval.Request, at time.Time) error {
		var input service.PublicationPayload
		if err = json.Unmarshal(r.Payload, &input); err != nil {
			return err
		}
		return plans.Repo.Publish(ctx, tx, s, input.PlanID, input.Revision, input.Hash, id(), input.EffectiveFrom, at)
	}
	workflowService.Executors["PLAN_PUBLISH"] = publish
	workflowService.Executors["PLAN_ROLLBACK"] = publish
	decide := func(approvalID, key string) {
		record, err := workflowService.Get(ctx, operator, approvalID)
		if err != nil {
			t.Fatal(err)
		}
		result, err := workflowService.Decide(ctx, approver, approvalID, workflow.Decision{Decision: "APPROVE", Revision: record.Revision, ObjectHash: record.ObjectHash, Reason: "独立审批"}, key, "request")
		if err != nil {
			t.Fatal(err)
		}
		data := decode(result)
		if data["execution_status"] != "SUCCEEDED" {
			t.Fatalf("execution %+v", data)
		}
	}
	decide(approvalID, "approve")
	if _, err = plans.Repo.Select(ctx, store.DB, scope, config.Route, effective.Add(-time.Microsecond)); err == nil {
		t.Fatal("version selected before start")
	}
	first, err := plans.Repo.Select(ctx, store.DB, scope, config.Route, effective)
	if err != nil || first.Number != 1 {
		t.Fatalf("start boundary %+v %v", first, err)
	}
	if err = store.Within(ctx, func(tx ports.Executor) error {
		return (agents.Repository{}).Create(ctx, tx, scope, "A1", "U1", "代理", clock.Add(-time.Hour))
	}); err != nil {
		t.Fatal(err)
	}
	clock = effective.Add(time.Hour)

	workflowService.Executors["GRADE"] = func(ctx context.Context, tx ports.Executor, scope foundation.Scope, r approval.Request, at time.Time) error {
		var change agent.GradeChange
		if err := json.Unmarshal(r.Payload, &change); err != nil {
			return err
		}
		return (agents.Repository{}).ApplyGrade(ctx, tx, scope, change, "MANUAL", r.ID, at)
	}
	manualGrades := service.GradeService{Service: plans, Grades: agents.Repository{}}
	result, err = manualGrades.RequestChange(ctx, operator, agent.GradeChange{AgentID: "A1", PlanID: "P1", GradeID: "G0", Eligibility: "ELIGIBLE", Reason: "加入方案"}, "grade-request", "request")
	if err != nil {
		t.Fatal(err)
	}
	decide(decode(result)["approval_id"].(string), "grade-approval")
	if err = store.Within(ctx, func(tx ports.Executor) error {
		return (agents.Repository{}).Create(ctx, tx, scope, "A2", "U2", "未加入代理", clock.Add(-time.Hour))
	}); err != nil {
		t.Fatal(err)
	}

	state, found, err := (agents.Repository{}).GradeAt(ctx, store.DB, scope, "A1", "P1", clock)
	if err != nil || !found || state.Eligibility != "ELIGIBLE" {
		t.Fatal("grade snapshot", err)
	}
	// 回退也生成新草稿、重新运行原试算并独立审批，原版本不覆盖。
	secondAt := clock.Add(time.Hour)
	result, err = plans.Rollback(ctx, operator, "P1", service.Rollback{VersionID: first.ID, Effective: secondAt, Reason: "回退为新版本", Revision: 1}, "rollback", "request")
	if err != nil {
		t.Fatal(err)
	}
	rollbackID := decode(result)["approval_id"].(string)
	decide(rollbackID, "approve-rollback")
	old, err := plans.Repo.Select(ctx, store.DB, scope, config.Route, secondAt.Add(-time.Microsecond))
	if err != nil || old.ID != first.ID {
		t.Fatal("old version overwritten", err)
	}
	newer, err := plans.Repo.Select(ctx, store.DB, scope, config.Route, secondAt)
	if err != nil || newer.ID == first.ID || newer.Number != 2 {
		t.Fatal("new version boundary", err)
	}
	// 旧摘要和旧试算不能发布新修订。
	result, err = plans.RequestPublication(ctx, operator, "P1", service.Publication{Revision: 1, DraftHash: hash, SimulationID: simulationID, EffectiveFrom: secondAt.Add(time.Hour), Reason: "旧试算"}, "stale-proof", "request")
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != 409 {
		t.Fatalf("stale proof %d", result.Status)
	}
	clock = time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	statisticsFile := filepath.Join(t.TempDir(), "statistics.json")
	statistics := planports.Statistics{PlanID: "P1", Period: "2026-09", Cutoff: clock, Watermark: "sandbox-grade-v1", Agents: map[string]map[string]string{"A1": {"PERSONAL_NET_BASIS": "150", "TEAM_NET_BASIS": "0", "ACTIVE_DIRECT_COUNT": "0"}, "A2": {"PERSONAL_NET_BASIS": "999", "TEAM_NET_BASIS": "0", "ACTIVE_DIRECT_COUNT": "0"}}}
	statisticsBody, _ := json.Marshal(statistics)
	if err = os.WriteFile(statisticsFile, statisticsBody, 0600); err != nil {
		t.Fatal(err)
	}
	gradeService := service.GradeService{Service: plans, Grades: agents.Repository{}, Subjects: agents.Repository{}, Evaluations: repo.Evaluations{}, Statistics: repo.SandboxStatisticsFile{Path: statisticsFile}}
	evaluation := service.EvaluationRequest{PlanID: "P1", Period: "2026-09", Cutoff: clock}
	result, err = gradeService.Evaluate(ctx, operator, evaluation, "evaluate", "request")
	if err != nil {
		t.Fatal(err)
	}
	evaluated := decode(result)
	if evaluated["source_watermark"] != "sandbox-grade-v1" {
		t.Fatal("missing statistics watermark")
	}
	state, _, err = (agents.Repository{}).GradeAt(ctx, store.DB, scope, "A1", "P1", clock)
	if err != nil || state.Grade != "G1" || state.Eligibility != "ELIGIBLE" {
		t.Fatalf("automatic grade %+v %v", state, err)
	}
	unjoined, _, err := (agents.Repository{}).GradeAt(ctx, store.DB, scope, "A2", "P1", clock)
	if err != nil || unjoined.Grade != "G1" || unjoined.Eligibility != "INELIGIBLE" {
		t.Fatalf("automatic grade improperly granted eligibility %+v %v", unjoined, err)
	}
	beforeEvaluation, _, err := (agents.Repository{}).GradeAt(ctx, store.DB, scope, "A1", "P1", clock.Add(-time.Microsecond))
	if err != nil || beforeEvaluation.Grade != "G0" {
		t.Fatal("automatic grade rewrote history", err)
	}
	if err = os.Remove(statisticsFile); err != nil {
		t.Fatal(err)
	}
	replay, err := gradeService.Evaluate(ctx, operator, evaluation, "evaluate", "new-request")
	if err != nil || string(replay.Body) != string(result.Body) {
		t.Fatal("evaluation did not replay frozen result", err)
	}
	var preserved []byte
	if err = store.DB.QueryRow(`SELECT config FROM plan_versions WHERE tenant_id=? AND version_id=?`, scope.Tenant, first.ID).Scan(&preserved); err != nil {
		t.Fatal(err)
	}
	var historical plan.Config
	if err = json.Unmarshal(preserved, &historical); err != nil || historical.Revision != 1 {
		t.Fatal("historical config mutated", err)
	}
}
