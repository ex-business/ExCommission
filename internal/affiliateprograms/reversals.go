package affiliateprograms

import (
	"context"
	"errors"
	affiliate "github.com/ex-business/ExCommission/internal/affiliate/domain"
	flow "github.com/ex-business/ExCommission/internal/affiliateflow/domain"
	"github.com/ex-business/ExCommission/internal/affiliateflow/ports"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"math/big"
	"time"
)

// Reverse 不受当前启动开关影响；退款必须按原奖励冻结版本冲正，停止方案不能成为保留错误权益的理由。
func (s Service) Reverse(ctx context.Context, tx db.Executor, sc foundation.Scope, event flow.Reversal, now time.Time) error {
	repo, ok := s.Flow.Repo.(ports.Reversals)
	if !ok {
		return errors.New("UNSUPPORTED_CAPABILITY")
	}
	funds, ok := s.Flow.Funds.(ports.ReversalFunds)
	if !ok {
		return errors.New("UNSUPPORTED_CAPABILITY")
	}
	days, e := s.Repo.Days(ctx, tx, sc)
	if e != nil {
		return e
	}
	targets := map[string]string{}
	for _, d := range days {
		v := Version{Change: Change{ID: d.ProgramID, Config: d.Daily.Config.Config, Effective: d.Daily.Config.Effective}, Revision: d.Revision}
		date, e := time.ParseInLocation("2006-01-02", d.Daily.Day, v.Config.Location())
		if e != nil {
			return e
		}
		results, e := s.calculate(ctx, tx, sc, v, date, d.Daily.Results)
		if e != nil {
			return e
		}
		d.Daily.RestatedResults = results
		for _, f := range results {
			if f.Eligible {
				targets[flow.ID("program-day", v.ID, stepString(int(v.Revision)), f.Result.UserID, d.Daily.Day)] = f.Result.Amount
			}
		}
		if e = s.Repo.SaveDay(ctx, tx, sc, d); e != nil {
			return e
		}
	}
	members, e := s.Flow.Members.List(ctx, tx, sc, now)
	if e != nil {
		return e
	}
	for _, m := range members {
		rewards, e := s.Flow.Repo.Rewards(ctx, tx, sc, m.UserID)
		if e != nil {
			return e
		}
		for _, r := range rewards {
			if r.ProgramID == "" || r.Status == "CANCELLED" {
				continue
			}
			if r.ProgramConfig == nil {
				return foundation.ErrConflict
			}
			baseline, e := repo.ReversalBaseline(ctx, tx, sc, r)
			if e != nil {
				return e
			}
			target := baseline.Amount
			if r.Kind == flow.Commission {
				calculated, exists := targets[r.ID]
				if !exists {
					continue
				}
				original, _ := new(big.Int).SetString(baseline.Original, 10)
				base, _ := new(big.Int).SetString(baseline.Amount, 10)
				next, _ := new(big.Int).SetString(calculated, 10)
				if original == nil || original.Sign() <= 0 {
					return foundation.ErrConflict
				}
				target = new(big.Int).Div(new(big.Int).Mul(base, next), original).String()
			} else {
				v := Version{Change: Change{ID: r.ProgramID, Config: r.ProgramConfig.Config, Effective: r.ProgramConfig.Effective}, Revision: r.ConfigRevision}
				facts, e := s.Repo.Facts(ctx, tx, sc, v, r.UnlockedAt.Add(time.Microsecond))
				if e != nil {
					return e
				}
				condition := v.Config.ReferralCondition
				if r.Kind == flow.Invitee {
					condition = v.Config.InviteeCondition
				}
				for _, user := range r.Members {
					selected := []affiliate.Fact{}
					for _, f := range facts {
						if f.UserID == user && !f.At.Before(r.Since) {
							selected = append(selected, f)
						}
					}
					q, e := affiliate.Qualify(condition, selected, v.Config, r.UnlockedAt.Add(time.Microsecond))
					if e != nil {
						return e
					}
					if q.At == nil {
						target = "0"
						break
					}
				}
			}
			before, debt := r.Amount, "0"
			if r.EntitlementID != "" {
				before, debt, e = funds.Adjust(ctx, tx, sc, r, target, flow.ID("program-reversal", event.Source, event.BusinessID, r.ID), now)
				if e != nil {
					return e
				}
			}
			a, _ := new(big.Int).SetString(target, 10)
			b, _ := new(big.Int).SetString(before, 10)
			if a.Cmp(b) == 0 {
				continue
			}
			status := "ADJUSTED"
			// 正向差额仅记录复核，已领负向差额保留原钱包流水并写追偿，不自动扣钱包。
			if a.Cmp(b) > 0 {
				status = "REVIEW_REQUIRED"
			} else {
				if debt != "0" {
					status = "RECOVERY_OPEN"
				}
				if r.Status != "CLAIMED" {
					if target == "0" {
						r.Status = "CANCELLED"
					} else {
						r.Amount = target
					}
				}
				r.Revision++
				r.UpdatedAt = now
				r.Reason = "独立方案来源冲正：" + event.BusinessID
				if e = s.Flow.Repo.SaveReward(ctx, tx, sc, r); e != nil {
					return e
				}
			}
			adjustment := flow.ReversalAdjustment{ID: flow.ID(event.Source, event.BusinessID, r.ID), ReversalID: flow.ID(event.Source, event.BusinessID), RewardID: r.ID, UserID: r.UserID, Currency: r.Currency, Before: before, After: target, Debt: debt, Status: status, At: now}
			if e = repo.SaveReversalAdjustment(ctx, tx, sc, adjustment); e != nil {
				return e
			}
		}
	}
	return nil
}
