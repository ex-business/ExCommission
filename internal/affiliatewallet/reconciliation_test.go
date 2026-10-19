package affiliatewallet

import (
	"context"
	"testing"
	"time"

	ledger "github.com/ex-business/ExCommission/internal/ledger/adapters"
	paymentrepo "github.com/ex-business/ExCommission/internal/payments/adapters"
	paymentapp "github.com/ex-business/ExCommission/internal/payments/application"
	payment "github.com/ex-business/ExCommission/internal/payments/domain"
	paymentports "github.com/ex-business/ExCommission/internal/payments/ports"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
)

type statementProvider struct {
	*testProvider
	rows []payment.Outcome
}

func (p statementProvider) Reconcile(context.Context, string) ([]payment.Outcome, error) {
	return p.rows, nil
}
func TestSharedReconciliationIsReadOnlyAndScopeBound(t *testing.T) {
	store, sc, r, funds := walletFixture(t)
	r = claimFixture(t, store, sc, r, funds, "reconcile-batch")
	ctx := context.Background()
	provider := &testProvider{}
	s := Service{UOW: store, Query: store.DB, Provider: provider, Now: func() time.Time { return time.Now().UTC().Add(time.Minute) }}
	if e := s.Recover(ctx, sc); e != nil {
		t.Fatal(e)
	}
	if e := store.Within(ctx, func(tx db.Executor) error {
		_, _, e := funds.Adjust(ctx, tx, sc, r, "0", "reconcile-cancel", time.Now().UTC())
		return e
	}); e != nil {
		t.Fatal(e)
	}
	receipt := "receipt"
	own := payment.Outcome{MerchantID: r.WalletDelivery, Currency: "USD", Amount: "40", Status: "SUCCEEDED", Receipt: &receipt}
	old := payment.Outcome{MerchantID: "legacy-original", Currency: "USD", Amount: "5000", Status: "SUCCEEDED"}
	before, e := (ledger.Repository{}).Get(ctx, store.DB, sc, r.EntitlementID, false)
	if e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct {
		name   string
		out    payment.Outcome
		result string
	}{{"已领冲正后的原单仍一致", own, "MATCHED"}, {"错金额必须报告差异", func() payment.Outcome { v := own; v.Amount = "41"; return v }(), "MISMATCH"}, {"错回执必须报告差异", func() payment.Outcome { v := own; x := "other-receipt"; v.Receipt = &x; return v }(), "MISMATCH"}, {"错终态必须报告差异", func() payment.Outcome { v := own; v.Status = "FAILED_FINAL"; return v }(), "MISMATCH"}} {
		t.Run(tc.name, func(t *testing.T) {
			var report ReconciliationReport
			p := ReconciliationProvider{Provider: statementProvider{provider, []payment.Outcome{tc.out, old}}, Service: s, Scope: sc, Report: &report}
			legacy, e := p.Reconcile(ctx, "2026-10-19")
			if e != nil || len(legacy) != 1 || legacy[0].MerchantID != old.MerchantID || report.Checked != 1 || report.Status != tc.result || report.Rows[0].Debt != "40" || report.Rows[0].Paid != "40" {
				t.Fatal("混合账单分流或核对结果错误", legacy, report, e)
			}
		})
	}
	var report ReconciliationReport
	p := ReconciliationProvider{Provider: statementProvider{provider, []payment.Outcome{own}}, Service: s, Scope: foundation.Scope{Tenant: sc.Tenant + "-other", App: sc.App}, Report: &report}
	rows, e := p.Reconcile(ctx, "2026-10-19")
	if e != nil || report.Checked != 0 || len(rows) != 1 {
		t.Fatal("跨站原单被识别或吞掉", report, e)
	}
	// 无代理归属且也不存在于旧付款仓储的原单，必须保留原NOT_FOUND，不能被空对账吞掉。
	unknownProvider := ReconciliationProvider{Provider: statementProvider{provider, []payment.Outcome{old}}, Service: s, Scope: sc}
	oldService := paymentapp.Service{UOW: store, Query: store.DB, Repo: paymentrepo.Repository{}, Providers: map[string]paymentports.Provider{"wallet-sandbox": unknownProvider}}
	if err := oldService.Reconcile(ctx, sc, "wallet-sandbox", "2026-10-19"); err == nil {
		t.Fatal("未知原单被误报对账成功")
	}
	after, e := (ledger.Repository{}).Get(ctx, store.DB, sc, r.EntitlementID, false)
	if e != nil || before.Revision != after.Revision || after.Paid != "40" || after.Debt != "40" || provider.submits.Load() != 1 || provider.queries.Load() != 0 {
		t.Fatal("对账修改了资金或重放派发", after, e)
	}
}
func TestReconciliationDoesNotConfirmUnknownDelivery(t *testing.T) {
	store, sc, r, funds := walletFixture(t)
	r = claimFixture(t, store, sc, r, funds, "unknown-reconcile")
	provider := &testProvider{}
	s := Service{UOW: store, Query: store.DB, Provider: provider}
	receipt := "receipt"
	var report ReconciliationReport
	p := ReconciliationProvider{Provider: statementProvider{provider, []payment.Outcome{{MerchantID: r.WalletDelivery, Currency: "USD", Amount: "40", Status: "SUCCEEDED", Receipt: &receipt}}}, Service: s, Scope: sc, Report: &report}
	if _, e := p.Reconcile(context.Background(), "2026-10-19"); e != nil {
		t.Fatal(e)
	}
	ent, e := (ledger.Repository{}).Get(context.Background(), store.DB, sc, r.EntitlementID, false)
	if e != nil || ent.Reserved != "40" || ent.Paid != "0" || report.Pending != 1 || report.Status != "PENDING_CONFIRMATION" || provider.submits.Load() != 0 {
		t.Fatal("只读对账提前确认资金", report, ent, e)
	}
}
