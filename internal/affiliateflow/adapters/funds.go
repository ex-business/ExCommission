package adapters

import (
	"context"
	"errors"
	model "github.com/ex-business/ExCommission/internal/affiliateflow/domain"
	ledger "github.com/ex-business/ExCommission/internal/ledger/adapters"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	walletmodel "github.com/ex-business/ExCommission/internal/wallet/domain"
	wallet "github.com/ex-business/ExCommission/internal/wallet/ports"
	"time"
)

// Funds 仅在相同命令事务内调用账簿与钱包；解锁权益加模块保留标志，旧结算扫描不能二次派发。
type Funds struct {
	Wallet   wallet.Repository
	Validate func(context.Context, db.Executor, foundation.Scope, model.Reward, time.Time) error
}

func hold(id string) string { return model.ID("affiliate-claim-owner", id) }
func (f Funds) Unlock(ctx context.Context, tx db.Executor, s foundation.Scope, r model.Reward, now time.Time) (string, error) {
	if f.Validate != nil {
		if e := f.Validate(ctx, tx, s, r, now); e != nil {
			return "", e
		}
	}
	repo := ledger.Repository{}
	id, e := repo.CreditAffiliate(ctx, tx, s, r.ID, r.AgentID, r.Kind, r.Currency, r.Amount, r.ConfigRevision, r.UnlockedAt, now)
	if e != nil {
		return "", e
	}
	e = repo.Hold(ctx, tx, s, id, hold(r.ID), "AFFILIATE_MEMBER_CLAIM", now)
	return id, e
}
func (f Funds) Claim(ctx context.Context, tx db.Executor, s foundation.Scope, r model.Reward, batch string, now time.Time) error {
	if f.Validate != nil {
		if e := f.Validate(ctx, tx, s, r, now); e != nil {
			return e
		}
	}
	repo := ledger.Repository{}
	if e := repo.CloseHold(ctx, tx, s, r.EntitlementID, hold(r.ID), now); e != nil {
		return e
	}
	e, err := repo.Get(ctx, tx, s, r.EntitlementID, true)
	if err != nil {
		return err
	}
	// 移除本模块保留后仍有其他风控限制则拒绝；事务回滚会恢复保留，不绕过原冻结或追偿。
	if e.Frozen || e.Debt != "0" || e.Pending != r.Amount || e.Available != "0" || e.Reserved != "0" || e.Paid != "0" {
		return errors.New("FUNDS_FROZEN")
	}
	for _, pair := range [][2]string{{"pending", "available"}, {"available", "reserved"}, {"reserved", "paid"}} {
		ref := model.ID("affiliate-claim", r.ID, pair[0], pair[1])
		if err = repo.Transfer(ctx, tx, s, r.EntitlementID, pair[0], pair[1], r.Amount, ref, now); err != nil {
			return err
		}
	}
	// 一次领取多笔奖励仍逐笔写交易与账变；业务引用为权益ID，跨幂等键也不会重复入账。
	return f.Wallet.Credit(ctx, tx, s, walletmodel.Credit{UserID: r.UserID, Currency: r.Currency, Amount: r.Amount, Reference: "affiliate-" + r.ID, PaymentID: batch, StatementID: r.ID}, now)
}
func (Funds) Cancel(ctx context.Context, tx db.Executor, s foundation.Scope, r model.Reward, now time.Time) error {
	if r.EntitlementID == "" {
		return nil
	}
	return (ledger.Repository{}).Reduce(ctx, tx, s, r.EntitlementID, "0", model.ID("affiliate-cancel", r.ID), now)
}
