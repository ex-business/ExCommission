package adapters

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	model "github.com/ex-business/ExCommission/internal/affiliateflow/domain"
	ledger "github.com/ex-business/ExCommission/internal/ledger/adapters"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"math/big"
	"time"
)

// RecordReversal 必须在站点编排锁下调用，原单身份与累计上限在同一事务校验；不覆盖原事实。
func (Repository) RecordReversal(ctx context.Context, tx db.Executor, s foundation.Scope, r model.Reversal, now time.Time) (bool, error) {
	var hash string
	e := tx.QueryRowContext(ctx, `SELECT facts_hash FROM affiliate_reversal_events WHERE tenant_id=? AND app_id=? AND source_id=? AND business_id=?`, s.Tenant, s.App, r.Source, r.BusinessID).Scan(&hash)
	if e == nil {
		if hash != r.Hash() {
			return false, foundation.ErrConflict
		}
		return false, nil
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return false, e
	}
	e = tx.QueryRowContext(ctx, `SELECT facts_hash FROM affiliate_reversal_events WHERE tenant_id=? AND app_id=? AND source_id=? AND event_id=?`, s.Tenant, s.App, r.Source, r.EventID).Scan(&hash)
	if e == nil {
		return false, foundation.ErrConflict
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return false, e
	}
	var body []byte
	e = tx.QueryRowContext(ctx, `SELECT body FROM affiliate_business_facts WHERE tenant_id=? AND app_id=? AND source_id=? AND kind=? AND business_id=? UNION ALL SELECT body FROM affiliate_late_business_facts WHERE tenant_id=? AND app_id=? AND source_id=? AND kind=? AND business_id=?`, s.Tenant, s.App, r.Source, r.OriginalKind(), r.OriginalBusiness, s.Tenant, s.App, r.Source, r.OriginalKind(), r.OriginalBusiness).Scan(&body)
	if errors.Is(e, sql.ErrNoRows) {
		return false, errors.New("UNKNOWN_FACT")
	}
	if e != nil {
		return false, e
	}
	var original model.Event
	if json.Unmarshal(body, &original) != nil {
		return false, foundation.ErrValidation
	}
	if original.EventID != r.OriginalID || original.ExternalUser != r.ExternalUser || original.Currency != r.Currency || r.At.Before(original.At) {
		return false, foundation.ErrConflict
	}
	amount, valid := "0", "0"
	e = tx.QueryRowContext(ctx, `SELECT CAST(amount AS CHAR),CAST(valid_bet AS CHAR) FROM affiliate_reversal_totals WHERE tenant_id=? AND app_id=? AND source_id=? AND kind=? AND business_id=?`, s.Tenant, s.App, r.Source, r.OriginalKind(), r.OriginalBusiness).Scan(&amount, &valid)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return false, e
	}
	a, _ := new(big.Int).SetString(amount, 10)
	b, _ := new(big.Int).SetString(valid, 10)
	da, _ := new(big.Int).SetString(r.Amount, 10)
	dv, _ := new(big.Int).SetString(r.ValidBet, 10)
	a.Add(a, da)
	b.Add(b, dv)
	oa, _ := new(big.Int).SetString(original.Amount, 10)
	ov, _ := new(big.Int).SetString(original.ValidBet, 10)
	if a.Cmp(oa) > 0 || b.Cmp(ov) > 0 || new(big.Int).Sub(ov, b).Cmp(new(big.Int).Sub(oa, a)) > 0 {
		return false, errors.New("REFUND_EXCEEDS_ORIGINAL")
	}
	raw, _ := json.Marshal(r)
	if _, e = tx.ExecContext(ctx, `INSERT INTO affiliate_reversal_events VALUES(?,?,?,?,?,?,?,?)`, s.Tenant, s.App, r.Source, r.BusinessID, r.EventID, r.Hash(), raw, now); e != nil {
		return false, e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO affiliate_reversal_totals VALUES(?,?,?,?,?,?,?) ON DUPLICATE KEY UPDATE amount=VALUES(amount),valid_bet=VALUES(valid_bet)`, s.Tenant, s.App, r.Source, r.OriginalKind(), r.OriginalBusiness, a.String(), b.String())
	return e == nil, e
}
func (Repository) ReversalDays(ctx context.Context, tx db.Executor, s foundation.Scope) ([]model.Daily, error) {
	rows, e := tx.QueryContext(ctx, `SELECT body FROM affiliate_daily_runs WHERE tenant_id=? AND app_id=? ORDER BY business_day`, s.Tenant, s.App)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []model.Daily{}
	for rows.Next() {
		var raw []byte
		var d model.Daily
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
func (Repository) ReversalBaseline(ctx context.Context, tx db.Executor, s foundation.Scope, r model.Reward) (model.Reward, error) {
	raw, e := json.Marshal(r)
	if e != nil {
		return r, e
	}
	if _, e = tx.ExecContext(ctx, `INSERT IGNORE INTO affiliate_reversal_baselines VALUES(?,?,?,?)`, s.Tenant, s.App, r.ID, raw); e != nil {
		return r, e
	}
	e = tx.QueryRowContext(ctx, `SELECT body FROM affiliate_reversal_baselines WHERE tenant_id=? AND app_id=? AND reward_id=?`, s.Tenant, s.App, r.ID).Scan(&raw)
	if e == nil {
		e = json.Unmarshal(raw, &r)
	}
	return r, e
}
func (Repository) SaveReversalAdjustment(ctx context.Context, tx db.Executor, s foundation.Scope, a model.ReversalAdjustment) error {
	_, e := tx.ExecContext(ctx, `INSERT INTO affiliate_reversal_adjustments VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, s.Tenant, s.App, a.ID, a.ReversalID, a.RewardID, a.UserID, a.Currency, a.Before, a.After, a.Debt, a.Status, a.At)
	return e
}
func (Repository) ReversalRecords(ctx context.Context, tx db.Executor, s foundation.Scope) ([]model.ReversalAdjustment, error) {
	rows, e := tx.QueryContext(ctx, `SELECT adjustment_id,reversal_id,reward_id,user_id,currency,CAST(before_target AS CHAR),CAST(after_target AS CHAR),CAST(debt_added AS CHAR),status,created_at FROM affiliate_reversal_adjustments WHERE tenant_id=? AND app_id=? ORDER BY created_at,adjustment_id LIMIT 1000`, s.Tenant, s.App)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []model.ReversalAdjustment{}
	for rows.Next() {
		var a model.ReversalAdjustment
		if e = rows.Scan(&a.ID, &a.ReversalID, &a.RewardID, &a.UserID, &a.Currency, &a.Before, &a.After, &a.Debt, &a.Status, &a.At); e != nil {
			return nil, e
		}
		out = append(out, a)
	}
	if e = rows.Err(); e != nil {
		rows.Close()
		return nil, e
	}
	rows.Close()
	// 查询时关联不可变来源单，后台可以直接核对退款编号、原事件与原业务单，而不是只展示内部哈希。
	events, e := tx.QueryContext(ctx, `SELECT body FROM affiliate_reversal_events WHERE tenant_id=? AND app_id=?`, s.Tenant, s.App)
	if e != nil {
		return nil, e
	}
	defer events.Close()
	linked := map[string]model.Reversal{}
	for events.Next() {
		var raw []byte
		var r model.Reversal
		if e = events.Scan(&raw); e != nil {
			return nil, e
		}
		if e = json.Unmarshal(raw, &r); e != nil {
			return nil, e
		}
		linked[model.ID(r.Source, r.BusinessID)] = r
	}
	if e = events.Err(); e != nil {
		return nil, e
	}
	for i := range out {
		r := linked[out[i].ReversalID]
		out[i].Source = r.Source
		out[i].Business = r.BusinessID
		out[i].OriginalID = r.OriginalID
		out[i].OriginalBusiness = r.OriginalBusiness
		out[i].Kind = r.Kind
	}
	return out, nil
}

