package application

import (
	"context"
	"encoding/json"
	"errors"
	affiliate "github.com/ex-business/ExCommission/internal/affiliate/domain"
	model "github.com/ex-business/ExCommission/internal/affiliateflow/domain"
	"github.com/ex-business/ExCommission/internal/affiliateflow/ports"
	principal "github.com/ex-business/ExCommission/internal/identity/domain"
	response "github.com/ex-business/ExCommission/internal/platform/application"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"log/slog"
	"math/big"
	"time"
)

// 独立的可重试哨兵不进入公共命令的确定失败缓存，原幂等键可在付款确认后继续处理。
var ErrReversalPaymentPending = errors.New("AFFILIATE_REVERSAL_WAITING_PAYMENT")
var ErrReversalOriginalPending = errors.New("AFFILIATE_REVERSAL_WAITING_ORIGINAL")

func (s Service) ReceiveReversal(ctx context.Context, p principal.Principal, raw []byte, signature, key, rid string) (foundation.CommandResult, error) {
	var selector struct {
		Source string `json:"source_id"`
	}
	if json.Unmarshal(raw, &selector) != nil {
		return foundation.CommandResult{}, foundation.ErrValidation
	}
	candidate := s.Sources[selector.Source]
	if candidate == nil && s.ResolveSource != nil {
		var e error
		candidate, e = s.ResolveSource(ctx, p, selector.Source)
		if e != nil {
			return foundation.CommandResult{}, e
		}
	}
	source, ok := candidate.(ports.ReversalSource)
	if !ok {
		return foundation.CommandResult{}, errors.New("UNSUPPORTED_CAPABILITY")
	}
	r, e := source.VerifyReversal(ctx, scope(p), raw, signature)
	if e != nil {
		return foundation.CommandResult{}, e
	}
	now := s.Now().UTC().Truncate(time.Microsecond)
	if e = r.Validate(now); e != nil {
		return foundation.CommandResult{}, e
	}
	if e = s.Authorize(ctx, s.Query, p, "affiliate:ingest"); e != nil {
		return foundation.CommandResult{}, e
	}
	repo, ok := s.Repo.(ports.Reversals)
	if !ok {
		return foundation.CommandResult{}, errors.New("UNSUPPORTED_CAPABILITY")
	}
	ctx = foundation.WithRequest(ctx, rid, p.Subject)
	sc := scope(p)
	out, e := s.Commands.Execute(ctx, sc, "affiliate-reversal", key, r, func(tx db.Executor) (foundation.CommandResult, error) {
		if e := s.Authorize(ctx, tx, p, "affiliate:ingest"); e != nil {
			return foundation.CommandResult{}, e
		}
		if e := s.Repo.Lock(ctx, tx, sc); e != nil {
			return foundation.CommandResult{}, e
		}
		accepted, e := repo.RecordReversal(ctx, tx, sc, r, now)
		if e != nil {
			if e.Error() == "UNKNOWN_FACT" {
				return foundation.CommandResult{}, ErrReversalOriginalPending
			}
			return foundation.CommandResult{}, e
		}
		if accepted {
			if e = s.restateReversal(ctx, tx, sc, r, repo, now); e != nil {
				if e.Error() == "PAYMENT_RESULT_UNKNOWN" {
					return foundation.CommandResult{}, ErrReversalPaymentPending
				}
				return foundation.CommandResult{}, e
			}
		}
		if accepted && s.ProgramReversal != nil {
			if e = s.ProgramReversal(ctx, tx, sc, r, now); e != nil {
				if e.Error() == "PAYMENT_RESULT_UNKNOWN" {
					return foundation.CommandResult{}, ErrReversalPaymentPending
				}
				return foundation.CommandResult{}, e
			}
		}
		return response.Result(200, r.EventID, rid, map[string]any{"event_id": r.EventID, "business_id": r.BusinessID, "accepted": accepted, "status": "APPLIED"})
	})
	if e == nil && out.Status == 200 && !out.Replayed {
		slog.InfoContext(ctx, "代理退款撤单已完成冲正并保留追偿依据", "request_id", rid, "source_id", r.Source, "original_event_id", r.OriginalID, "reversal_id", r.BusinessID, "tenant_id", sc.Tenant, "app_id", sc.App)
	}
	return out, e
}

