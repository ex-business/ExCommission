package runtime

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	flow "github.com/ex-business/ExCommission/internal/affiliateflow/application"
	model "github.com/ex-business/ExCommission/internal/affiliateflow/domain"
	programs "github.com/ex-business/ExCommission/internal/affiliateprograms"
	principal "github.com/ex-business/ExCommission/internal/identity/domain"
	ledger "github.com/ex-business/ExCommission/internal/ledger/adapters"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	wallet "github.com/ex-business/ExCommission/internal/wallet/adapters"
	"sync/atomic"
	"testing"
	"time"
)

func affiliateProgramsAcceptance(t *testing.T, ctx context.Context, store *mysql.Store, sc foundation.Scope, base time.Time, clock *atomic.Int64, root map[string]any, members []map[string]any, cfg map[string]any, op, checker, key string, call func(string, string, any, int, string, map[string]string) map[string]any, send func(map[string]any, int) map[string]any, event func(string, string, string, string, string) map[string]any) {
	t.Helper()
	cfgRaw, _ := json.Marshal(cfg)
	var change programs.Change
	json.Unmarshal(cfgRaw, &change.Config)
	change.ID = "parallel-a"
	change.Name = "并行方案A"
	change.State = "READY"
	change.Effective = clockUTC()
	change.Reason = "验证多方案生命周期"
	change.Match.Mode = "STACK"
	approve := func(in programs.Change) {
		t.Helper()
		pending := call("/v1/affiliate/programs/requests", op, in, 202, "", nil)
		path := "/v1/approvals/" + pending["approval_id"].(string) + "/decisions"
		body := map[string]any{"decision": "APPROVE", "expected_revision": 0, "object_hash": pending["object_hash"], "reason": "独立审核方案"}
		call(path, op, body, 403, "", nil)
		call(path, checker, body, 200, "", nil)
	}
	approve(change)
	// 申请权限与审批权限分离，预览也不能借用审批角色写配置。
	denied := change
	denied.ID = "approver-cannot-write"
	denied.Expected = 0
	call("/v1/affiliate/programs/requests", checker, denied, 403, "", nil)
	passed := change
	passed.ID = "past-time"
	passed.Effective = base.Add(-time.Minute)
	call("/v1/affiliate/programs/preview", op, passed, 409, "", nil)
	s := affiliateFlowService(store)
	ps := programService(store, s)
	rootUser := root["user_id"].(string)
	childUser := members[0]["user_id"].(string)
	sendNow := func(external, business, kind, amount, bet string) {
		in := event(external, business, kind, amount, bet)
		in["business_time"] = clockUTC().Format(time.RFC3339Nano)
		if kind == "BET_SETTLED" {
			in["venue_type_id"] = "SPORTS"
		}
		send(in, 200)
		if send(in, 200)["accepted"] != false {
			t.Fatal("重复原单重新计算")
		}
	}
	sendNow("child1", "before-start-deposit", "DEPOSIT_SUCCEEDED", "100", "0")
	sendNow("child1", "before-start-bet", "BET_SETTLED", "300", "300")
	rewards, _ := s.Repo.Rewards(ctx, store.DB, sc, rootUser)
	for _, r := range rewards {
		if r.ProgramID != "" {
			t.Fatal("未启动产生奖励", r)
		}
	}
	clock.Store(clockUTC().Add(time.Minute).UnixNano())
	change.Expected = 1
	change.State = "RUNNING"
	change.Effective = clockUTC()
	approve(change)
	second := change
	second.ID = "parallel-b"
	second.Name = "并行方案B"
	second.Expected = 0
	approve(second)
	sendNow("child1", "parallel-deposit", "DEPOSIT_SUCCEEDED", "100", "0")
	sendNow("child1", "parallel-bet", "BET_SETTLED", "30000", "30000")
	rewards, e := s.Repo.Rewards(ctx, store.DB, sc, rootUser)
	if e != nil {
		t.Fatal(e)
	}
	count := 0
	for _, r := range rewards {
		if r.ProgramID != "" {
			count++
			if r.ProgramConfig == nil {
				t.Fatal("方案快照缺失")
			}
		}
	}
	if count != 2 {
		t.Fatal("两个叠加方案没有独立发奖", rewards)
	}
	// 先让A日结，再停止B；B关闭后不得创建任何新的结算单，即使事实与水位都齐全。
	clock.Store(time.Date(2026, 10, 19, 0, 10, 0, 0, time.UTC).UnixNano())
	second.Expected = 1
	second.State = "STOPPED"
	second.Effective = clockUTC()
	approve(second)
	// 没有任何来源水位时，实际方案任务也必须可见，停止方案只保留阻止记录。
	clock.Store(time.Date(2026, 10, 19, 1, 0, 0, 0, time.UTC).UnixNano())
	if e := ps.RunOnce(ctx, sc); e != nil {
		t.Fatal(e)
	}
	taskRows, e := affiliateTaskManager(store).Repo.List(ctx, store.DB, sc, "", "")
	if e != nil {
		t.Fatal(e)
	}
	observed := map[string]string{}
	for _, task := range taskRows {
		if task.Day == "2026-10-18" {
			observed[task.Program] = task.Status
		}
	}
	if observed["parallel-a"] != "WAITING_WATERMARK" || observed["parallel-b"] != "BLOCKED" {
		t.Fatal("真实方案任务依赖或停止状态不符", taskRows)
	}
	date, _ := time.Parse("2006-01-02", "2026-10-18")
	n, digest, e := s.DailyRepo.Manifest(ctx, store.DB, sc, "sandbox", date, date.AddDate(0, 0, 1))
	if e != nil {
		t.Fatal(e)
	}
	mark := model.Watermark{Source: "sandbox", Day: "2026-10-18", Count: n, Digest: digest}
	raw, _ := json.Marshal(mark)
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte("affiliate-watermark-v1\n" + sc.Tenant + "\n" + sc.App + "\nsandbox\n"))
	mac.Write(raw)
	// 水位直接使用原端口保存，来源验签由已有完整链路回归覆盖；此测试聚焦运行开关与日结事务。
	if e = s.DailyRepo.SaveWatermark(ctx, store.DB, sc, mark, clockUTC()); e != nil {
		t.Fatal(e)
	}
	clock.Store(time.Date(2026, 10, 19, 1, 0, 0, 0, time.UTC).UnixNano())
	if e = ps.RunOnce(ctx, sc); e != nil {
		t.Fatal(e)
	}
	if e = ps.RunOnce(ctx, sc); e != nil {
		t.Fatal(e)
	}
	days, e := ps.Repo.Days(ctx, store.DB, sc)
	if e != nil || len(days) != 1 || days[0].ProgramID != "parallel-a" {
		t.Fatal("停止方案发生新结算", days, e)
	}
	taskRows, e = affiliateTaskManager(store).Repo.List(ctx, store.DB, sc, "", "")
	if e != nil {
		t.Fatal(e)
	}
	for _, task := range taskRows {
		if task.Day == "2026-10-18" && task.Program == "parallel-a" && (task.Status != "SUCCEEDED" || task.Attempts != 1) {
			t.Fatal("已结算任务未确认或重复运行", task)
		}
	}
	// A已有待审核单仍能在停止后审核和领取；B未结算事实不会在恢复时自动补算。
	change.Expected = 2
	change.State = "STOPPED"
	change.Effective = clockUTC()
	approve(change)
	p := principal.Principal{Tenant: sc.Tenant, App: sc.App, Subject: "member-program", MemberUserID: rootUser}
	if e = bootstrapFeature(ctx, store, sc.Tenant, sc.App, p.Subject, "program-member", []string{"member:read", "member:claim"}); e != nil {
		t.Fatal(e)
	}
	rewards, _ = s.Repo.Rewards(ctx, store.DB, sc, rootUser)
	var commission model.Reward
	for _, r := range rewards {
		if r.ProgramID == "parallel-a" && r.Kind == model.Commission {
			commission = r
		}
	}
	if commission.ID == "" {
		t.Fatal("独立方案日结佣金缺失", rewards)
	}
	reviewer := principal.Principal{Tenant: sc.Tenant, App: sc.App, Subject: "local-approver"}
	out, e := s.Review(ctx, reviewer, commission.ID, flow.Review{Decision: "APPROVE", Revision: 1, Reason: "验证停止后旧权益审核"}, "review-program", "review-program")
	if e != nil || out.Status != 200 {
		t.Fatal(out, e)
	}
	out, e = s.Claim(ctx, p, flow.Claim{Kind: "ALL", ProgramID: "parallel-a"}, "127.0.0.1", "claim-program", "claim-program")
	if e != nil || out.Status != 200 {
		t.Fatal("停止后旧权益领取失败", out, e)
	}
	rewards, _ = s.Repo.Rewards(ctx, store.DB, sc, rootUser)
	for _, r := range rewards {
		if r.ProgramID == "parallel-b" && r.Status == "CLAIMED" {
			t.Fatal("领取串到其他方案")
		}
	}
	accounts, e := (wallet.Repository{}).Accounts(ctx, store.DB, sc, rootUser)
	if e != nil || len(accounts) == 0 {
		t.Fatal("领取未入账", accounts, e)
	}
	clock.Store(clockUTC().Add(time.Minute).UnixNano())
	second.Expected = 2
	second.State = "RUNNING"
	second.Effective = clockUTC()
	approve(second)
	if e = ps.RunOnce(ctx, sc); e != nil {
		t.Fatal(e)
	}
	days, _ = ps.Repo.Days(ctx, store.DB, sc)
	if len(days) != 1 {
		t.Fatal("恢复后自动补结算旧区间", days)
	}
	// 原方案即使已停止，原充值退款仍按原冻结配置生成追偿，并保持钱包已入账流水。
	reversal := model.Reversal{Source: "sandbox", EventID: "program-refund", BusinessID: "program-refund", OriginalID: "event-parallel-deposit", OriginalBusiness: "parallel-deposit", ExternalUser: "child1", Kind: "DEPOSIT_REFUNDED", Currency: "USD", Amount: "100", ValidBet: "0", At: clockUTC(), Reason: "验证独立方案已领取追偿"}
	revraw, _ := json.Marshal(reversal)
	revMac := hmac.New(sha256.New, []byte(key))
	revMac.Write([]byte("affiliate-reversal-v1\n" + sc.Tenant + "\n" + sc.App + "\nsandbox\n"))
	revMac.Write(revraw)
	call("/v1/affiliate/reversals", op, reversal, 200, "program-reversal", map[string]string{"X-Affiliate-Signature": hex.EncodeToString(revMac.Sum(nil))})
	rewards, _ = s.Repo.Rewards(ctx, store.DB, sc, rootUser)
	found := false
	for _, r := range rewards {
		if r.ProgramID == "parallel-a" && r.Kind == model.Referral {
			ent, e := (ledger.Repository{}).Get(ctx, store.DB, sc, r.EntitlementID, false)
			if e != nil || ent.Debt != "100" || r.Status != "CLAIMED" {
				t.Fatal("原方案已领追偿丢失", r, ent, e)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("原奖励丢失")
	}
	// 不同站点读不到本次方案版本或命中记录。
	other, e := ps.Summary(ctx, foundation.Scope{Tenant: sc.Tenant + "-other", App: sc.App})
	if e != nil || len(other["versions"].([]programs.Version)) != 0 {
		t.Fatal("跨站读取泄漏", e)
	}
	// 同组优先级并列、互斥重叠必须在发布前拒绝；不同优先级允许并行发布。
	clock.Store(clockUTC().Add(time.Minute).UnixNano())
	high := second
	high.ID = "priority-high"
	high.Expected = 0
	high.Effective = clockUTC()
	high.Match.Mode = "PRIORITY"
	high.Match.Group = "promotion"
	high.Match.Priority = 10
	approve(high)
	low := high
	low.ID = "priority-low"
	call("/v1/affiliate/programs/preview", op, low, 409, "", nil)
	low.Match.Priority = 5
	approve(low)
	exclusive := high
	exclusive.ID = "exclusive-conflict"
	exclusive.Match.Mode = "EXCLUSIVE"
	call("/v1/affiliate/programs/preview", op, exclusive, 409, "", nil)
	sendNow("child2", "priority-deposit", "DEPOSIT_SUCCEEDED", "100", "0")
	summary, e := ps.Summary(ctx, sc)
	if e != nil {
		t.Fatal(e)
	}
	selected := map[string]bool{}
	for _, receipt := range summary["receipts"].([]programs.Receipt) {
		if receipt.BusinessID == "priority-deposit" {
			for _, d := range receipt.Decisions {
				if d.Selected {
					selected[d.ProgramID] = true
				}
			}
		}
	}
	if !selected["priority-high"] || selected["priority-low"] {
		t.Fatal("优先级未按配置命中", selected)
	}
	// 独立BTC方案与USD兼容方案共享会员，事实、奖励精度和钱包账户必须保持币种隔离。
	clock.Store(clockUTC().Add(time.Minute).UnixNano())
	btc := second
	btc.ID = "btc-program"
	btc.Name = "BTC独立方案"
	btc.Expected = 0
	btc.Effective = clockUTC()
	btc.Match.Mode = "STACK"
	btc.Match.Group = ""
	btc.Config.Currency = "BTC"
	btc.Config.CurrencyScale = 8
	approve(btc)
	for _, entry := range []struct{ business, kind, amount, bet string }{{"btc-deposit", "DEPOSIT_SUCCEEDED", "100", "0"}, {"btc-bet", "BET_SETTLED", "300", "300"}} {
		in := event("child3", entry.business, entry.kind, entry.amount, entry.bet)
		in["currency"] = "BTC"
		in["business_time"] = clockUTC().Format(time.RFC3339Nano)
		if entry.kind == "BET_SETTLED" {
			in["venue_type_id"] = "SPORTS"
		}
		send(in, 200)
	}
	out, e = s.Claim(ctx, p, flow.Claim{Kind: "ALL", ProgramID: "btc-program"}, "127.0.0.1", "claim-btc", "claim-btc")
	if e != nil || out.Status != 200 {
		t.Fatal("独立BTC方案无法领取", out, e)
	}
	accounts, e = (wallet.Repository{}).Accounts(ctx, store.DB, sc, rootUser)
	if e != nil || len(accounts) != 2 {
		t.Fatal("钱包账户币种混合", accounts, e)
	}
	_ = childUser
}
