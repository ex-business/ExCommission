// Package application 串接已发布配置、可信事实、三套资格和两类终身奖励，不调用外部网络发钱。
package application

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	affiliate "github.com/ex-business/ExCommission/internal/affiliate/domain"
	model "github.com/ex-business/ExCommission/internal/affiliateflow/domain"
	"github.com/ex-business/ExCommission/internal/affiliateflow/ports"
	tasks "github.com/ex-business/ExCommission/internal/affiliatetasks"
	principal "github.com/ex-business/ExCommission/internal/identity/domain"
	response "github.com/ex-business/ExCommission/internal/platform/application"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"log/slog"
	"sort"
	"time"
)

type Service struct {
	TaskRun         tasks.Run
	ProgramAccept   func(context.Context, db.Executor, foundation.Scope, model.Event, string, time.Time) error
	ProgramValidate func(context.Context, db.Executor, foundation.Scope, model.Event, time.Time) error
	ProgramRun      func(context.Context, foundation.Scope) error
	ProgramReversal func(context.Context, db.Executor, foundation.Scope, model.Reversal, time.Time) error

	// 可配置来源仅扩展独立适配器，旧sandbox仍按原签名和幂等结构处理。
	WatermarkGuard  func(context.Context, db.Executor, principal.Principal, []byte, string, model.Watermark) error
	ResolveSource   func(context.Context, principal.Principal, string) (ports.Source, error)
	SourceGuard     func(context.Context, db.Executor, principal.Principal, []byte, string, model.Event) error
	SourceReceipt   func(context.Context, db.Executor, foundation.Scope, model.Event, []byte, bool, bool, time.Time) error
	RequiredSources func(context.Context, db.Executor, foundation.Scope, time.Time, time.Time) ([]string, error)

	Relations         ports.Relations
	ClaimContextReady bool
	CanRewardAt       func(context.Context, db.Executor, foundation.Scope, time.Time) error
	CanReward         func(context.Context, db.Executor, foundation.Scope) error
	Commands          db.Commands
	Query             db.Executor
	Repo              ports.Repository
	DailyRepo         ports.DailyRepository
	Members           ports.Members
	Configs           ports.Configurations
	Funds             ports.Funds
	Sources           map[string]ports.Source
	Authorize         ports.Authorize
	StepUp            ports.StepUp
	Now               func() time.Time
}

