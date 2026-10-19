package affiliatewallet

import (
	"context"
	"database/sql"
	"errors"
	"time"

	flowrepo "github.com/ex-business/ExCommission/internal/affiliateflow/adapters"
	ledger "github.com/ex-business/ExCommission/internal/ledger/adapters"
	payment "github.com/ex-business/ExCommission/internal/payments/domain"
	paymentports "github.com/ex-business/ExCommission/internal/payments/ports"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
)

type ReconciliationRow struct {
	Delivery       string  `json:"delivery_id"`
	Claim          string  `json:"claim_id"`
	Reward         string  `json:"reward_id"`
	User           string  `json:"user_id"`
	Currency       string  `json:"currency"`
	Amount         string  `json:"amount_minor"`
	WalletAmount   string  `json:"wallet_amount_minor"`
	DeliveryStatus string  `json:"delivery_status"`
	WalletStatus   string  `json:"wallet_status"`
	Receipt        *string `json:"receipt_ref"`
	WalletReceipt  *string `json:"wallet_receipt_ref"`
	Paid           string  `json:"paid_minor"`
	Target         string  `json:"target_minor"`
	Debt           string  `json:"debt_minor"`
	Result         string  `json:"result"`
	Reason         string  `json:"reason"`
}
type ReconciliationReport struct {
	ObservedAt  time.Time           `json:"observed_at"`
	Statement   string              `json:"statement_ref"`
	Status      string              `json:"status"`
	Checked     int                 `json:"checked_count"`
	Matched     int                 `json:"matched_count"`
	Differences int                 `json:"difference_count"`
	Pending     int                 `json:"pending_count"`
	Rows        []ReconciliationRow `json:"rows"`
}

// ReconciliationProvider只分流已绑定作用域内的代理原单。其余原单仍交原付款服务核验，未知单不能被吞掉。
// 对账不调用Apply、Submit或Query，不确认资金、不自动重发；本报告仅核对钱包返回的指定日账单，不推断其他日期的缺单。
type ReconciliationProvider struct {
	paymentports.Provider
	Service Service
	Scope   foundation.Scope
	Report  *ReconciliationReport
}

func (p ReconciliationProvider) Reconcile(ctx context.Context, ref string) ([]payment.Outcome, error) {
	outcomes, err := p.Provider.Reconcile(ctx, ref)
	if err != nil {
		return nil, err
	}
	report := ReconciliationReport{ObservedAt: p.Service.now(), Statement: ref, Status: "NO_AFFILIATE_ORDERS", Rows: []ReconciliationRow{}}
	legacy := []payment.Outcome{}
	seen := map[string]bool{}
	for _, out := range outcomes {
		if seen[out.MerchantID] {
			return nil, errors.New("DUPLICATE_WALLET_ORIGINAL")
		}
		seen[out.MerchantID] = true
		d, err := read(ctx, p.Service.Query, p.Scope, out.MerchantID, false)
		if errors.Is(err, sql.ErrNoRows) {
			legacy = append(legacy, out)
			continue
		}
		if err != nil {
			return nil, err
		}
		if err = p.Service.checkBinding(p.Scope, d); err != nil {
			return nil, err
		}
		row := ReconciliationRow{Delivery: d.ID, Claim: d.Claim, Reward: d.Reward, User: d.User, Currency: d.Currency, Amount: d.Amount, WalletAmount: out.Amount, DeliveryStatus: d.Status, WalletStatus: out.Status, Receipt: d.Receipt, WalletReceipt: out.Receipt, Result: "MATCHED", Reason: "原单、金额、状态与回执一致"}
		// 已领冲正保持原已付额并单列追偿，目标变成零本身不构成重复付款或钱包差异。
		ent, err := (ledger.Repository{}).Get(ctx, p.Service.Query, p.Scope, d.Entitlement, false)
		if err != nil {
			return nil, err
		}
		row.Paid, row.Target, row.Debt = ent.Paid, ent.Target, ent.Debt
		if d.Currency != out.Currency || d.Amount != out.Amount {
			row.Result, row.Reason = "MISMATCH", "钱包币种或金额与领取原单不一致"
		} else if !payment.Terminal(d.Status) {
			row.Result, row.Reason = "PENDING_CONFIRMATION", "钱包已有结果，领取原单仍待确认"
		} else if d.Status != out.Status {
			row.Result, row.Reason = "MISMATCH", "钱包与领取原单的终态不一致"
		} else if d.Status == "SUCCEEDED" {
			reward, err := (flowrepo.Repository{}).Reward(ctx, p.Service.Query, p.Scope, d.Reward)
			if err != nil {
				return nil, err
			}
			if d.Receipt == nil || out.Receipt == nil || *d.Receipt != *out.Receipt {
				row.Result, row.Reason = "MISMATCH", "钱包回执缺失或与领取原单不一致"
			} else if reward.ClaimID != d.Claim || reward.UserID != d.User || reward.EntitlementID != d.Entitlement || reward.Status != "CLAIMED" || ent.Currency != d.Currency || ent.Paid != d.Amount {
				row.Result, row.Reason = "MISMATCH", "奖励归属或已付账本与领取原单不一致"
			}
		} else if d.Receipt != nil && out.Receipt != nil && *d.Receipt != *out.Receipt {
			row.Result, row.Reason = "MISMATCH", "失败原单回执不一致"
		}
		report.Checked++
		switch row.Result {
		case "MISMATCH":
			report.Differences++
		case "PENDING_CONFIRMATION":
			report.Pending++
		default:
			report.Matched++
		}
		report.Rows = append(report.Rows, row)
	}
	if report.Checked > 0 {
		report.Status = "MATCHED"
	}
	if report.Differences > 0 {
		report.Status = "MISMATCH"
	} else if report.Pending > 0 {
		report.Status = "PENDING_CONFIRMATION"
	}
	if p.Report != nil {
		*p.Report = report
	}
	return legacy, nil
}
