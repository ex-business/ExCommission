package application

import (
	"context"
	"errors"
	affiliate "github.com/ex-business/ExCommission/internal/affiliate/domain"
	model "github.com/ex-business/ExCommission/internal/affiliateflow/domain"
	principal "github.com/ex-business/ExCommission/internal/identity/domain"
	response "github.com/ex-business/ExCommission/internal/platform/application"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"log/slog"
	"math/big"
	"time"
)

type Review struct {
	Decision string  `json:"decision"`
	Revision uint64  `json:"expected_revision"`
	Amount   *string `json:"amount_minor,omitempty"`
	Reason   string  `json:"reason"`
}

func (s Service) Review(ctx context.Context, p principal.Principal, id string, in Review, key, rid string) (foundation.CommandResult, error) {
	if !foundation.ValidID(id) || !foundation.ValidID(in.Reason) || (in.Decision != "APPROVE" && in.Decision != "CANCEL") {
		return foundation.CommandResult{}, foundation.ErrValidation
	}
	if e := s.Authorize(ctx, s.Query, p, "affiliate:review"); e != nil {
		return foundation.CommandResult{}, e
	}
	sc := scope(p)
	ctx = foundation.WithRequest(ctx, rid, p.Subject)
	out, e := s.Commands.Execute(ctx, sc, "affiliate-commission-review", key, struct {
		ID     string
		Review Review
	}{id, in}, func(tx db.Executor) (foundation.CommandResult, error) {
		if e := s.Authorize(ctx, tx, p, "affiliate:review"); e != nil {
			return foundation.CommandResult{}, e
		}
		if e := s.Repo.Lock(ctx, tx, sc); e != nil {
			return foundation.CommandResult{}, e
		}
		r, e := s.Repo.Reward(ctx, tx, sc, id)
		if e != nil {
			return foundation.CommandResult{}, e
		}
		if r.Kind != model.Commission {
			return foundation.CommandResult{}, foundation.ErrValidation
		}
		if r.Revision != in.Revision {
			return foundation.CommandResult{}, errors.New("REVISION_CONFLICT")
		}
		if r.Status == "CLAIMED" || r.Status == "CANCELLED" {
			return foundation.CommandResult{}, errors.New("STATE_CONFLICT")
		}
		now := s.Now().UTC().Truncate(time.Microsecond)
		if in.Decision == "CANCEL" {
			if in.Amount != nil {
				return foundation.CommandResult{}, foundation.ErrValidation
			}
			if e = s.Funds.Cancel(ctx, tx, sc, r, now); e != nil {
				return foundation.CommandResult{}, e
			}
			r.Status = "CANCELLED"
		} else {
			if s.CanReward != nil {
				if e = s.CanReward(ctx, tx, sc); e != nil {
					return foundation.CommandResult{}, e
				}
			}
			if r.Status != "PENDING_REVIEW" {
				return foundation.CommandResult{}, errors.New("STATE_CONFLICT")
			}
			if in.Amount != nil {
				n, e := affiliate.Money(*in.Amount, true)
				if e != nil {
					return foundation.CommandResult{}, e
				}
				scale := 0
				if r.ProgramID != "" {
					// 独立方案审核使用原权益快照，不能按兼容方案的当日币种精度改金额。
					if r.ProgramConfig == nil {
						return foundation.CommandResult{}, foundation.ErrConflict
					}
					scale = r.ProgramConfig.Config.CurrencyScale
				} else {
					d, err := s.DailyRepo.Daily(ctx, tx, sc, r.Day)
					if err != nil {
						return foundation.CommandResult{}, err
					}
					scale = d.Config.Config.CurrencyScale
				}
				minimum := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(scale)), nil)
				if n.Cmp(minimum) < 0 {
					return foundation.CommandResult{}, foundation.ErrValidation
				}
				r.Amount = *in.Amount
			}
			r.EntitlementID, e = s.Funds.Unlock(ctx, tx, sc, r, now)
			if e != nil {
				return foundation.CommandResult{}, e
			}
			r.Status = "CLAIMABLE"
		}
		r.UpdatedAt = now
		r.Reviewer = p.Subject
		r.ReviewedAt = &now
		r.Reason = in.Reason
		r.Revision++
		if e = s.Repo.SaveReward(ctx, tx, sc, r); e != nil {
			return foundation.CommandResult{}, e
		}
		return response.Result(200, id, rid, r)
	})
	if e == nil && out.Status == 200 && !out.Replayed {
		slog.InfoContext(ctx, "佣金业务审核已完成", "request_id", rid, "reward_id", id, "reviewer_id", p.Subject, "decision", in.Decision, "result", "SUCCEEDED")
	}
	return out, e
}
