package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	flowrepo "github.com/ex-business/ExCommission/internal/affiliateflow/adapters"
	flow "github.com/ex-business/ExCommission/internal/affiliateflow/domain"
	ledger "github.com/ex-business/ExCommission/internal/ledger/adapters"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
)

// TestAffiliateFullchainEvidence 只读核对本轮真实HTTP进程验收留下的关联和资金，不创建事实、权益或余额。
func TestAffiliateFullchainEvidence(t *testing.T) {
	dir := os.Getenv("AFFILIATE_FULLCHAIN_EVIDENCE_DIR")
	if dir == "" {
		t.Skip("仅显式全链路证据核对启用")
	}
	read := func(name string, v any) {
		t.Helper()
		raw, e := os.ReadFile(filepath.Join(dir, name))
		if e != nil {
			t.Fatal(e)
		}
		if json.Unmarshal(raw, v) != nil {
			t.Fatal("证据格式错误", name)
		}
	}
	var report struct {
		Tenant   string   `json:"tenant"`
		Reward   string   `json:"reward_id"`
		Claim    string   `json:"claim_id"`
		Requests []string `json:"request_ids"`
		External bool     `json:"external_wallet_verified"`
	}
	read("report.json", &report)
	var cfg map[string]string
	read("api/api-runtime.json", &cfg)
	if !strings.HasPrefix(report.Tenant, "fullchain-") || cfg["LOCAL_TENANT"] != report.Tenant || !strings.HasPrefix(cfg["DB_ADDRESS"], "127.0.0.1:") {
		t.Fatal("拒绝非本机隔离证据")
	}
	ctx := context.Background()
	store, e := mysql.Open(ctx, cfg["DB_ADDRESS"], cfg["DB_USER"], cfg["MYSQL_PASSWORD"], "excommission")
	if e != nil {
		t.Fatal(e)
	}
	defer store.DB.Close()
	// 使用同一只读快照，避免把不同观察时点的余额和流水误判为对账差异。
	tx, e := store.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	sc := foundation.Scope{Tenant: report.Tenant, App: cfg["LOCAL_APP"]}
	reward, e := (flowrepo.Repository{}).Reward(ctx, tx, sc, report.Reward)
	if e != nil {
		t.Fatal(e)
	}
	if reward.Status != "CLAIMED" || reward.ClaimID != report.Claim || reward.Amount != "40" {
		t.Fatal("领取关联不一致", reward.ID, reward.Status)
	}
	ent, e := (ledger.Repository{}).Get(ctx, tx, sc, reward.EntitlementID, false)
	if e != nil {
		t.Fatal(e)
	}
	if ent.Paid != "40" || ent.Target != "0" || ent.Debt != "40" {
		t.Fatal("冲正追偿账簿不一致", ent)
	}
	var count int
	var amount, statement, payment, balance string
	e = tx.QueryRowContext(ctx, `SELECT COUNT(*),CAST(COALESCE(SUM(amount),0) AS CHAR),COALESCE(MAX(statement_id),''),COALESCE(MAX(payment_id),'') FROM wallet_transactions WHERE tenant_id=? AND app_id=? AND user_id=? AND business_ref=?`, sc.Tenant, sc.App, reward.UserID, "affiliate-"+reward.ID).Scan(&count, &amount, &statement, &payment)
	if e != nil {
		t.Fatal(e)
	}
	if report.External {
		if count != 0 {
			t.Fatal("独立钱包领取重复计入内部钱包")
		}
		var external struct {
			Ledger    []struct{ Account, Amount, Currency, Merchant, Receipt string } `json:"ledger"`
			Balances  []struct{ Account, Amount, Currency string }                    `json:"balances"`
			Posts     int                                                             `json:"relay_posts"`
			Queries   int                                                             `json:"relay_queries"`
			Callbacks int                                                             `json:"callback_delivered"`
		}
		read("external-wallet.json", &external)
		if len(external.Ledger) != 1 || len(external.Balances) != 1 {
			t.Fatal("独立钱包交易或余额条数异常")
		}
		entry := external.Ledger[0]
		account := external.Balances[0]
		if entry.Merchant != reward.WalletDelivery || entry.Account != reward.UserID || entry.Amount != "40" || account.Account != reward.UserID || account.Amount != "40" || reward.WalletReceipt == nil || entry.Receipt != *reward.WalletReceipt {
			t.Fatal("独立钱包原单、账号、金额和回执不匹配")
		}
		if external.Posts != 1 || external.Queries < 1 || external.Callbacks < 1 {
			t.Fatal("未完成原单查询与回执或重复派发")
		}
		var raw []byte
		e = tx.QueryRowContext(ctx, `SELECT body FROM affiliate_wallet_deliveries WHERE tenant_id=? AND app_id=? AND delivery_id=?`, sc.Tenant, sc.App, reward.WalletDelivery).Scan(&raw)
		var delivery struct {
			Claim    string `json:"claim_id"`
			Reward   string `json:"reward_id"`
			Status   string `json:"status"`
			Callback string `json:"callback_id"`
			Site     string `json:"wallet_site_id"`
			Client   string `json:"wallet_client_id"`
		}
		if e != nil || json.Unmarshal(raw, &delivery) != nil || delivery.Claim != report.Claim || delivery.Reward != reward.ID || delivery.Status != "SUCCEEDED" {
			t.Fatal("派发记录没有关联同一领取和奖励", e)
		}
		if delivery.Callback == "" || delivery.Site == "" || delivery.Client == "" {
			t.Fatal("缺少钱包绑定或回执关联")
		}
		var audited int
		if e = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_entries WHERE tenant_id=? AND app_id=? AND resource_ref=? AND action IN ('affiliate.wallet.confirm','affiliate.wallet.receipt')`, sc.Tenant, sc.App, reward.WalletDelivery).Scan(&audited); e != nil || audited < 2 {
			t.Fatal("缺少查询确认及回执审计", audited, e)
		}

		balance = account.Amount
	} else {
		if count != 1 || amount != "40" || statement != reward.ID || payment != report.Claim {
			t.Fatal("钱包交易与领取不匹配", count, amount, statement, payment)
		}
		e = tx.QueryRowContext(ctx, `SELECT CAST(balance AS CHAR) FROM wallet_accounts WHERE tenant_id=? AND app_id=? AND user_id=? AND currency='USD'`, sc.Tenant, sc.App, reward.UserID).Scan(&balance)
		if e != nil || balance != "40" {
			t.Fatal("钱包余额核对失败", e, balance)
		}
	}
	var facts []flow.Event
	read("source-facts.json", &facts)
	if len(facts) != 2 {
		t.Fatal("来源事实数量异常")
	}
	for _, event := range facts {
		var raw []byte
		e = tx.QueryRowContext(ctx, `SELECT body FROM affiliate_business_facts WHERE tenant_id=? AND app_id=? AND source_id=? AND event_id=? AND business_id=?`, sc.Tenant, sc.App, event.Source, event.EventID, event.BusinessID).Scan(&raw)
		var saved flow.Event
		if e != nil || json.Unmarshal(raw, &saved) != nil || saved.BusinessHash() != event.BusinessHash() {
			t.Fatal("来源与业务事实关联不一致", event.EventID, e)
		}
	}
	var linked int
	for _, rid := range report.Requests {
		var n int
		if e = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_entries WHERE tenant_id=? AND app_id=? AND request_id=?`, sc.Tenant, sc.App, rid).Scan(&n); e != nil {
			t.Fatal(e)
		}
		if n > 0 {
			linked++
		}
	}
	if linked == 0 {
		t.Fatal("请求编号无法关联业务审计")
	}
	proof := map[string]any{"result": "PASS", "tenant": sc.Tenant, "reward_id": reward.ID, "claim_id": report.Claim, "entitlement_id": reward.EntitlementID, "wallet_transactions": count, "wallet_balance_minor": balance, "paid_minor": ent.Paid, "restated_target_minor": ent.Target, "recovery_debt_minor": ent.Debt, "source_facts_matched": len(facts), "request_ids_matched_to_audit": linked, "read_only": true, "external_wallet_verified": report.External}
	raw, _ := json.MarshalIndent(proof, "", "  ")
	if e = os.WriteFile(filepath.Join(dir, "reconciliation.json"), raw, 0600); e != nil {
		t.Fatal(e)
	}
	t.Log("来源事实、权益、领取、钱包交易、追偿及请求审计只读核对通过")
}