func scope(p principal.Principal) foundation.Scope {
	return foundation.Scope{Tenant: p.Tenant, App: p.App}
}
func (s Service) refresh(ctx context.Context, tx db.Executor, sc foundation.Scope, v affiliate.Version, regs []model.Member, facts []affiliate.Fact, m model.Member, now time.Time) (model.Progress, error) {
	gate, e := s.Repo.Gate(ctx, tx, sc, m.UserID, m.RegisteredAt)
	if e != nil {
		return model.Progress{}, e
	}
	prior, e := s.Repo.Rewards(ctx, tx, sc, m.UserID)
	if e != nil {
		return model.Progress{}, e
	}
	out, created, e := model.Evaluate(v, m, regs, facts, gate, prior, now)
	if e != nil {
		return out, e
	}
	if e = s.Members.Allowed(ctx, tx, sc, m.AgentID, now); e != nil {
		if !errors.Is(e, foundation.ErrConflict) {
			return out, e
		}
		out.Enabled = false
		out.BlockedReason = "AGENT_NOT_ACTIVE"
		created = nil
	}
	if s.CanReward != nil {
		if err := s.CanReward(ctx, tx, sc); err != nil {
			if err.Error() != "SITE_AGENT_DISABLED" {
				return out, err
			}
			out.Enabled = false
			out.BlockedReason = "SITE_AGENT_DISABLED"
			created = nil
		}
	}
	if len(created) > 0 {
		for _, r := range created {
			r.EntitlementID, e = s.Funds.Unlock(ctx, tx, sc, r, now)
			if e != nil {
				return out, e
			}
			if e = s.Repo.SaveReward(ctx, tx, sc, r); e != nil {
				return out, e
			}
			out.Rewards = append(out.Rewards, r)
		}
	}
	e = s.Repo.SaveProgress(ctx, tx, sc, m.UserID, out, now)
	return out, e
}
func (s Service) Receive(ctx context.Context, p principal.Principal, raw []byte, signature, key, rid string) (foundation.CommandResult, error) {
	var selector struct {
		Source string `json:"source_id"`
	}
	if json.Unmarshal(raw, &selector) != nil {
		return foundation.CommandResult{}, foundation.ErrValidation
	}
	source := s.Sources[selector.Source]
	custom := source == nil && s.ResolveSource != nil
	if custom {
		var err error
		source, err = s.ResolveSource(ctx, p, selector.Source)
		if err != nil {
			return foundation.CommandResult{}, err
		}
	}
	if source == nil {
		return foundation.CommandResult{}, errors.New("PERMISSION_DENIED")
	}
	event, e := source.Verify(ctx, scope(p), raw, signature)
	if e != nil {
		return foundation.CommandResult{}, e
	}
	ctx = foundation.WithRequest(ctx, rid, p.Subject)
	sc := scope(p)
	if e = s.Authorize(ctx, s.Query, p, "affiliate:ingest"); e != nil {
		return foundation.CommandResult{}, e
	}
	accepted := false
	late := false
	var payload any = event
	if custom {
		sum := sha256.Sum256(raw)
		payload = struct {
			Event  model.Event `json:"event"`
			Digest string      `json:"raw_digest"`
		}{event, hex.EncodeToString(sum[:])}
	}
	out, e := s.Commands.Execute(ctx, sc, "affiliate-business-fact", key, payload, func(tx db.Executor) (foundation.CommandResult, error) {
		if e := s.Authorize(ctx, tx, p, "affiliate:ingest"); e != nil {
			return foundation.CommandResult{}, e
		}
		if custom && s.SourceGuard != nil {
			if e := s.SourceGuard(ctx, tx, p, raw, signature, event); e != nil {
				return foundation.CommandResult{}, e
			}
		}
		if e := s.Repo.Lock(ctx, tx, sc); e != nil {
			return foundation.CommandResult{}, e
		}
		now := s.Now().UTC().Truncate(time.Microsecond)
		business, e := s.Configs.At(ctx, tx, sc, event.At)
		if errors.Is(e, sql.ErrNoRows) {
			e = errors.New("UNKNOWN_FACT")
		}
		if e != nil {
			return foundation.CommandResult{}, e
		}
		legacyValid := event.Validate(business.Config, now) == nil
		if !legacyValid {
			if s.ProgramValidate == nil {
				return foundation.CommandResult{}, foundation.ErrValidation
			}
			if e = s.ProgramValidate(ctx, tx, sc, event, now); e != nil {
				return foundation.CommandResult{}, e
			}
		}
		member, e := s.Members.Resolve(ctx, tx, sc, event.Source, event.ExternalUser)
		if e != nil {
			return foundation.CommandResult{}, e
		}
		if event.At.Before(member.RegisteredAt) {
			return foundation.CommandResult{}, foundation.ErrValidation
		}
		day, _ := business.Config.Day(event.At)
		if s.DailyRepo != nil {
			late, e = s.DailyRepo.Closed(ctx, tx, sc, event.Source, day.In(business.Config.Location()).Format("2006-01-02"))
			if e != nil {
				return foundation.CommandResult{}, e
			}
		}
		eligible := legacyValid && business.Config.Enabled
		if event.Kind == "BET_SETTLED" && !business.Config.AllowsVenue(event.Venue) {
			eligible = false
		}
		if s.CanRewardAt != nil {
			if err := s.CanRewardAt(ctx, tx, sc, event.At); err != nil {
				if err.Error() != "SITE_AGENT_DISABLED" {
					return foundation.CommandResult{}, err
				}
				eligible = false
			}
		}
		accepted, e = s.Repo.SaveFact(ctx, tx, sc, event, member.UserID, now, late, eligible)
		if e != nil {
			return foundation.CommandResult{}, e
		}
		if accepted && !late && s.ProgramAccept != nil {
			// 新方案只处理已验签且首次接收的事实；同事务失败会回滚旧方案和全部新方案。
			if e = s.ProgramAccept(ctx, tx, sc, event, member.UserID, now); e != nil {
				return foundation.CommandResult{}, e
			}
		}
		if accepted && !late && legacyValid {
			current, e := s.Configs.At(ctx, tx, sc, now)
			if e != nil {
				return foundation.CommandResult{}, e
			}
			regs, e := s.Members.List(ctx, tx, sc, now)
			if e != nil {
				return foundation.CommandResult{}, e
			}
			facts, e := s.Repo.Facts(ctx, tx, sc, now.Add(time.Microsecond))
			if e != nil {
				return foundation.CommandResult{}, e
			}
			// 只推进本人和直属推荐人；其他祖先只在日终计佣参与，不误发跨层推荐奖励。
			for _, m := range regs {
				if m.UserID == member.UserID || m.UserID == member.ParentUser {
					if _, e = s.refresh(ctx, tx, sc, current, regs, facts, m, now); e != nil {
						return foundation.CommandResult{}, e
					}
				}
			}
		}
		if custom && s.SourceReceipt != nil {
			if e := s.SourceReceipt(ctx, tx, sc, event, raw, accepted, late, now); e != nil {
				return foundation.CommandResult{}, e
			}
		}
		return response.Result(200, event.EventID, rid, map[string]any{"event_id": event.EventID, "business_id": event.BusinessID, "accepted": accepted && !late, "late_adjustment": late, "user_id": member.UserID})
	})
	if e == nil && out.Status == 200 && accepted && !out.Replayed {
		slog.InfoContext(ctx, "代理业务事实已接入并推进奖励资格", "request_id", rid, "source_id", event.Source, "business_id", event.BusinessID, "event_id", event.EventID, "result", "SUCCEEDED")
	}
	return out, e
}
func (s Service) RefreshOwn(ctx context.Context, p principal.Principal, key, rid string) (foundation.CommandResult, error) {
	if p.MemberUserID == "" {
		return foundation.CommandResult{}, principal.ErrPermission
	}
	sc := scope(p)
	if e := s.Authorize(ctx, s.Query, p, "member:read"); e != nil {
		return foundation.CommandResult{}, e
	}
	ctx = foundation.WithRequest(ctx, rid, p.Subject)
	return s.Commands.Execute(ctx, sc, "affiliate-progress-refresh", key, map[string]string{"user_id": p.MemberUserID}, func(tx db.Executor) (foundation.CommandResult, error) {
		if e := s.Authorize(ctx, tx, p, "member:read"); e != nil {
			return foundation.CommandResult{}, e
		}
		if e := s.Repo.Lock(ctx, tx, sc); e != nil {
			return foundation.CommandResult{}, e
		}
		now := s.Now().UTC().Truncate(time.Microsecond)
		v, e := s.Configs.At(ctx, tx, sc, now)
		if e != nil {
			return foundation.CommandResult{}, e
		}
		m, e := s.Members.Own(ctx, tx, sc, p.MemberUserID)
		if e != nil {
			return foundation.CommandResult{}, e
		}
		regs, e := s.Members.List(ctx, tx, sc, now)
		if e != nil {
			return foundation.CommandResult{}, e
		}
		facts, e := s.Repo.Facts(ctx, tx, sc, now.Add(time.Microsecond))
		if e != nil {
			return foundation.CommandResult{}, e
		}
		progress, e := s.refresh(ctx, tx, sc, v, regs, facts, m, now)
		if e != nil {
			return foundation.CommandResult{}, e
		}
		return response.Result(200, m.UserID, rid, progress)
	})
}
func (s Service) Read(ctx context.Context, p principal.Principal) (model.Progress, error) {
	if p.MemberUserID == "" {
		return model.Progress{}, principal.ErrPermission
	}
	out, e := s.Repo.Progress(ctx, s.Query, scope(p), p.MemberUserID)
	if e != nil {
		return out, e
	}
	out.Rewards, e = s.Repo.Rewards(ctx, s.Query, scope(p), p.MemberUserID)
	return out, e
}