// restateReversal 所有净额、资格、奖励与追偿在同一事务提交；付款结果未知时全部回滚，原事件由来源队列保留重试。
func (s Service) restateReversal(ctx context.Context, tx db.Executor, sc foundation.Scope, r model.Reversal, repo ports.Reversals, now time.Time) error {
	facts, e := s.Repo.Facts(ctx, tx, sc, now.Add(time.Microsecond))
	if e != nil {
		return e
	}
	days, e := repo.ReversalDays(ctx, tx, sc)
	if e != nil {
		return e
	}
	targets := map[string]string{}
	for _, d := range days {
		date, e := time.ParseInLocation("2006-01-02", d.Day, d.Config.Config.Location())
		if e != nil {
			return e
		}
		start, end := d.Config.Config.Day(date)
		historical := []affiliate.Fact{}
		for _, f := range facts {
			if f.At.Before(end) {
				historical = append(historical, f)
			}
		}
		regs, e := s.Members.List(ctx, tx, sc, end.Add(-time.Microsecond))
		if e != nil {
			return e
		}
		registrations := []affiliate.Registration{}
		for _, m := range regs {
			registrations = append(registrations, affiliate.Registration{UserID: m.UserID, ParentID: m.ParentUser, At: m.RegisteredAt})
		}
		if s.Relations != nil {
			registrations, e = s.Relations.Close(ctx, tx, sc, end.Add(-time.Microsecond))
			if e != nil {
				return e
			}
		}
		var ownership []affiliate.Attribution
		if s.Relations != nil {
			if len(d.Config.Config.RiskRules) > 0 {
				start = time.Time{}
			}
			ownership, e = s.attributions(ctx, tx, sc, historical, start, end, d.Config.Config.DepthLimit())
			if e != nil {
				return e
			}
		}
		restated := []model.FrozenCommission{}
		for _, frozen := range d.Results {
			selected := []affiliate.Fact{}
			for _, f := range historical {
				if !f.At.Before(frozen.Since) {
					selected = append(selected, f)
				}
			}
			result, e := affiliate.CalculateRiskDay(d.Config.Config, frozen.Result.UserID, date, registrations, selected, ownership, historical)
			if e != nil {
				return e
			}
			next := frozen
			next.Result = result
			restated = append(restated, next)
			if frozen.Eligible {
				targets[model.ID(model.Commission, result.UserID, d.Day)] = result.Amount
			}
		}
		d.RestatedResults = restated
		if e = s.DailyRepo.SaveDaily(ctx, tx, sc, d, now); e != nil {
			return e
		}
	}
	members, e := s.Members.List(ctx, tx, sc, now)
	if e != nil {
		return e
	}
	for _, member := range members {
		rewards, e := s.Repo.Rewards(ctx, tx, sc, member.UserID)
		if e != nil {
			return e
		}
		for _, reward := range rewards {
			if reward.ProgramID != "" || reward.Status == "CANCELLED" {
				continue
			}
			baseline, e := repo.ReversalBaseline(ctx, tx, sc, reward)
			if e != nil {
				return e
			}
			target := baseline.Amount
			if reward.Kind == model.Commission {
				calculated, ok := targets[reward.ID]
				if !ok {
					continue
				}
				original, ok := new(big.Int).SetString(baseline.Original, 10)
				if !ok || original.Sign() <= 0 {
					return foundation.ErrValidation
				}
				calculation, _ := new(big.Int).SetString(calculated, 10)
				base, _ := new(big.Int).SetString(baseline.Amount, 10)
				// 固定首次冲正前的审核金额作为比例基准，分批退款不重复缩减基数；最小单位统一向下取整。
				target = new(big.Int).Div(new(big.Int).Mul(base, calculation), original).String()
			} else {
				versionReader, ok := s.Configs.(interface {
					Version(context.Context, db.Executor, foundation.Scope, uint64) (affiliate.Version, error)
				})
				if !ok {
					return errors.New("UNSUPPORTED_CAPABILITY")
				}
				version, e := versionReader.Version(ctx, tx, sc, reward.ConfigRevision)
				if e != nil {
					return e
				}
				condition := version.Config.ReferralCondition
				if reward.Kind == model.Invitee {
					condition = version.Config.InviteeCondition
				}
				for _, user := range reward.Members {
					selected := []affiliate.Fact{}
					for _, f := range facts {
						if f.UserID == user && !f.At.Before(reward.Since) {
							selected = append(selected, f)
						}
					}
					q, e := affiliate.Qualify(condition, selected, version.Config, reward.UnlockedAt.Add(time.Microsecond))
					if e != nil {
						return e
					}
					if q.At == nil {
						target = "0"
						break
					}
				}
			}
			before := reward.Amount
			debt := "0"
			status := "ADJUSTED"
			a, _ := new(big.Int).SetString(target, 10)
			b, _ := new(big.Int).SetString(before, 10)
			if reward.EntitlementID != "" {
				adjuster, ok := s.Funds.(ports.ReversalFunds)
				if !ok {
					return errors.New("UNSUPPORTED_CAPABILITY")
				}
				before, debt, e = adjuster.Adjust(ctx, tx, sc, reward, target, model.ID("reversal", r.Source, r.BusinessID, reward.ID), now)
				if e != nil {
					return e
				}
				b, _ = new(big.Int).SetString(before, 10)
			}
			if a.Cmp(b) == 0 {
				continue
			}
			// 正向差额只保留复核依据，来源事件不能直接扩大已经批准的发放金额。
			if a.Cmp(b) > 0 {
				status = "REVIEW_REQUIRED"
			} else {
				if debt != "0" {
					status = "RECOVERY_OPEN"
				}
				if reward.Status != "CLAIMED" {
					if target == "0" {
						reward.Status = "CANCELLED"
					} else {
						reward.Amount = target
					}
				}
				reward.Revision++
				reward.UpdatedAt = now
				reward.Reason = "来源退款撤单冲正：" + r.BusinessID
				if e = s.Repo.SaveReward(ctx, tx, sc, reward); e != nil {
					return e
				}
			}
			adjustment := model.ReversalAdjustment{ID: model.ID(r.Source, r.BusinessID, reward.ID), ReversalID: model.ID(r.Source, r.BusinessID), RewardID: reward.ID, UserID: reward.UserID, Currency: reward.Currency, Before: before, After: target, Debt: debt, Status: status, At: now}
			if e = repo.SaveReversalAdjustment(ctx, tx, sc, adjustment); e != nil {
				return e
			}
		}
		// 刷新资格展示但不额外解锁新奖励；历史会员占用保持，防止退款后重新领取终身奖励。
		version, e := s.Configs.At(ctx, tx, sc, now)
		if e != nil {
			return e
		}
		gate, e := s.Repo.Gate(ctx, tx, sc, member.UserID, member.RegisteredAt)
		if e != nil {
			return e
		}
		updated, e := s.Repo.Rewards(ctx, tx, sc, member.UserID)
		if e != nil {
			return e
		}
		progress, _, e := model.Evaluate(version, member, members, facts, gate, updated, now)
		if e != nil {
			return e
		}
		if e = s.Repo.SaveProgress(ctx, tx, sc, member.UserID, progress, now); e != nil {
			return e
		}
	}
	return nil
}
