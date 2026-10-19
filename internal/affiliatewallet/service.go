// Package affiliatewallet 将代理领取持久化为独立钱包原单；网络调用不进入资金事务。
package affiliatewallet

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"math/big"
	"regexp"
	"time"

	flowrepo "github.com/ex-business/ExCommission/internal/affiliateflow/adapters"
	flow "github.com/ex-business/ExCommission/internal/affiliateflow/domain"
	audit "github.com/ex-business/ExCommission/internal/audit/adapters"
	ledger "github.com/ex-business/ExCommission/internal/ledger/adapters"
	payment "github.com/ex-business/ExCommission/internal/payments/domain"
	paymentports "github.com/ex-business/ExCommission/internal/payments/ports"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
)

var walletCurrency = regexp.MustCompile(`^[A-Z]{3,10}$`)

type Delivery struct {
	CallbackID   string    `json:"callback_id,omitempty"`
	WalletSite   string    `json:"wallet_site_id,omitempty"`
	WalletClient string    `json:"wallet_client_id,omitempty"`
	ID           string    `json:"delivery_id"`
	Reward       string    `json:"reward_id"`
	Claim        string    `json:"claim_id"`
	User         string    `json:"user_id"`
	Entitlement  string    `json:"entitlement_id"`
	Currency     string    `json:"currency"`
	Amount       string    `json:"amount_minor"`
	Status       string    `json:"status"`
	RequestID    string    `json:"request_id"`
	Receipt      *string   `json:"receipt_ref,omitempty"`
	Error        string    `json:"last_error,omitempty"`
	Updated      time.Time `json:"updated_at"`
}

func (d Delivery) payment(sc foundation.Scope) payment.Payment {
	return payment.Payment{MerchantID: d.ID, Currency: d.Currency, Amount: d.Amount, WalletTenant: sc.Tenant, WalletApp: sc.App, WalletAccount: d.User}
}
func owner(reward string) string   { return flow.ID("affiliate-claim-owner", reward) }
func failureHold(id string) string { return flow.ID("affiliate-wallet-failure", id) }
func save(ctx context.Context, tx db.Executor, sc foundation.Scope, d Delivery) error {
	raw, e := json.Marshal(d)
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `UPDATE affiliate_wallet_deliveries SET status=?,body=?,updated_at=? WHERE tenant_id=? AND app_id=? AND delivery_id=?`, d.Status, raw, d.Updated, sc.Tenant, sc.App, d.ID)
	return e
}
func read(ctx context.Context, tx db.Executor, sc foundation.Scope, id string, locked bool) (Delivery, error) {
	var d Delivery
	var raw []byte
	q := `SELECT body FROM affiliate_wallet_deliveries WHERE tenant_id=? AND app_id=? AND delivery_id=?`
	if locked {
		q += " FOR UPDATE"
	}
	e := tx.QueryRowContext(ctx, q, sc.Tenant, sc.App, id).Scan(&raw)
	if e != nil {
		return d, e
	}
	e = json.Unmarshal(raw, &d)
	return d, e
}

// Funds 复用原解锁/冲正规则，仅把领取从同步内部入账改成预留和派发意图。
type Funds struct {
	flowrepo.Funds
	ValidateScope func(foundation.Scope) error
	Binding       func(foundation.Scope) (string, string, error)
}