type Claim struct {
	Kind      string `json:"kind"`
	ProgramID string `json:"program_id,omitempty"`
}

func (s Service) Claim(ctx context.Context, p principal.Principal, in Claim, ip, key, rid string) (foundation.CommandResult, error) {
	if p.MemberUserID == "" || (in.Kind != model.Referral && in.Kind != model.Invitee && in.Kind != model.Commission && in.Kind != "ALL") {
		return foundation.CommandResult{}, foundation.ErrValidation
	}
	sc := scope(p)
	if e := s.Authorize(ctx, s.Query, p, "member:claim"); e != nil {
		return foundation.CommandResult{}, e
	}
	ctx = foundation.WithRequest(ctx, rid, p.Subject)
	out, e := s.Commands.Execute(ctx, sc, "affiliate-member-claim", key, struct {
		User  string
		Input Claim
	}{p.MemberUserID, in}, func(tx db.Executor) (foundation.CommandResult, error) {
		if e := s.Authorize(ctx, tx, p, "member:claim"); e != nil {
			return foundation.CommandResult{}, e
		}
		if e := s.Repo.Lock(ctx, tx, sc); e != nil {
			return foundation.CommandResult{}, e
		}
		now := s.Now().UTC().Truncate(time.Microsecond)
		v, e := s.Configs.At(ctx, tx, sc, now)
		if e != nil {
			return foundation.CommandResult{}, e
		}
		// 停止新增计奖不冻结历史已产生权益；本站开关和会员风控仍由原授权与Gate校验。
		_ = v
		m, e := s.Members.Own(ctx, tx, sc, p.MemberUserID)
		if e != nil {
			return foundation.CommandResult{}, e
		}
		g, e := s.Repo.Gate(ctx, tx, sc, m.UserID, m.RegisteredAt)
		if e != nil {
			return foundation.CommandResult{}, e
		}
		if !g.Enabled {
			return foundation.CommandResult{}, errors.New("STATE_CONFLICT:AGENT_REWARDS_DISABLED")
		}
		if e = s.Members.Allowed(ctx, tx, sc, m.AgentID, now); e != nil {
			return foundation.CommandResult{}, e
		}
		rewards, e := s.Repo.Rewards(ctx, tx, sc, m.UserID)
		if e != nil {
			return foundation.CommandResult{}, e
		}
		sort.Slice(rewards, func(i, j int) bool { return rewards[i].ID < rewards[j].ID })
		batch := model.ID("claim", p.Subject, key)
		claimed := []model.Reward{}
		for _, r := range rewards {
			if r.ProgramID != in.ProgramID {
				continue
			}
			if (r.Kind != in.Kind && (in.Kind != "ALL" || r.Kind == model.Invitee)) || r.Status != "CLAIMABLE" {
				continue
			}
			if e = s.Funds.Claim(ctx, tx, sc, r, batch, now); e != nil {
				return foundation.CommandResult{}, e
			}
			r.UpdatedAt = now
			r.Status = "CLAIMED"
			r.ClaimedAt = &now
			r.ClaimID = batch
			r.ClaimIP = ip
			r.Revision++
			if e = s.Repo.SaveReward(ctx, tx, sc, r); e != nil {
				return foundation.CommandResult{}, e
			}
			claimed = append(claimed, r)
		}
		if len(claimed) == 0 {
			return foundation.CommandResult{}, errors.New("STATE_CONFLICT:NO_CLAIMABLE_REWARD")
		}
		return response.Result(200, batch, rid, map[string]any{"claim_id": batch, "rewards": claimed})
	})
	if e == nil && out.Status == 200 && !out.Replayed {
		slog.InfoContext(ctx, "会员奖励领取已原子入账", "request_id", rid, "user_id", p.MemberUserID, "kind", in.Kind, "claim_id", out.ResourceID, "result", "SUCCEEDED")
	}
	return out, e
}
