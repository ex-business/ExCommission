package affiliatewallet

import (
	"context"
	"errors"
	"os"
	"regexp"
	"sync/atomic"
	"testing"
	"time"

	flowrepo "github.com/ex-business/ExCommission/internal/affiliateflow/adapters"
	flow "github.com/ex-business/ExCommission/internal/affiliateflow/domain"
	ledger "github.com/ex-business/ExCommission/internal/ledger/adapters"
	payment "github.com/ex-business/ExCommission/internal/payments/domain"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
)

type testProvider struct {
	submits, queries atomic.Int64
	fail             bool
	started, release chan struct{}
}

func (*testProvider) Enabled() bool { return true }

func (p *testProvider) Submit(_ context.Context, v payment.Payment) (payment.Outcome, error) {
	p.submits.Add(1)
	if p.started != nil {
		close(p.started)
		<-p.release
	}
	if p.fail {
		return payment.Outcome{}, errors.New("模拟提交后超时")
	}
	receipt := "receipt"
	return payment.Outcome{MerchantID: v.MerchantID, Currency: v.Currency, Amount: v.Amount, Status: "SUCCEEDED", Receipt: &receipt}, nil
}
func (p *testProvider) Query(_ context.Context, v payment.Payment) (payment.Outcome, error) {
	p.queries.Add(1)
	receipt := "receipt"
	return payment.Outcome{MerchantID: v.MerchantID, Currency: v.Currency, Amount: v.Amount, Status: "SUCCEEDED", Receipt: &receipt}, nil
}
func (*testProvider) VerifyCallback([]byte, string) (payment.Outcome, error) {
	return payment.Outcome{}, errors.New("测试提供方不接收回执")
}
func (*testProvider) Reconcile(context.Context, string) ([]payment.Outcome, error) { return nil, nil }
func walletFixture(t *testing.T) (*mysql.Store, foundation.Scope, flow.Reward, Funds) {
	t.Helper()
	if os.Getenv("TEST_MYSQL_ADDRESS") == "" {
		t.Skip("需要本机MySQL")
	}
	ctx := context.Background()
	store, e := mysql.Open(ctx, os.Getenv("TEST_MYSQL_ADDRESS"), "excommission", os.Getenv("MYSQL_PASSWORD"), "excommission")
	if e != nil {
		t.Fatal(e)
	}
	if e = store.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	sc := foundation.Scope{Tenant: "test-aff-wallet-" + flow.ID(t.Name(), time.Now().String())[:16], App: "commission"}
	t.Cleanup(func() {
		rows, e := store.DB.Query(`SELECT TABLE_NAME FROM information_schema.columns WHERE TABLE_SCHEMA=DATABASE() AND COLUMN_NAME='tenant_id'`)
		if e != nil {
			t.Error(e)
			return
		}
		var tables []string
		for rows.Next() {
			var name string
			if e = rows.Scan(&name); e != nil {
				t.Error(e)
				break
			}
			if !regexp.MustCompile(`^[a-z_]+$`).MatchString(name) {
				t.Fatal("非法表名")
			}
			tables = append(tables, name)
		}
		rows.Close()
		for _, name := range tables {
			if _, e = store.DB.Exec("DELETE FROM `"+name+"` WHERE tenant_id=?", sc.Tenant); e != nil {
				t.Error(e)
			}
		}
		store.DB.Close()
	})
	now := time.Now().UTC()
	r := flow.Reward{ID: flow.ID(sc.Tenant, "reward"), UserID: "member", AgentID: "agent", Kind: "COMMISSION", Currency: "USD", Amount: "40", Original: "40", ConfigRevision: 1, Status: "CLAIMABLE", UnlockedAt: now, CreatedAt: now, UpdatedAt: now, Revision: 1}
	funds := Funds{Funds: flowrepo.Funds{}}
	e = store.Within(ctx, func(tx db.Executor) error {
		var e error
		r.EntitlementID, e = funds.Unlock(ctx, tx, sc, r, now)
		if e != nil {
			return e
		}
		return (flowrepo.Repository{}).SaveReward(ctx, tx, sc, r)
	})
	if e != nil {
		t.Fatal(e)
	}
	return store, sc, r, funds
}
func claimFixture(t *testing.T, store *mysql.Store, sc foundation.Scope, r flow.Reward, funds Funds, batch string) flow.Reward {
	t.Helper()
	ctx := foundation.WithRequest(context.Background(), "request-test", "member")
	e := store.Within(ctx, func(tx db.Executor) error {
		if e := (flowrepo.Repository{}).Lock(ctx, tx, sc); e != nil {
			return e
		}
		if e := funds.Claim(ctx, tx, sc, r, batch, time.Now().UTC()); e != nil {
			return e
		}
		r.Revision++
		r.Status = "CLAIMING"
		r.ClaimID = batch
		r.WalletDelivery = flow.ID("affiliate-wallet", r.ID, batch)
		r.WalletDestination = "wallet-sandbox"
		return (flowrepo.Repository{}).SaveReward(ctx, tx, sc, r)
	})
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func TestUnknownRecoveryAndCallbackIdempotency(t *testing.T) {
	store, sc, r, funds := walletFixture(t)
	r = claimFixture(t, store, sc, r, funds, "batch-one")
	ctx := context.Background()
	now := time.Now().UTC().Add(time.Minute)
	p := &testProvider{fail: true}
	s := Service{UOW: store, Query: store.DB, Provider: p, Now: func() time.Time { return now }}
	if e := s.Recover(ctx, sc); e != nil {
		t.Fatal(e)
	}
	ent, e := (ledger.Repository{}).Get(ctx, store.DB, sc, r.EntitlementID, false)
	if e != nil || ent.Reserved != "40" || ent.Paid != "0" {
		t.Fatal("超时提前确认或释放预留", ent, e)
	}
	e = store.Within(ctx, func(tx db.Executor) error {
		_, _, e := funds.Adjust(ctx, tx, sc, r, "0", "reverse-pending", now)
		return e
	})
	if e == nil || e.Error() != "PAYMENT_RESULT_UNKNOWN" {
		t.Fatal("未知派发必须阻止冲正", e)
	}
	wrong := payment.Outcome{MerchantID: r.WalletDelivery, Currency: "USD", Amount: "41", Status: "SUCCEEDED"}
	if e = s.Apply(ctx, sc, wrong); e == nil {
		t.Fatal("错金额不能确认")
	}
	wrong.Amount = "40"
	if e = s.Apply(ctx, foundation.Scope{Tenant: "other", App: sc.App}, wrong); e == nil {
		t.Fatal("跨站不能确认")
	}
	now = now.Add(2 * time.Second)
	if e = s.Recover(ctx, sc); e != nil {
		t.Fatal(e)
	}
	if p.submits.Load() != 1 || p.queries.Load() != 1 {
		t.Fatal("未知恢复重复提交", p.submits.Load(), p.queries.Load())
	}
	if e = s.Apply(ctx, sc, wrong); e != nil {
		t.Fatal(e)
	}
	if e = s.Apply(ctx, sc, wrong); e != nil {
		t.Fatal(e)
	}
	ent, e = (ledger.Repository{}).Get(ctx, store.DB, sc, r.EntitlementID, false)
	if e != nil || ent.Paid != "40" || ent.Reserved != "0" {
		t.Fatal("重复回执改变金额", ent, e)
	}
	receiptConflict := "other-receipt"
	conflicting := wrong
	conflicting.Receipt = &receiptConflict
	if e = s.Apply(ctx, sc, conflicting); e == nil {
		t.Fatal("冲突回执号不能覆盖原回执")
	}
	wrong.Status = "FAILED_FINAL"
	if e = s.Apply(ctx, sc, wrong); e == nil {
		t.Fatal("终态冲突不能改写成功")
	}
}
func TestExplicitFailureCanReclaimAndPreservesOtherHolds(t *testing.T) {
	store, sc, r, funds := walletFixture(t)
	r = claimFixture(t, store, sc, r, funds, "batch-failed")
	ctx := context.Background()
	s := Service{UOW: store, Query: store.DB}
	if e := s.Apply(ctx, sc, payment.Outcome{MerchantID: r.WalletDelivery, Currency: "USD", Amount: "40", Status: "FAILED_FINAL"}); e != nil {
		t.Fatal(e)
	}
	retry, e := (flowrepo.Repository{}).Reward(ctx, store.DB, sc, r.ID)
	if e != nil || retry.Status != "CLAIMABLE" {
		t.Fatal(retry, e)
	}
	if e = (ledger.Repository{}).Hold(ctx, store.DB, sc, r.EntitlementID, "other-risk", "MANUAL_REVIEW", time.Now().UTC()); e != nil {
		t.Fatal(e)
	}
	e = store.Within(ctx, func(tx db.Executor) error { return funds.Claim(ctx, tx, sc, retry, "blocked", time.Now().UTC()) })
	if e == nil || e.Error() != "FUNDS_FROZEN" {
		t.Fatal("不能解除其他风控", e)
	}
	if e = (ledger.Repository{}).CloseHold(ctx, store.DB, sc, r.EntitlementID, "other-risk", time.Now().UTC()); e != nil {
		t.Fatal(e)
	}
	retry = claimFixture(t, store, sc, retry, funds, "batch-retry")
	if retry.WalletDelivery == r.WalletDelivery {
		t.Fatal("明确失败后新领取仍复用失败原单")
	}
	if e = s.Apply(ctx, sc, payment.Outcome{MerchantID: retry.WalletDelivery, Currency: "USD", Amount: "40", Status: "SUCCEEDED"}); e != nil {
		t.Fatal(e)
	}
	ent, e := (ledger.Repository{}).Get(ctx, store.DB, sc, r.EntitlementID, false)
	if e != nil || ent.Paid != "40" || ent.Reserved != "0" {
		t.Fatal(ent, e)
	}
}
func TestConcurrentWorkersDispatchOnce(t *testing.T) {
	store, sc, r, funds := walletFixture(t)
	claimFixture(t, store, sc, r, funds, "concurrent")
	ctx := context.Background()
	p := &testProvider{started: make(chan struct{}), release: make(chan struct{})}
	s := Service{UOW: store, Query: store.DB, Provider: p}
	done := make(chan error, 1)
	go func() { done <- s.Recover(ctx, sc) }()
	select {
	case <-p.started:
	case <-time.After(5 * time.Second):
		t.Fatal("未开始派发")
	}
	if e := s.Recover(ctx, sc); e != nil {
		t.Error(e)
	}
	close(p.release)
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	if p.submits.Load() != 1 {
		t.Fatal("并发重复派发", p.submits.Load())
	}
}

func TestWalletBindingCannotChangeAfterClaim(t *testing.T) {
	store, sc, r, funds := walletFixture(t)
	funds.Binding = func(foundation.Scope) (string, string, error) { return "1000", "wallet-a", nil }
	r = claimFixture(t, store, sc, r, funds, "binding")
	provider := &testProvider{}
	s := Service{UOW: store, Query: store.DB, Provider: provider, Binding: func(foundation.Scope) (string, string, error) { return "1001", "wallet-b", nil }}
	if e := s.Recover(context.Background(), sc); e == nil || e.Error() != "WALLET_BINDING_CHANGED" {
		t.Fatal("配置变化不能重定向原领取", e)
	}
	if provider.submits.Load() != 0 {
		t.Fatal("错误站点已收到派发")
	}
	if e := s.Apply(context.Background(), sc, payment.Outcome{MerchantID: r.WalletDelivery, Currency: "USD", Amount: "40", Status: "SUCCEEDED"}); e == nil {
		t.Fatal("错误客户端不能确认原单")
	}
}

func TestUnsupportedWalletCurrencyDoesNotReserve(t *testing.T) {
	store, sc, r, funds := walletFixture(t)
	r.Currency = "USD_TRC20"
	e := store.Within(context.Background(), func(tx db.Executor) error {
		return funds.Claim(context.Background(), tx, sc, r, "unsupported", time.Now().UTC())
	})
	if e == nil {
		t.Fatal("独立钱包不支持的币种必须先拒绝")
	}
	ent, e := (ledger.Repository{}).Get(context.Background(), store.DB, sc, r.EntitlementID, false)
	if e != nil || ent.Reserved != "0" || ent.Pending != "40" {
		t.Fatal("能力校验失败仍占用资金", ent, e)
	}
}