func (Funds) DeferredClaim() bool { return true }
func (f Funds) Claim(ctx context.Context, tx db.Executor, sc foundation.Scope, r flow.Reward, batch string, now time.Time) error {
	// 先校验独立钱包能力，不能将不支持的币种或零金额占成永远未知的派发。
	amount, ok := new(big.Int).SetString(r.Amount, 10)
	if !ok || amount.Sign() <= 0 || amount.String() != r.Amount || len(r.Amount) > 38 || !walletCurrency.MatchString(r.Currency) || len(r.UserID) > 100 || !foundation.ValidID(r.UserID) {
		return foundation.ErrValidation
	}
	if f.ValidateScope != nil {
		if e := f.ValidateScope(sc); e != nil {
			return e
		}
	}
	if f.Validate != nil {
		if e := f.Validate(ctx, tx, sc, r, now); e != nil {
			return e
		}
	}
	walletSite, walletClient := "", ""
	if f.Binding != nil {
		var e error
		walletSite, walletClient, e = f.Binding(sc)
		if e != nil {
			return e
		}
	}
	l := ledger.Repository{}
	if e := l.CloseHold(ctx, tx, sc, r.EntitlementID, owner(r.ID), now); e != nil {
		return e
	}
	// 只有已明确失败的旧原单才能重新领取；先解除本模块自己的失败保留，不解除其他风控。
	rows, e := tx.QueryContext(ctx, `SELECT delivery_id FROM affiliate_wallet_deliveries WHERE tenant_id=? AND app_id=? AND reward_id=? AND status='FAILED_FINAL' ORDER BY delivery_id`, sc.Tenant, sc.App, r.ID)
	if e != nil {
		return e
	}
	var failed []string
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return e
		}
		failed = append(failed, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, id := range failed {
		if e = l.CloseHold(ctx, tx, sc, r.EntitlementID, failureHold(id), now); e != nil {
			return e
		}
	}
	ent, e := l.Get(ctx, tx, sc, r.EntitlementID, true)
	if e != nil {
		return e
	}
	if ent.Frozen || ent.Debt != "0" || ent.Pending != r.Amount || ent.Available != "0" || ent.Reserved != "0" || ent.Paid != "0" {
		return errors.New("FUNDS_FROZEN")
	}
	id := flow.ID("affiliate-wallet", r.ID, batch)
	for _, pair := range [][2]string{{"pending", "available"}, {"available", "reserved"}} {
		if e = l.Transfer(ctx, tx, sc, r.EntitlementID, pair[0], pair[1], r.Amount, flow.ID(id, pair[0], pair[1]), now); e != nil {
			return e
		}
	}
	// 收款账号来自已认证会员的奖励归属，不接受浏览器传入账号、站点或钱包地址。
	d := Delivery{WalletSite: walletSite, WalletClient: walletClient, ID: id, Reward: r.ID, Claim: batch, User: r.UserID, Entitlement: r.EntitlementID, Currency: r.Currency, Amount: r.Amount, Status: "PENDING", RequestID: foundation.RequestFrom(ctx).RequestID, Updated: time.Now().UTC()}
	raw, _ := json.Marshal(d)
	_, e = tx.ExecContext(ctx, `INSERT INTO affiliate_wallet_deliveries VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, sc.Tenant, sc.App, id, r.ID, batch, r.UserID, d.Status, d.Updated, d.Updated, "", raw, d.Updated, d.Updated)
	return e
}

type Service struct {
	UOW      db.UnitOfWork
	Query    db.Executor
	Provider paymentports.Provider
	Now      func() time.Time
	Binding  func(foundation.Scope) (string, string, error)
}

// Owns 仅在已验签的固定作用域内查原单；归属存在时业务校验失败不得回退到旧派发。
func (s Service) Owns(ctx context.Context, sc foundation.Scope, id string) (bool, error) {
	var count int
	err := s.Query.QueryRowContext(ctx, `SELECT COUNT(*) FROM affiliate_wallet_deliveries WHERE tenant_id=? AND app_id=? AND delivery_id=?`, sc.Tenant, sc.App, id).Scan(&count)
	return count > 0, err
}

// 固定领取受理时的钱包站点与客户端，重启或配置变化不能把旧原单发送到另一钱包作用域。
func (s Service) checkBinding(sc foundation.Scope, d Delivery) error {
	if s.Binding == nil {
		return nil
	}
	site, client, e := s.Binding(sc)
	if e != nil {
		return e
	}
	if site != d.WalletSite || client != d.WalletClient {
		return errors.New("WALLET_BINDING_CHANGED")
	}
	return nil
}

func (s Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC().Truncate(time.Microsecond)
	}
	return time.Now().UTC().Truncate(time.Microsecond)
}
func (s Service) List(ctx context.Context, sc foundation.Scope, user string, limit int, after ...string) ([]Delivery, error) {
	if limit < 1 || limit > 101 {
		return nil, foundation.ErrValidation
	}
	q := `SELECT body FROM affiliate_wallet_deliveries WHERE tenant_id=? AND app_id=?`
	args := []any{sc.Tenant, sc.App}
	if user != "" {
		q += " AND user_id=?"
		args = append(args, user)
	}
	if len(after) > 0 && after[0] != "" {
		q += " AND delivery_id>?"
		args = append(args, after[0])
	}
	q += " ORDER BY delivery_id ASC LIMIT ?"
	args = append(args, limit)
	rows, e := s.Query.QueryContext(ctx, q, args...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Delivery{}
	for rows.Next() {
		var raw []byte
		var d Delivery
		if e = rows.Scan(&raw); e != nil {
			return nil, e
		}
		if e = json.Unmarshal(raw, &d); e != nil {
			return nil, e
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
func (s Service) Recover(ctx context.Context, sc foundation.Scope) error {
	now := s.now()
	rows, e := s.Query.QueryContext(ctx, `SELECT delivery_id FROM affiliate_wallet_deliveries WHERE tenant_id=? AND app_id=? AND status IN ('PENDING','UNKNOWN') AND next_at<=? AND lease_until<=? ORDER BY created_at,delivery_id LIMIT 20`, sc.Tenant, sc.App, now, now)
	if e != nil {
		return e
	}
	var ids []string
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return e
		}
		ids = append(ids, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, id := range ids {
		if e = s.deliver(ctx, sc, id); e != nil {
			return e
		}
	}
	return nil
}
func (s Service) deliver(ctx context.Context, sc foundation.Scope, id string) error {
	var d Delivery
	var submit, acquired bool
	var lease string
	e := s.UOW.Within(ctx, func(tx db.Executor) error {
		if e := (flowrepo.Repository{}).Lock(ctx, tx, sc); e != nil {
			return e
		}
		var raw []byte
		var until, next time.Time
		e := tx.QueryRowContext(ctx, `SELECT body,lease_until,next_at FROM affiliate_wallet_deliveries WHERE tenant_id=? AND app_id=? AND delivery_id=? FOR UPDATE`, sc.Tenant, sc.App, id).Scan(&raw, &until, &next)
		if e != nil {
			return e
		}
		if e = json.Unmarshal(raw, &d); e != nil {
			return e
		}
		if e = s.checkBinding(sc, d); e != nil {
			return e
		}
		now := s.now()
		if (d.Status != "PENDING" && d.Status != "UNKNOWN") || until.After(now) || next.After(now) {
			return nil
		}
		submit = d.Status == "PENDING"
		var nonce [32]byte
		if _, e = rand.Read(nonce[:]); e != nil {
			return e
		}
		lease = hex.EncodeToString(nonce[:])
		acquired = true
		// 在HTTP之前持久标记未知；宕机恢复只能查原单，不能当作尚未投递重新发款。
		d.Status = "UNKNOWN"
		d.Updated = now
		if e = save(ctx, tx, sc, d); e != nil {
			return e
		}
		_, e = tx.ExecContext(ctx, `UPDATE affiliate_wallet_deliveries SET lease_until=?,lease_token=? WHERE tenant_id=? AND app_id=? AND delivery_id=?`, now.Add(10*time.Second), lease, sc.Tenant, sc.App, id)
		return e
	})
	if e != nil || !acquired {
		return e
	}
	var outcome payment.Outcome
	if submit {
		outcome, e = s.Provider.Submit(ctx, d.payment(sc))
	} else {
		outcome, e = s.Provider.Query(ctx, d.payment(sc))
	}
	if e == nil && payment.Terminal(outcome.Status) {
		return s.Apply(ctx, sc, outcome)
	}
	// 超时/未知只释放本次租约；已由并发回执确认的原单不能被错误回包覆盖。
	return s.UOW.Within(ctx, func(tx db.Executor) error {
		if e := (flowrepo.Repository{}).Lock(ctx, tx, sc); e != nil {
			return e
		}
		current, e := read(ctx, tx, sc, id, true)
		if e != nil {
			return e
		}
		if current.Status != "UNKNOWN" {
			return nil
		}
		current.Error = "WALLET_RESULT_UNKNOWN"
		current.Updated = s.now()
		raw, _ := json.Marshal(current)
		_, e = tx.ExecContext(ctx, `UPDATE affiliate_wallet_deliveries SET body=?,next_at=?,lease_until=?,lease_token='' WHERE tenant_id=? AND app_id=? AND delivery_id=? AND lease_token=?`, raw, current.Updated.Add(time.Second), current.Updated, sc.Tenant, sc.App, id, lease)
		return e
	})
}

// Apply 在同一站点锁及事务内确认终态、划转预留并更新奖励；回执与查询并发也只能记账一次。
func (s Service) Apply(ctx context.Context, sc foundation.Scope, out payment.Outcome) error {
	if !payment.Terminal(out.Status) {
		return foundation.ErrValidation
	}
	return s.UOW.Within(ctx, func(tx db.Executor) error {
		if e := (flowrepo.Repository{}).Lock(ctx, tx, sc); e != nil {
			return e
		}
		d, e := read(ctx, tx, sc, out.MerchantID, true)
		if e != nil {
			return e
		}
		if e = s.checkBinding(sc, d); e != nil {
			return e
		}
		if d.Amount != out.Amount || d.Currency != out.Currency {
			return foundation.ErrConflict
		}
		if payment.Terminal(d.Status) {
			if d.Status != out.Status || (d.Receipt != nil && out.Receipt != nil && *d.Receipt != *out.Receipt) {
				return foundation.ErrConflict
			}
			// 查询已确认后到达的首个回执仍保留关联编号，但绝不再次划转资金。
			if d.CallbackID == "" && out.CallbackID != "" {
				d.CallbackID = out.CallbackID
				d.Updated = s.now()
				if e = save(ctx, tx, sc, d); e != nil {
					return e
				}
				return audit.Append(ctx, tx, audit.Entry{Tenant: sc.Tenant, App: sc.App, ID: flow.ID(d.ID, "receipt", out.CallbackID), Subject: "system-affiliate-wallet", RequestID: d.RequestID, Type: "OPERATION", Action: "affiliate.wallet.receipt", Resource: d.ID, Summary: map[string]string{"claim_id": d.Claim, "reward_id": d.Reward, "callback_id": out.CallbackID}, At: d.Updated})
			}
			return nil
		}
		r, e := (flowrepo.Repository{}).Reward(ctx, tx, sc, d.Reward)
		if e != nil {
			return e
		}
		if r.Status != "CLAIMING" || r.ClaimID != d.Claim || r.UserID != d.User || r.EntitlementID != d.Entitlement {
			return foundation.ErrConflict
		}
		now := s.now()
		l := ledger.Repository{}
		if out.Status == "SUCCEEDED" {
			if e = l.Transfer(ctx, tx, sc, d.Entitlement, "reserved", "paid", d.Amount, flow.ID(d.ID, "confirmed"), now); e != nil {
				return e
			}
			r.Status = "CLAIMED"
			r.ClaimedAt = &now
			r.WalletReceipt = out.Receipt
		} else {
			// 只有钱包明确最终失败才退回待领；超时不能释放预留，原领取请求始终指向旧原单。
			if e = l.Transfer(ctx, tx, sc, d.Entitlement, "reserved", "available", d.Amount, flow.ID(d.ID, "failed"), now); e != nil {
				return e
			}
			if e = l.Requeue(ctx, tx, sc, d.Entitlement, flow.ID(d.ID, "requeue"), now); e != nil {
				return e
			}
			if e = l.Hold(ctx, tx, sc, d.Entitlement, failureHold(d.ID), "AFFILIATE_MEMBER_CLAIM", now); e != nil {
				return e
			}
			r.Status = "CLAIMABLE"
			r.ClaimID = ""
			r.ClaimedAt = nil
			r.ClaimIP = ""
			r.WalletDelivery = ""
			r.WalletReceipt = nil
		}
		r.Revision++
		r.UpdatedAt = now
		if e = (flowrepo.Repository{}).SaveReward(ctx, tx, sc, r); e != nil {
			return e
		}
		d.Status = out.Status
		d.CallbackID = out.CallbackID
		d.Receipt = out.Receipt
		d.Error = ""
		d.Updated = now
		if e = save(ctx, tx, sc, d); e != nil {
			return e
		}
		if e = audit.Append(ctx, tx, audit.Entry{Tenant: sc.Tenant, App: sc.App, ID: flow.ID(d.ID, out.Status), Subject: "system-affiliate-wallet", RequestID: d.RequestID, Type: "OPERATION", Action: "affiliate.wallet.confirm", Resource: d.ID, Summary: map[string]string{"claim_id": d.Claim, "reward_id": d.Reward, "status": out.Status}, At: now}); e != nil {
			return e
		}
		slog.InfoContext(ctx, "代理领取独立钱包结果已确认", "request_id", d.RequestID, "claim_id", d.Claim, "reward_id", d.Reward, "delivery_id", d.ID, "status", out.Status)
		return nil
	})
}
