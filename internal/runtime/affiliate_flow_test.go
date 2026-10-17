package runtime

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	flowrepo "github.com/ex-business/ExCommission/internal/affiliateflow/adapters"
	flow "github.com/ex-business/ExCommission/internal/affiliateflow/application"
	flowmodel "github.com/ex-business/ExCommission/internal/affiliateflow/domain"
	principal "github.com/ex-business/ExCommission/internal/identity/domain"
	ledger "github.com/ex-business/ExCommission/internal/ledger/adapters"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	security "github.com/ex-business/ExCommission/internal/security/domain"
	wallet "github.com/ex-business/ExCommission/internal/wallet/adapters"
	walletmodel "github.com/ex-business/ExCommission/internal/wallet/domain"
)

type failAffiliateWallet struct{ calls atomic.Int64 }

func (w *failAffiliateWallet) Credit(ctx context.Context, tx db.Executor, s foundation.Scope, c walletmodel.Credit, at time.Time) error {
	if w.calls.Add(1) == 2 {
		return errors.New("测试注入第二笔钱包写入失败")
	}
	return (wallet.Repository{}).Credit(ctx, tx, s, c, at)
}
func TestAffiliateFlowQualificationUnlockAtomicClaimsAndGate(t *testing.T) {
	if os.Getenv("TEST_MYSQL_ADDRESS") == "" {
		t.Skip("需要本地 MySQL")
	}
	ctx := context.Background()
	store, e := mysql.Open(ctx, os.Getenv("TEST_MYSQL_ADDRESS"), "excommission", os.Getenv("MYSQL_PASSWORD"), "excommission")
	if e != nil {
		t.Fatal(e)
	}
	defer store.DB.Close()
	if e = store.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	tenant := "test-affiliate-flow-" + newID()
	sc := foundation.Scope{Tenant: tenant, App: "commission"}
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
				break
			}
			if !regexp.MustCompile(`^[a-z_]+$`).MatchString(table) {
				t.Error("非法清理表名")
				break
			}
			tables = append(tables, table)
		}
		rows.Close()
		for _, table := range tables {
			if _, e = store.DB.Exec("DELETE FROM `"+table+"` WHERE tenant_id=?", tenant); e != nil {
				t.Error(e)
			}
		}
	}()
	clock := atomic.Int64{}
	clock.Store(time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC).UnixNano())
	previous := timeSource
	timeSource = func() time.Time { return time.Unix(0, clock.Load()).UTC() }
	defer func() { timeSource = previous }()
	base := clockUTC()
	key := strings.Repeat("f", 32)
	regKey := strings.Repeat("r", 32)
	contextKey := strings.Repeat("i", 32)
	op := strings.Repeat("o", 32)
	checker := strings.Repeat("a", 32)
	for k, v := range map[string]string{"APP_MODE": "local", "LOCAL_TENANT": tenant, "LOCAL_APP": sc.App, "LOCAL_OPERATOR_TOKEN": op, "LOCAL_APPROVER_TOKEN": checker, "LOCAL_CURSOR_KEY": strings.Repeat("c", 32), "LOCAL_SOURCE_KEY": strings.Repeat("s", 32), "LOCAL_PAYMENT_KEY": strings.Repeat("p", 32), "LOCAL_AFFILIATE_FLOW_ENABLED": "true", "LOCAL_AFFILIATE_FLOW_KEY": key, "AFFILIATE_CLIENT_CONTEXT_KEY": contextKey, "LOCAL_REGISTRATION_ENABLED": "true", "LOCAL_REGISTRATION_KEY": regKey, "LOCAL_REGISTRATION_URL": "http://127.0.0.1:18286/", "LOCAL_USERS_FILE": "", "AUTH_LOCAL_ACCOUNTS_FILE": "", "AUTH_EXTERNAL_FILE": "", "AUTH_MEMBER_SOURCE_FILE": "", "AUTH_TOTP_FILE": ""} {
		t.Setenv(k, v)
	}
	secret := []byte("affiliate-totp-test-secret")
	totpJSON, _ := json.Marshal([]map[string]string{{"tenant_id": tenant, "app_id": sc.App, "subject_id": "local-operator", "secret": base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(secret)}})
	t.Setenv("AUTH_TOTP_SECRETS", string(totpJSON))
	handler, e := businessHandler(ctx, store)
	if e != nil {
		t.Fatal(e)
	}
	call := func(path, token string, in any, want int, id string, extra map[string]string) map[string]any {
		t.Helper()
		raw, _ := json.Marshal(in)
		r := httptest.NewRequest("POST", path, bytes.NewReader(raw))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		if id == "" {
			id = newID()
		}
		r.Header.Set("Idempotency-Key", id)
		for k, v := range extra {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s got%d want%d %s", path, w.Code, want, w.Body)
		}
		var out map[string]any
		if e = json.Unmarshal(w.Body.Bytes(), &out); e != nil {
			t.Fatal(e)
		}
		if data, ok := out["data"].(map[string]any); ok {
			return data
		}
		return out
	}
	cfg := affiliateTestConfig()
	cfg["currency"] = "USD"
	cfg["grade_period"] = "DAY"
	cfg["grade_condition"] = map[string]string{"deposit_minor": "100", "valid_bet_minor": "100"}
	cfg["referral_condition"] = map[string]string{"deposit_minor": "100", "valid_bet_minor": "200"}
	cfg["invitee_condition"] = map[string]string{"deposit_minor": "100", "valid_bet_minor": "300"}
	cfg["invitee_reward_minor"] = "50"
	cfg["steps"] = []map[string]any{{"step": 1, "additional_members": 1, "reward_minor": "100"}, {"step": 2, "additional_members": 3, "reward_minor": "200"}}
	pending := call("/v1/affiliate/config-requests", op, map[string]any{"config": cfg, "expected_revision": 0, "reason": "验证业务奖励流程"}, 202, "", nil)
	call("/v1/approvals/"+pending["approval_id"].(string)+"/decisions", checker, map[string]any{"decision": "APPROVE", "expected_revision": 0, "object_hash": pending["object_hash"], "reason": "独立复核"}, 200, "", nil)
	registration := func(external string, invite any) map[string]any {
		in := map[string]any{"schema_version": "1", "source_id": "sandbox", "event_id": "reg-" + external, "external_user_id": external, "display_name": external, "registered_at": base.Format(time.RFC3339Nano), "invitation_code": invite}
		raw, _ := json.Marshal(in)
		mac := hmac.New(sha256.New, []byte(regKey))
		mac.Write([]byte("registration-v1\n" + tenant + "\n" + sc.App + "\nsandbox\n"))
		mac.Write(raw)
		return call("/v1/registrations", op, in, 200, "", map[string]string{"X-Registration-Signature": hex.EncodeToString(mac.Sum(nil))})
	}
	root := registration("root", nil)
	invite := call("/v1/registration-invitations", op, map[string]string{"source_id": "sandbox", "agent_id": root["agent_id"].(string)}, 200, "", nil)["invitation_code"]
	members := []map[string]any{}
	for _, id := range []string{"child1", "child2", "child3", "child4"} {
		members = append(members, registration(id, invite))
	}
	clock.Store(base.Add(10 * time.Minute).UnixNano())
	event := func(external, business, kind, amount, bet string) map[string]any {
		return map[string]any{"source_id": "sandbox", "event_id": "event-" + business, "business_id": business, "external_user_id": external, "kind": kind, "currency": "USD", "amount_minor": amount, "valid_bet_minor": bet, "venue_type_id": "", "business_time": base.Add(time.Minute).Format(time.RFC3339Nano)}
	}
	send := func(in map[string]any, want int) map[string]any {
		raw, _ := json.Marshal(in)
		mac := hmac.New(sha256.New, []byte(key))
		mac.Write([]byte("affiliate-business-v1\n" + tenant + "\n" + sc.App + "\nsandbox\n"))
		mac.Write(raw)
		return call("/v1/affiliate/business-facts", op, in, want, "", map[string]string{"X-Affiliate-Signature": hex.EncodeToString(mac.Sum(nil))})
	}
	call("/v1/affiliate/business-facts", op, event("child1", "forged", "DEPOSIT_SUCCEEDED", "100", "0"), 403, "", nil)
	for _, id := range []string{"child1", "child2", "child3", "child4"} {
		send(event(id, id+"-deposit", "DEPOSIT_SUCCEEDED", "100", "0"), 200)
		bet := event(id, id+"-bet", "BET_SETTLED", "200", "200")
		bet["venue_type_id"] = "SPORTS"
		send(bet, 200)
	}
	service := affiliateFlowService(store)
	rootUser := root["user_id"].(string)
	childUser := members[0]["user_id"].(string)
	rewards, e := service.Repo.Rewards(ctx, store.DB, sc, rootUser)
	if e != nil || len(rewards) != 2 {
		t.Fatal("推荐阶梯未按1+3解锁", rewards, e)
	}
	amounts := map[int]string{}
	for _, r := range rewards {
		amounts[r.Step] = r.Amount
	}
	if amounts[1] != "100" || amounts[2] != "200" {
		t.Fatal(rewards)
	}
	childRewards, e := service.Repo.Rewards(ctx, store.DB, sc, childUser)
	if e != nil || len(childRewards) != 0 {
		t.Fatal("受邀条件与推荐条件串用", childRewards, e)
	}
	extra := event("child1", "invitee-extra", "BET_SETTLED", "100", "100")
	extra["venue_type_id"] = "SPORTS"
	send(extra, 200)
	extra["event_id"] = "different-delivery"
	if send(extra, 200)["accepted"] != false {
		t.Fatal("同订单换事件号重复累计")
	}
	extra["event_id"] = "different-content"
	extra["amount_minor"] = "101"
	send(extra, 409)
	childRewards, e = service.Repo.Rewards(ctx, store.DB, sc, childUser)
	if e != nil || len(childRewards) != 1 || childRewards[0].UserID != childUser || childRewards[0].Kind != "INVITEE" {
		t.Fatal(childRewards, e)
	}
	candidates, e := (ledger.Repository{}).PendingBefore(ctx, store.DB, sc, base.AddDate(0, 0, 2), clockUTC())
	if e != nil || len(candidates) != 0 {
		t.Fatal("旧结算扫描获得了新模块权益", candidates, e)
	}
	makePrincipal := func(user, subject string) principal.Principal {
		p := principal.Principal{Tenant: tenant, App: sc.App, Subject: subject, MemberUserID: user}
		if e = bootstrapFeature(ctx, store, tenant, sc.App, subject, "affiliate-test-member", []string{"member:read", "member:claim"}); e != nil {
			t.Fatal(e)
		}
		return p
	}
	rootP := makePrincipal(rootUser, "member-flow-root")
	childP := makePrincipal(childUser, "member-flow-child")
	failing := service
	failing.Funds = flowrepo.Funds{Wallet: &failAffiliateWallet{}}
	if _, e = failing.Claim(ctx, rootP, flow.Claim{Kind: "ALL"}, "203.0.113.10", "rollback-batch", "rollback-request"); e == nil {
		t.Fatal("注入失败未回滚")
	}
	accounts, e := (wallet.Repository{}).Accounts(ctx, store.DB, sc, rootUser)
	if e != nil || len(accounts) != 0 {
		t.Fatal("失败批次仍写入钱包", accounts, e)
	}
	for _, r := range rewards {
		ent, e := (ledger.Repository{}).Get(ctx, store.DB, sc, r.EntitlementID, false)
		if e != nil || ent.Pending != r.Amount || !ent.Frozen {
			t.Fatal("回滚未恢复账簿", ent, e)
		}
	}
	filter := flowmodel.RecordFilter{Kind: "REFERRAL", UserID: rootUser, From: base, To: base.AddDate(0, 0, 1), AsOf: clockUTC(), Limit: 1}
	first, e := (flowrepo.Repository{}).Records(ctx, store.DB, sc, filter, true)
	if e != nil || len(first.Rows) != 1 || first.Next == nil || first.Snapshot == 0 || len(first.Summary) != 1 || first.Summary[0].Claimable != "300" {
		t.Fatal("第一页及全条件汇总不正确", first, e)
	}
	successes := atomic.Int64{}
	var wg sync.WaitGroup
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := service.Claim(ctx, rootP, flow.Claim{Kind: "ALL"}, "203.0.113.10", newID(), newID())
			if err == nil && out.Status == 200 {
				successes.Add(1)
			} else if err != nil || out.Status != 409 {
				t.Error(out, err)
			}
		}()
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatal("并发重复领取", successes.Load())
	}
	accounts, e = (wallet.Repository{}).Accounts(ctx, store.DB, sc, rootUser)
	if e != nil || len(accounts) != 1 || accounts[0].Balance != "300" {
		t.Fatal(accounts, e)
	}
	filter.After = *first.Next
	filter.Snapshot = first.Snapshot
	second, e := (flowrepo.Repository{}).Records(ctx, store.DB, sc, filter, true)
	if e != nil || len(second.Rows) != 1 || second.Rows[0].ID == first.Rows[0].ID || second.Rows[0].Status != "CLAIMABLE" || second.Summary[0].Claimable != "300" {
		t.Fatal("领取改变了已冻结的分页快照", second, e)
	}
	filter.After = ""
	filter.Snapshot = 0
	filter.Limit = 100
	latest, e := (flowrepo.Repository{}).Records(ctx, store.DB, sc, filter, false)
	if e != nil || len(latest.Rows) != 2 || latest.Summary[0].Claimed != "300" || latest.Rows[0].ClaimIP != "" {
		t.Fatal("当前记录或IP权限不正确", latest, e)
	}
	var count int
	if e = store.DB.QueryRow(`SELECT COUNT(*) FROM wallet_changes WHERE tenant_id=? AND user_id=?`, tenant, rootUser).Scan(&count); e != nil || count != 2 {
		t.Fatal("合并领取未逐笔账变", count, e)
	}
	// 实际TOTP校验：缺码拒绝，暂停后既有受邀奖励不可领，重开才可领取旧权益。
	path := "/v1/affiliate/agents/" + childUser + "/reward-switch"
	change := map[string]any{"enabled": false, "expected_revision": 0, "reason": "暂停受邀奖励测试", "confirmed": true}
	call(path, op, change, 403, "toggle-missing", nil)
	code, _ := security.Code(secret, uint64(clockUTC().Unix()/30), 6)
	call(path, op, change, 200, "toggle-disable", map[string]string{"X-TOTP-Code": code})
	if blocked, err := service.Claim(ctx, childP, flow.Claim{Kind: "INVITEE"}, "203.0.113.11", "disabled-claim", "disabled-request"); err != nil || blocked.Status != 409 {
		t.Fatal("关闭后仍能领取")
	}
	clock.Store(clockUTC().Add(time.Minute).UnixNano())
	change["enabled"] = true
	change["expected_revision"] = 1
	code, _ = security.Code(secret, uint64(clockUTC().Unix()/30), 6)
	call(path, op, change, 200, "toggle-enable", map[string]string{"X-TOTP-Code": code})
	out, e := service.Claim(ctx, childP, flow.Claim{Kind: "INVITEE"}, "203.0.113.11", "invitee-claim", "invitee-request")
	if e != nil || out.Status != 200 {
		t.Fatal(out, e)
	}
	retry, e := service.Claim(ctx, childP, flow.Claim{Kind: "INVITEE"}, "203.0.113.11", "invitee-claim", "invitee-retry")
	if e != nil || !retry.Replayed {
		t.Fatal("领取重试未复用原批次", retry, e)
	}
	accounts, e = (wallet.Repository{}).Accounts(ctx, store.DB, sc, childUser)
	if e != nil || len(accounts) != 1 || accounts[0].Balance != "50" {
		t.Fatal(accounts, e)
	}
	// 按业务来源水位冻结最终等级，00点仅计算，次日01点才进入待审核。
	clock.Store(base.Add(11*time.Hour + 50*time.Minute).UnixNano())
	high := event("child2", "dayend-final-grade", "BET_SETTLED", "60000", "60000")
	high["venue_type_id"] = "SPORTS"
	high["business_time"] = base.Add(11*time.Hour + 45*time.Minute).Format(time.RFC3339Nano)
	send(high, 200)
	clock.Store(time.Date(2026, 10, 19, 0, 10, 0, 0, time.UTC).UnixNano())
	if e = service.RunOnce(ctx, sc); e != nil {
		t.Fatal(e)
	}
	if _, e = service.DailyRepo.Daily(ctx, store.DB, sc, "2026-10-18"); e == nil {
		t.Fatal("来源水位缺失仍冻结日终")
	}
	closeDay := func(day string, want int, wrongCount bool) {
		date, _ := time.Parse("2006-01-02", day)
		count, digest, e := service.DailyRepo.Manifest(ctx, store.DB, sc, "sandbox", date, date.AddDate(0, 0, 1))
		if e != nil {
			t.Fatal(e)
		}
		if wrongCount {
			count++
		}
		in := map[string]any{"source_id": "sandbox", "business_day": day, "fact_count": count, "facts_digest": digest}
		raw, _ := json.Marshal(in)
		mac := hmac.New(sha256.New, []byte(key))
		mac.Write([]byte("affiliate-watermark-v1\n" + tenant + "\n" + sc.App + "\nsandbox\n"))
		mac.Write(raw)
		call("/v1/affiliate/source-watermarks", op, in, want, "", map[string]string{"X-Affiliate-Signature": hex.EncodeToString(mac.Sum(nil))})
	}
	closeDay("2026-10-18", 409, true)
	closeDay("2026-10-18", 200, false)
	if e = service.RunOnce(ctx, sc); e != nil {
		t.Fatal(e)
	}
	daily, e := service.DailyRepo.Daily(ctx, store.DB, sc, "2026-10-18")
	if e != nil || daily.Status != "CALCULATED" {
		t.Fatal("01点前状态不正确", daily, e)
	}
	found := false
	for _, row := range daily.Results {
		if row.Result.UserID == rootUser {
			found = true
			if row.Result.Level != 1 || row.Result.Amount != "243" {
				t.Fatal("未按当天最终等级重算全部业务量", row)
			}
		}
	}
	if !found {
		t.Fatal("日终缺少根代理")
	}
	clock.Store(time.Date(2026, 10, 19, 1, 0, 0, 0, time.UTC).UnixNano())
	if e = service.RunOnce(ctx, sc); e != nil {
		t.Fatal(e)
	}
	if e = service.RunOnce(ctx, sc); e != nil {
		t.Fatal(e)
	}
	all, e := service.Repo.Rewards(ctx, store.DB, sc, rootUser)
	if e != nil {
		t.Fatal(e)
	}
	var commissionID string
	for _, r := range all {
		if r.Kind == "COMMISSION" {
			commissionID = r.ID
			if r.Status != "PENDING_REVIEW" || r.Amount != "243" {
				t.Fatal(r)
			}
		}
	}
	if commissionID == "" || len(all) != 3 {
		t.Fatal("重复结算或漏单", all)
	}
	late := event("child2", "late-old-day", "BET_SETTLED", "99999", "99999")
	late["venue_type_id"] = "SPORTS"
	late["business_time"] = base.Add(11*time.Hour + 55*time.Minute).Format(time.RFC3339Nano)
	lateResult := send(late, 200)
	if lateResult["late_adjustment"] != true || lateResult["accepted"] != false {
		t.Fatal("迟到单未保留为调整", lateResult)
	}
	original, e := service.Repo.Reward(ctx, store.DB, sc, commissionID)
	if e != nil || original.Original != "243" {
		t.Fatal("迟到单静默修改已关账佣金", original, e)
	}
	reviewPath := "/v1/affiliate/commissions/" + commissionID + "/review"
	call(reviewPath, op, map[string]any{"decision": "APPROVE", "expected_revision": 1, "reason": "禁止申请身份审核"}, 403, "", nil)
	call(reviewPath, checker, map[string]any{"decision": "APPROVE", "expected_revision": 1, "amount_minor": "99", "reason": "调整不得小于主货币1元"}, 400, "", nil)
	reviewed := call(reviewPath, checker, map[string]any{"decision": "APPROVE", "expected_revision": 1, "amount_minor": "500", "reason": "独立调整并通过"}, 200, "", nil)
	if reviewed["status"] != "CLAIMABLE" || reviewed["calculated_minor"] != "243" || reviewed["reviewer_id"] != "local-approver" {
		t.Fatal("审核通过未直接待领取或丢失原计算额", reviewed)
	}
	call(reviewPath, checker, map[string]any{"decision": "CANCEL", "expected_revision": 2, "reason": "取消尚未领取佣金"}, 200, "", nil)
	cancelled, e := service.Repo.Reward(ctx, store.DB, sc, commissionID)
	if e != nil || cancelled.Status != "CANCELLED" {
		t.Fatal(cancelled, e)
	}
	ent, e := (ledger.Repository{}).Get(ctx, store.DB, sc, cancelled.EntitlementID, false)
	if e != nil || ent.Target != "0" || ent.Pending != "0" {
		t.Fatal("取消未冲回账簿", ent, e)
	}
	blocked, e := service.Claim(ctx, rootP, flow.Claim{Kind: "COMMISSION"}, "203.0.113.10", "cancelled-commission-claim", "cancelled-request")
	if e != nil || blocked.Status != 409 {
		t.Fatal("取消后仍可领取", blocked, e)
	}
	// 次日仍只计算当天业务量，自然日有效人数归零；旧阶梯和受邀奖励不能跨日再发。
	clock.Store(time.Date(2026, 10, 19, 12, 5, 0, 0, time.UTC).UnixNano())
	next := event("child1", "next-day-volume", "BET_SETTLED", "90000", "90000")
	next["venue_type_id"] = "SPORTS"
	next["business_time"] = "2026-10-19T12:00:00Z"
	send(next, 200)
	clock.Store(time.Date(2026, 10, 20, 1, 0, 0, 0, time.UTC).UnixNano())
	closeDay("2026-10-19", 200, false)
	if e = service.RunOnce(ctx, sc); e != nil {
		t.Fatal(e)
	}
	all, e = service.Repo.Rewards(ctx, store.DB, sc, rootUser)
	if e != nil {
		t.Fatal(e)
	}
	commissionID = ""
	for _, r := range all {
		if r.Kind == "COMMISSION" && r.Day == "2026-10-19" {
			commissionID = r.ID
			if r.Amount != "225" {
				t.Fatal("次日等级口径错误", r)
			}
		}
	}
	if commissionID == "" {
		t.Fatal("次日佣金缺失")
	}
	call("/v1/affiliate/commissions/"+commissionID+"/review", checker, map[string]any{"decision": "APPROVE", "expected_revision": 1, "reason": "通过次日真实计算佣金"}, 200, "", nil)
	paid, e := service.Claim(ctx, rootP, flow.Claim{Kind: "ALL"}, "203.0.113.10", "next-day-claim", "next-day-request")
	if e != nil || paid.Status != 200 {
		t.Fatal(paid, e)
	}
	accounts, e = (wallet.Repository{}).Accounts(ctx, store.DB, sc, rootUser)
	if e != nil || len(accounts) != 1 || accounts[0].Balance != "525" {
		t.Fatal("佣金与推荐合并钱包金额不符", accounts, e)
	}

	// 代理总表使用同一只读事务与不可变查询快照，统计/贡献/钱包口径可分别核验。
	reader := flowrepo.AgentReports{DB: store.DB}
	reportFilter := flowmodel.AgentReportFilter{From: time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC), To: time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC), Sort: "team_valid_bet_minor", Descending: true}
	report, e := reader.Build(ctx, sc, reportFilter, clockUTC())
	if e != nil || len(report.Rows) != 1 {
		t.Fatal("代理总表未按有直属会员筛选", report, e)
	}
	row := report.Rows[0]
	if row.UserID != rootUser || row.Direct != 4 || row.Team != 4 || row.TeamDeposit != "400" || row.TeamValidBet != "150900" || row.ClaimedReferral != "300" || row.ClaimedCommission != "225" || row.CalculatedCommission != "468" {
		t.Fatal("总表统计口径不一致", row)
	}
	sum := flowmodel.SummarizeAgents(report.Rows)
	if sum.ClaimedReferral != "300" || sum.ClaimedCommission != "225" {
		t.Fatal("总表汇总与钱包不一致", sum)
	}
	snapshotID := flowmodel.ID("report-snapshot", tenant)
	if e = reader.Save(ctx, store.DB, sc, snapshotID, "local-operator", report, clockUTC()); e != nil {
		t.Fatal(e)
	}
	if _, e = reader.Get(ctx, store.DB, sc, snapshotID, "local-approver", clockUTC()); e == nil {
		t.Fatal("读取了其他操作者的查询快照")
	}
	live := event("child3", "today-after-report", "BET_SETTLED", "100", "100")
	live["venue_type_id"] = "SPORTS"
	live["business_time"] = "2026-10-20T00:30:00Z"
	send(live, 200)
	cached, e := reader.Get(ctx, store.DB, sc, snapshotID, "local-operator", clockUTC())
	if e != nil || cached.Rows[0].TeamValidBet != "150900" {
		t.Fatal("后续事实改写了分页/导出快照", cached, e)
	}
	if _, e = reader.Get(ctx, store.DB, sc, snapshotID, "local-operator", clockUTC().Add(11*time.Minute)); e == nil {
		t.Fatal("过期查询快照仍可用")
	}

	// 报表预估必须复用日结的跨日竞争事实和历史归属，不能把昨日赢家在分页查询时漏掉。
	oldWinner := event("child1", "risk-report-old-winner", "BET_SETTLED", "100000", "100000")
	oldWinner["venue_type_id"] = "SPORTS"
	oldWinner["business_time"] = "2026-10-20T00:20:00Z"
	identity := map[string]string{"device_hash": strings.Repeat("d", 64)}
	oldWinner["risk_identity"] = identity
	send(oldWinner, 200)
	cfg["risk_rules"] = []map[string]any{{"rule_id": "report-lifetime", "enabled": true, "dimension": "DEVICE", "period": "LIFETIME", "window_days": 0, "scope": "BENEFICIARY", "missing_identity": "ALLOW", "effective_from": "2026-10-20T00:00:00Z"}}
	pending = call("/v1/affiliate/config-requests", op, map[string]any{"config": cfg, "expected_revision": 1, "reason": "验证报表跨日去重"}, 202, "", nil)
	call("/v1/approvals/"+pending["approval_id"].(string)+"/decisions", checker, map[string]any{"decision": "APPROVE", "expected_revision": 0, "object_hash": pending["object_hash"], "reason": "复核跨日规则"}, 200, "", nil)
	clock.Store(time.Date(2026, 10, 21, 1, 0, 0, 0, time.UTC).UnixNano())
	losing := event("child2", "risk-report-next-day", "BET_SETTLED", "100000", "100000")
	losing["venue_type_id"] = "SPORTS"
	losing["business_time"] = "2026-10-21T00:20:00Z"
	losing["risk_identity"] = identity
	send(losing, 200)
	assertReportBlocked := func(day int) {
		t.Helper()
		f := flowmodel.AgentReportFilter{From: time.Date(2026, 10, day, 0, 0, 0, 0, time.UTC), To: time.Date(2026, 10, day+1, 0, 0, 0, 0, time.UTC), Sort: "became_agent_at"}
		report, err := reader.Build(ctx, sc, f, clockUTC())
		if err != nil || len(report.Rows) != 1 || report.Rows[0].Estimate != "0" {
			t.Fatal("报表漏掉跨日竞争事实", report, err)
		}
	}
	assertReportBlocked(21)
	// SITE策略在代理重新开启后仍使用全站历史赢家，而非只读取enabled_since之后的订单。
	cfg["risk_rules"].([]map[string]any)[0]["scope"] = "SITE"
	pending = call("/v1/affiliate/config-requests", op, map[string]any{"config": cfg, "expected_revision": 2, "reason": "验证重开后的全站去重"}, 202, "", nil)
	call("/v1/approvals/"+pending["approval_id"].(string)+"/decisions", checker, map[string]any{"decision": "APPROVE", "expected_revision": 0, "object_hash": pending["object_hash"], "reason": "复核全站规则"}, 200, "", nil)
	clock.Store(time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC).UnixNano())
	rootPath := "/v1/affiliate/agents/" + rootUser + "/reward-switch"
	switchBody := map[string]any{"enabled": false, "expected_revision": 0, "reason": "测试根代理重开边界", "confirmed": true}
	code, _ = security.Code(secret, uint64(clockUTC().Unix()/30), 6)
	call(rootPath, op, switchBody, 200, "root-off-risk", map[string]string{"X-TOTP-Code": code})
	clock.Store(clockUTC().Add(time.Minute).UnixNano())
	switchBody["enabled"] = true
	switchBody["expected_revision"] = 1
	code, _ = security.Code(secret, uint64(clockUTC().Unix()/30), 6)
	call(rootPath, op, switchBody, 200, "root-on-risk", map[string]string{"X-TOTP-Code": code})
	clock.Store(time.Date(2026, 10, 22, 1, 0, 0, 0, time.UTC).UnixNano())
	losing = event("child3", "risk-report-reopened", "BET_SETTLED", "100000", "100000")
	losing["venue_type_id"] = "SPORTS"
	losing["business_time"] = "2026-10-22T00:20:00Z"
	losing["risk_identity"] = identity
	send(losing, 200)
	assertReportBlocked(22)

}