// Adjust 已领金额保持paid，差额进入原追偿账本；任何reserved金额都会阻止整个冲正事务，等待原付款确认。
func (Funds) Adjust(ctx context.Context, tx db.Executor, s foundation.Scope, r model.Reward, target, reference string, now time.Time) (string, string, error) {
	repo := ledger.Repository{}
	old, e := repo.Get(ctx, tx, s, r.EntitlementID, true)
	if e != nil {
		return "", "", e
	}
	before := old.Target
	if old.Reserved != "0" {
		return "", "", errors.New("PAYMENT_RESULT_UNKNOWN")
	}
	a, _ := new(big.Int).SetString(target, 10)
	b, _ := new(big.Int).SetString(before, 10)
	if a.Cmp(b) > 0 {
		return before, "0", nil
	}
	if e = repo.Reduce(ctx, tx, s, r.EntitlementID, target, reference, now); e != nil {
		return "", "", e
	}
	after, e := repo.Get(ctx, tx, s, r.EntitlementID, true)
	if e != nil {
		return "", "", e
	}
	n, _ := new(big.Int).SetString(after.Debt, 10)
	d, _ := new(big.Int).SetString(old.Debt, 10)
	return before, n.Sub(n, d).String(), nil
}

// ReversalEvents 包含已验签但未产生新差额的撤销，便于核对“收到事件”与“资金调整”的区别。
func (Repository) ReversalEvents(ctx context.Context, tx db.Executor, s foundation.Scope) ([]model.Reversal, error) {
	rows, e := tx.QueryContext(ctx, `SELECT body FROM affiliate_reversal_events WHERE tenant_id=? AND app_id=? ORDER BY received_at DESC,business_id LIMIT 1000`, s.Tenant, s.App)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []model.Reversal{}
	for rows.Next() {
		var raw []byte
		var r model.Reversal
		if e = rows.Scan(&raw); e != nil {
			return nil, e
		}
		if e = json.Unmarshal(raw, &r); e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
