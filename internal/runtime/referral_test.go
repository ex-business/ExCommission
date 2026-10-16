package runtime

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	agents "github.com/ex-business/ExCommission/internal/agents/adapters"
	agent "github.com/ex-business/ExCommission/internal/agents/domain"
	identity "github.com/ex-business/ExCommission/internal/identity/domain"
	ledger "github.com/ex-business/ExCommission/internal/ledger/adapters"
	money "github.com/ex-business/ExCommission/internal/ledger/domain"
	plans "github.com/ex-business/ExCommission/internal/plans/adapters"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	q "github.com/ex-business/ExCommission/internal/qualification/domain"
	referralrepo "github.com/ex-business/ExCommission/internal/referral/adapters"
	referral "github.com/ex-business/ExCommission/internal/referral/application"
	model "github.com/ex-business/ExCommission/internal/referral/domain"
	registration "github.com/ex-business/ExCommission/internal/registration/domain"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

type failingRewardLedger struct{ ledger.Repository }

func (f failingRewardLedger) CreditReward(ctx context.Context, tx db.Executor, s foundation.Scope, c money.RewardCredit, at time.Time) (string, error) {
	id, err := f.Repository.CreditReward(ctx, tx, s, c, at)
	if err != nil {
		return id, err
	}
	return id, errors.New("模拟账簿写入后中断")
}
func TestReferralRewardsEndToEnd(t *testing.T) {
	if os.Getenv("TEST_MYSQL_ADDRESS") == "" {
		t.Skip("需要本地MySQL")
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
	sc := foundation.Scope{Tenant: "test-referral-" + newID(), App: "commission"}
	defer func() {
		rows, e := store.DB.Query(`SELECT TABLE_NAME FROM information_schema.columns WHERE TABLE_SCHEMA=DATABASE() AND COLUMN_NAME='tenant_id'`)
		if e != nil {
			t.Error(e)
			return
		}
		tables := []string{}
		for rows.Next() {
			var table string
			if e = rows.Scan(&table); e != nil {
				t.Error(e)
				return
			}
			if !regexp.MustCompile(`^[a-z_]+$`).MatchString(table) {
				t.Fatal(table)
			}
			tables = append(tables, table)
		}
		rows.Close()
		for _, table := range tables {
			if _, e = store.DB.Exec("DELETE FROM `"+table+"` WHERE tenant_id=?", sc.Tenant); e != nil {
				t.Error(e)
			}
		}
	}()
	clock := time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC)
	old := timeSource
	timeSource = func() time.Time { return clock }
	defer func() { timeSource = old }()
	token, reviewer, key, rkey := strings.Repeat("o", 32), strings.Repeat("a", 32), strings.Repeat("q", 32), strings.Repeat("r", 32)
	for k, v := range map[string]string{"APP_MODE": "local", "LOCAL_TENANT": sc.Tenant, "LOCAL_APP": sc.App, "LOCAL_OPERATOR_TOKEN": token, "LOCAL_APPROVER_TOKEN": reviewer, "LOCAL_CURSOR_KEY": strings.Repeat("c", 32), "LOCAL_SOURCE_KEY": strings.Repeat("s", 32), "LOCAL_PAYMENT_KEY": strings.Repeat("p", 32), "LOCAL_REGISTRATION_ENABLED": "true", "LOCAL_REGISTRATION_KEY": rkey, "LOCAL_REGISTRATION_URL": "", "LOCAL_QUALIFICATION_ENABLED": "true", "LOCAL_QUALIFICATION_KEY": key, "LOCAL_REFERRAL_REWARDS_ENABLED": "true", "LOCAL_DAYEND_ENABLED": "false", "AUTH_LOCAL_ACCOUNTS_FILE": "", "AUTH_EXTERNAL_FILE": ""} {
		t.Setenv(k, v)
	}
	handler, err := businessHandler(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path, credential, idem string, input any, prefix, secret string) (int, map[string]any) {
		raw, _ := json.Marshal(input)
		req := httptest.NewRequest(method, path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", idem)
		if credential != "" {
			req.Header.Set("Authorization", "Bearer "+credential)
		}
		if prefix != "" {
			mac := hmac.New(sha256.New, []byte(secret))
			mac.Write([]byte(prefix + "\n" + sc.Tenant + "\n" + sc.App + "\nsandbox\n"))
			mac.Write(raw)
			h := "X-Qualification-Signature"
			if prefix == "registration-v1" {
				h = "X-Registration-Signature"
			}
			req.Header.Set(h, hex.EncodeToString(mac.Sum(nil)))
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		var out map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Errorf("响应非JSON %d %s", rec.Code, rec.Body.String())
		}
		return rec.Code, out
	}
	must := func(code int, out map[string]any) map[string]any {
		t.Helper()
		if code >= 400 {
			t.Fatalf("HTTP %d %+v", code, out)
		}
		return out["data"].(map[string]any)
	}
	raw, err := os.ReadFile("../../conformance/v1/plan-percent.json")
	if err != nil {
		t.Fatal(err)
	}
	validator, err := plans.NewValidator(foundation.CurrencyExponents(), map[string]bool{"UTC-v1": true})
	if err != nil {
		t.Fatal(err)
	}
	config, canonical, err := validator.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	// 夹具使用业务仓储发布真实方案版本；推荐奖励不伪造方案版本，以保持报表和结算链路。
	err = store.Within(ctx, func(tx db.Executor) error {
		r := plans.Repository{}
		hash := plans.Hash(canonical)
		before := clock.Add(-24 * time.Hour)
		if e := r.Create(ctx, tx, sc, config, canonical, hash, before); e != nil {
			return e
		}
		if e := r.SetPending(ctx, tx, sc, config.PlanID, 0, hash); e != nil {
			return e
		}
		return r.Publish(ctx, tx, sc, config.PlanID, 0, hash, "referral-pv", before, before)
	})
	if err != nil {
		t.Fatal(err)
	}
	register := func(external string, invite *string) map[string]any {
		t.Helper()
		return must(call("POST", "/v1/registrations", token, newID(), registration.Event{SchemaVersion: "1", SourceID: "sandbox", EventID: newID(), ExternalUserID: external, DisplayName: external, RegisteredAt: clock, InvitationCode: invite}, "registration-v1", rkey))
	}
	invitation := func(id string) string {
		t.Helper()
		data := must(call("POST", "/v1/registration-invitations", token, newID(), registration.InvitationRequest{SourceID: "sandbox", AgentID: id}, "", ""))
		return data["invitation_code"].(string)
	}
	A := register("A", nil)["agent_id"].(string)
	inviteA := invitation(A)
	B := register("B", &inviteA)["agent_id"].(string)
	register("C", &inviteA)
	inviteB := invitation(B)
	register("D", &inviteB)
	clock = clock.Add(time.Second)
	err = store.Within(ctx, func(tx db.Executor) error {
		return (agents.Repository{}).ApplyGrade(ctx, tx, sc, agent.GradeChange{AgentID: A, PlanID: "P1", GradeID: "G0", Eligibility: "ELIGIBLE"}, "MANUAL", "verified-fixture", clock)
	})
	if err != nil {
		t.Fatal(err)
	}
	policy := q.Policy{ID: "effective", Source: "sandbox", Currency: "USD", Deposit: "50", Bet: "100", Venues: []string{"SPORTS"}, Statistics: &q.StatisticsPolicy{Range: q.NaturalDay, Timezone: "UTC"}}
	must(call("POST", "/v1/member-qualification-policies", token, newID(), policy, "", ""))
	def := model.Definition{ID: "referral-r1", Name: "直属阶梯奖励", PlanID: "P1", Source: "sandbox", PolicyID: "effective", Currency: "USD", Tiers: []model.Tier{{ID: "one", Required: 1, Amount: "500"}, {ID: "two", Required: 2, Amount: "800"}, {ID: "three", Required: 3, Amount: "1000"}}}
	rule := must(call("POST", "/v1/referral-reward-rules", token, newID(), def, "", ""))
	hash := rule["config_hash"].(string)
	changed := def
	changed.Name = "不允许覆盖"
	code, _ := call("POST", "/v1/referral-reward-rules", token, newID(), changed, "", "")
	if code != 409 {
		t.Fatal("规则被覆盖", code)
	}
	activation := referral.Activation{Hash: hash, Decision: "APPROVE", Reason: "复核阶梯金额和人数"}
	code, _ = call("POST", "/v1/referral-reward-rules/"+def.ID+"/activation", token, newID(), activation, "", "")
	if code != 403 {
		t.Fatal("未授权审批通过", code)
	}
	self, err := referralService(store).Activate(ctx, identity.Principal{Tenant: sc.Tenant, App: sc.App, Subject: "local-operator"}, def.ID, activation, newID(), "self")
	if err != nil || self.Status != 403 {
		t.Fatal("申请人自审通过", self.Status, err)
	}
	claim := referral.Claim{RuleID: def.ID, AgentID: A, TierID: "one"}
	code, _ = call("POST", "/v1/referral-reward-claims", token, newID(), claim, "", "")
	if code != 409 {
		t.Fatal("未启用规则领取", code)
	}
	clock = clock.Add(time.Second)
	must(call("POST", "/v1/referral-reward-rules/"+def.ID+"/activation", reviewer, newID(), activation, "", ""))
	code, _ = call("POST", "/v1/referral-reward-claims", token, newID(), claim, "", "")
	if code != 422 {
		t.Fatal("未达标领取", code)
	}
	qualify := func(external string) {
		t.Helper()
		venue := "SPORTS"
		for _, f := range []q.Fact{{Schema: "1", PolicyID: "effective", Source: "sandbox", EventID: newID(), BusinessID: newID(), Type: "DEPOSIT_SUCCEEDED", ExternalUser: external, At: clock, Amount: "50", Currency: "USD"}, {Schema: "1", PolicyID: "effective", Source: "sandbox", EventID: newID(), BusinessID: newID(), Type: "BET_SETTLED", ExternalUser: external, At: clock, Amount: "100", Currency: "USD", Venue: &venue}} {
			must(call("POST", "/v1/member-qualification-facts", token, newID(), f, "qualification-v1", key))
		}
	}
	flush := func() {
		t.Helper()
		if err := deliverOutbox(ctx, store, sc); err != nil {
			t.Fatal(err)
		}
		if err := referralService(store).Drain(ctx, sc); err != nil {
			t.Fatal(err)
		}
	}
	view := func(agent string) model.Progress {
		t.Helper()
		data := must(call("GET", "/v1/referral-reward-rules/"+def.ID+"/progress?agent_id="+url.QueryEscape(agent), token, "", nil, "", ""))
		raw, _ := json.Marshal(data)
		var p model.Progress
		if err = json.Unmarshal(raw, &p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	clock = clock.Add(time.Second)
	qualify("D")
	flush()
	if p := view(A); p.DirectCount != 0 || p.Tiers[0].Status != "LOCKED" {
		t.Fatal("二级会员被计为直属", p)
	}
	clock = clock.Add(time.Second)
	qualify("B")
	flush()
	p := view(A)
	if p.DirectCount != 1 || p.Tiers[0].Status != "CLAIMABLE" || p.Tiers[1].Status != "LOCKED" {
		t.Fatal("解锁错误", p)
	}
	bad := claim
	bad.AgentID = B
	code, _ = call("POST", "/v1/referral-reward-claims", token, newID(), bad, "", "")
	if code != 422 {
		t.Fatal("无审批资格代理领取", code)
	}
	var wg sync.WaitGroup
	fails := make(chan string, 8)
	receipts := make(chan string, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			status, out := call("POST", "/v1/referral-reward-claims", token, newID(), claim, "", "")
			if status != 200 {
				fails <- fmt.Sprintf("%d %+v", status, out)
				return
			}
			receipts <- out["data"].(map[string]any)["entitlement_id"].(string)
		}()
	}
	wg.Wait()
	close(fails)
	close(receipts)
	for e := range fails {
		t.Error(e)
	}
	entitlement := ""
	for id := range receipts {
		if entitlement == "" {
			entitlement = id
		} else if id != entitlement {
			t.Fatal("并发产生多个权益", id, entitlement)
		}
	}
	// 暂不投递C的达标事件，跨自然日后重放，验证历史解锁不会因延迟消费丢失。
	clock = time.Date(2026, 10, 14, 23, 59, 59, 0, time.UTC)
	qualify("C")
	clock = time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC)
	must(call("POST", "/v1/referral-reward-rules/"+def.ID+"/progress-refresh", token, newID(), map[string]string{"agent_id": A}, "", ""))
	if p = view(A); p.DirectCount != 0 || p.Tiers[1].Status != "LOCKED" {
		t.Fatal("周期未重置人数", p)
	}
	flush()
	p = view(A)
	if p.DirectCount != 0 || p.Tiers[0].Status != "CLAIMED" || p.Tiers[1].Status != "CLAIMABLE" {
		t.Fatal("迟到事件丢解锁或回退当前进度", p)
	}
	if err = referralService(store).RefreshAll(ctx, sc); err != nil {
		t.Fatal(err)
	}
	claim.TierID = "two"
	// 冻结串行保护；恢复后依然保留已解锁阶梯。
	setStatus := func(status string) {
		t.Helper()
		clock = clock.Add(time.Second)
		err = store.Within(ctx, func(tx db.Executor) error {
			r := agents.Repository{}
			rev, e := r.Revision(ctx, tx, sc, A)
			if e != nil {
				return e
			}
			return r.SetStatus(ctx, tx, sc, A, status, rev, "verified-fixture", clock)
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	setStatus("FROZEN")
	code, _ = call("POST", "/v1/referral-reward-claims", token, newID(), claim, "", "")
	if code < 400 {
		t.Fatal("冻结代理领取", code)
	}
	setStatus("ACTIVE")
	broken := referralService(store)
	broken.Ledger = failingRewardLedger{}
	_, err = broken.Claim(ctx, identity.Principal{Tenant: sc.Tenant, App: sc.App, Subject: "local-operator"}, claim, newID(), "simulate-crash")
	if err == nil {
		t.Fatal("故障未发生")
	}
	var count int
	var total string
	if err = store.DB.QueryRow(`SELECT COUNT(*),CAST(SUM(target) AS CHAR) FROM ledger_entitlements WHERE tenant_id=? AND app_id=?`, sc.Tenant, sc.App).Scan(&count, &total); err != nil || count != 1 || total != "500" {
		t.Fatal("故障产生部分入账", count, total, err)
	}
	must(call("POST", "/v1/referral-reward-claims", token, newID(), claim, "", ""))
	claim.TierID = "one"
	must(call("POST", "/v1/referral-reward-claims", token, newID(), claim, "", ""))
	claim.TierID = "three"
	code, _ = call("POST", "/v1/referral-reward-claims", token, newID(), claim, "", "")
	if code != 422 {
		t.Fatal("未达第三阶领取", code)
	}
	if err = store.DB.QueryRow(`SELECT COUNT(*),CAST(SUM(target) AS CHAR) FROM ledger_entitlements WHERE tenant_id=? AND app_id=?`, sc.Tenant, sc.App).Scan(&count, &total); err != nil || count != 2 || total != "1300" {
		t.Fatal("跨周期重复奖励", count, total, err)
	}
	for _, kind := range []string{"earnings", "finance", "cost"} {
		must(call("GET", "/v1/reports/"+kind+"?plan_id=P1&currency=USD", token, "", nil, "", ""))
	}
	// 原日结能够纳入奖励权益，领取仍须等原结算审批后才成为可用余额。
	run := must(call("POST", "/v1/statement-runs", token, newID(), map[string]any{"period": "2026-10-14", "calendar_version": "UTC-v1"}, "", ""))
	statements := run["statements"].([]any)
	if len(statements) != 1 {
		t.Fatal(run)
	}
	statement := statements[0].(map[string]any)
	submission := must(call("POST", "/v1/statements/"+statement["statement_id"].(string)+"/submission", token, newID(), map[string]any{"expected_revision": statement["revision"], "content_hash": statement["content_hash"]}, "", ""))
	approvalID := submission["approval_id"].(string)
	approval := must(call("GET", "/v1/approvals/"+approvalID, reviewer, "", nil, "", ""))
	decided := must(call("POST", "/v1/approvals/"+approvalID+"/decisions", reviewer, newID(), map[string]any{"decision": "APPROVE", "expected_revision": approval["revision"], "object_hash": approval["object_hash"], "reason": "核对推荐奖励日结"}, "", ""))
	if decided["execution_status"] != "SUCCEEDED" {
		t.Fatal(decided)
	}
	ent, err := (ledger.Repository{}).Get(ctx, store.DB, sc, entitlement, false)
	if err != nil || ent.Available != "500" || ent.Pending != "0" {
		t.Fatal("奖励无法进入原结算", ent, err)
	}

	receipt := must(call("GET", "/v1/referral-reward-claims/"+model.ClaimID(def.ID, A, "one"), token, "", nil, "", ""))
	if receipt["plan_version_id"] != "referral-pv" || receipt["currency"] != "USD" || receipt["reward"].(map[string]any)["entitlement_id"] != entitlement {
		t.Fatal("领取凭证未保留资金依据", receipt)
	}
	if err = store.DB.QueryRow(`SELECT COUNT(*) FROM referral_reward_claims WHERE tenant_id=? AND app_id=?`, sc.Tenant, sc.App).Scan(&count); err != nil || count != 2 {
		t.Fatal("领取业务唯一记录错误", count, err)
	}
	wrongScope := identity.Principal{Tenant: sc.Tenant + "-other", App: sc.App, Subject: "local-operator"}
	if _, err = referralService(store).GetClaim(ctx, wrongScope, model.ClaimID(def.ID, A, "one")); err == nil {
		t.Fatal("跨租户读取凭证")
	}
	// 独立队列中的坏事件退避重试，不阻塞后面的正常奖励事件。
	inbox := referralrepo.Repository{}
	if err = inbox.Enqueue(ctx, store.DB, sc, model.Event{ID: "invalid-event", Body: []byte(`{"qualification":"invalid"}`), At: clock}); err != nil {
		t.Fatal(err)
	}
	if err = inbox.Enqueue(ctx, store.DB, sc, model.Event{ID: "valid-event", Body: []byte(`{"qualification":{"is_effective":false}}`), At: clock}); err != nil {
		t.Fatal(err)
	}
	if err = referralService(store).Drain(ctx, sc); err != nil {
		t.Fatal(err)
	}
	var status string
	var attempts int
	if err = store.DB.QueryRow(`SELECT status,attempts FROM referral_reward_inbox WHERE tenant_id=? AND app_id=? AND event_id='invalid-event'`, sc.Tenant, sc.App).Scan(&status, &attempts); err != nil || status != "PENDING" || attempts != 1 {
		t.Fatal(status, attempts, err)
	}
	if err = store.DB.QueryRow(`SELECT status FROM referral_reward_inbox WHERE tenant_id=? AND app_id=? AND event_id='valid-event'`, sc.Tenant, sc.App).Scan(&status); err != nil || status != "DONE" {
		t.Fatal(status, err)
	}
	clock = clock.Add(time.Second)
	register("created-after-activation", nil)
	if err = referralService(store).RefreshAll(ctx, sc); err != nil {
		t.Fatal("后创建代理阻塞进度刷新", err)
	}
	code, _ = call("POST", "/v1/referral-reward-claims", "", newID(), claim, "", "")
	if code != 401 {
		t.Fatal("匿名领取", code)
	}
	t.Log("直属解锁、双人启用、并发防重、跨周期、迟到消费、冻结及资格保护、原子回滚、报表与结算通过")
}
