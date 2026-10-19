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
	ledger "github.com/ex-business/ExCommission/internal/ledger/adapters"
	mysql "github.com/ex-business/ExCommission/internal/platform/adapters/mysql"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
)

// 浏览器验收只读核对；领取、审核、撤单、对账均由页面完成，本测试不得补写业务结果。
func TestAffiliateBrowserEvidence(t *testing.T) {
	dir := os.Getenv("AFFILIATE_BROWSER_EVIDENCE_DIR")
	if dir == "" {
		t.Skip("仅显式浏览器验收启用")
	}
	read := func(name string, v any) {
		t.Helper()
		raw, e := os.ReadFile(filepath.Join(dir, name))
		if e != nil || json.Unmarshal(raw, v) != nil {
			t.Fatal("证据文件不可读", name, e)
		}
	}
	var config map[string]string
	read("api/api-runtime.json", &config)
	var run struct {
		Tenant string `json:"tenant"`
	}
	read("browser.json", &run)
	var ids struct{ Reward, Merchant, Receipt, Parent, Child, Claim, Callback, Request, Reconciliation string }
	read("browser-ids.json", &ids)
	var external struct {
		Balance string `json:"wallet_balance_minor"`
		Count   int    `json:"wallet_ledger_count"`
	}
	read("browser-financial-snapshot.json", &external)
	if !strings.HasPrefix(run.Tenant, "fullchain-") || run.Tenant != config["LOCAL_TENANT"] || !strings.HasPrefix(config["DB_ADDRESS"], "127.0.0.1:") {
		t.Fatal("拒绝非隔离本机证据")
	}
	ctx := context.Background()
	store, e := mysql.Open(ctx, config["DB_ADDRESS"], config["DB_USER"], config["MYSQL_PASSWORD"], "excommission")
	if e != nil {
		t.Fatal(e)
	}
	defer store.DB.Close()
	tx, e := store.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	sc := foundation.Scope{Tenant: run.Tenant, App: config["LOCAL_APP"]}
	reward, e := (flowrepo.Repository{}).Reward(ctx, tx, sc, ids.Reward)
	if e != nil {
		t.Fatal(e)
	}
	ent, e := (ledger.Repository{}).Get(ctx, tx, sc, reward.EntitlementID, false)
	if e != nil {
		t.Fatal(e)
	}
	if reward.Status != "CLAIMED" || reward.ClaimID != ids.Claim || reward.WalletDelivery != ids.Merchant || reward.WalletReceipt == nil || *reward.WalletReceipt != ids.Receipt || reward.UserID != ids.Parent || ent.Paid != "40" || ent.Target != "0" || ent.Debt != "40" || external.Count != 1 || external.Balance != "40" {
		t.Fatal("浏览器同单资金关联不一致", reward.ID, ent)
	}
	var raw []byte
	if e = tx.QueryRowContext(ctx, `SELECT result_body FROM command_results WHERE tenant_id=? AND app_id=? AND operation='payment-reconcile' AND resource_id=?`, sc.Tenant, sc.App, ids.Reconciliation).Scan(&raw); e != nil {
		t.Fatal(e)
	}
	var response struct {
		Data struct {
			Report struct {
				Status      string `json:"status"`
				Count       int    `json:"checked_count"`
				Matched     int    `json:"matched_count"`
				Differences int    `json:"difference_count"`
				Rows        []struct {
					Delivery string `json:"delivery_id"`
					Debt     string `json:"debt_minor"`
					Paid     string `json:"paid_minor"`
					Target   string `json:"target_minor"`
				} `json:"rows"`
			} `json:"affiliate_wallet"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &response) != nil {
		t.Fatal("对账结果不可读")
	}
	report := response.Data.Report
	if report.Status != "MATCHED" || report.Count != 1 || report.Matched != 1 || report.Differences != 0 || len(report.Rows) != 1 || report.Rows[0].Delivery != ids.Merchant || report.Rows[0].Debt != "40" || report.Rows[0].Paid != "40" || report.Rows[0].Target != "0" {
		t.Fatal("浏览器对账结果未持久化或不一致", report)
	}
	var internal int
	if e = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM wallet_transactions WHERE tenant_id=? AND app_id=? AND user_id=?`, sc.Tenant, sc.App, ids.Parent).Scan(&internal); e != nil || internal != 0 {
		t.Fatal("内部钱包重复入账", internal, e)
	}
	proof := map[string]any{"result": "PASS", "tenant": sc.Tenant, "claim_id": ids.Claim, "reward_id": ids.Reward, "delivery_id": ids.Merchant, "receipt_ref": ids.Receipt, "reconciliation_id": ids.Reconciliation, "paid_minor": ent.Paid, "target_minor": ent.Target, "debt_minor": ent.Debt, "external_wallet_balance_minor": external.Balance, "external_wallet_ledger_count": external.Count, "internal_wallet_ledger_count": internal, "read_only": true}
	body, _ := json.MarshalIndent(proof, "", "  ")
	if e = os.WriteFile(filepath.Join(dir, "browser-reconciliation-proof.json"), body, 0600); e != nil {
		t.Fatal(e)
	}
	t.Log("浏览器同笔领取、独立钱包、已领追偿和持久化对账结果只读核对通过")
}
