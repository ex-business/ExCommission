package application

import (
	"context"
	"database/sql"
	"errors"
	affiliate "github.com/ex-business/ExCommission/internal/affiliate/domain"
	model "github.com/ex-business/ExCommission/internal/affiliateflow/domain"
	principal "github.com/ex-business/ExCommission/internal/identity/domain"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	"time"
)

type ReferralProgress struct {
	UserID        string                  `json:"user_id"`
	RegisteredAt  time.Time               `json:"registered_at"`
	Qualification affiliate.Qualification `json:"qualification"`
}
type Overview struct {
	DirectMembers         []ReferralProgress         `json:"direct_members"`
	ClaimContextAvailable bool                       `json:"claim_context_available"`
	Configured            bool                       `json:"configured"`
	Configuration         *affiliate.Version         `json:"configuration"`
	Progress              *model.Progress            `json:"progress"`
	Grade                 *affiliate.DailyCommission `json:"current_grade"`
	Provisional           bool                       `json:"provisional"`
	ObservedAt            time.Time                  `json:"observed_at"`
}

// Overview 使用同一领域计算显示当前实绩，不把尚未关账的等级冒充最终等级；读取不生成新权益。
func (s Service) Overview(ctx context.Context, p principal.Principal) (Overview, error) {
	out := Overview{DirectMembers: []ReferralProgress{}, ClaimContextAvailable: s.ClaimContextReady, ObservedAt: s.Now().UTC().Truncate(time.Microsecond), Provisional: true}
	if p.MemberUserID == "" {
		return out, principal.ErrPermission
	}
	sc := scope(p)
	v, e := s.Configs.At(ctx, s.Query, sc, out.ObservedAt)
	if errors.Is(e, sql.ErrNoRows) {
		return out, nil
	}
	if e != nil {
		return out, e
	}
	out.Configured = true
	out.Configuration = &v
	m, e := s.Members.Own(ctx, s.Query, sc, p.MemberUserID)
	if e != nil {
		return out, e
	}
	regs, e := s.Members.List(ctx, s.Query, sc, out.ObservedAt)
	if e != nil {
		return out, e
	}
	facts, e := s.Repo.Facts(ctx, s.Query, sc, out.ObservedAt.Add(time.Microsecond))
	if e != nil {
		return out, e
	}
	gate, e := s.Repo.Gate(ctx, s.Query, sc, m.UserID, m.RegisteredAt)
	if e != nil {
		return out, e
	}
	prior, e := s.Repo.Rewards(ctx, s.Query, sc, m.UserID)
	if e != nil {
		return out, e
	}
	progress, _, e := model.Evaluate(v, m, regs, facts, gate, prior, out.ObservedAt)
	if e != nil {
		return out, e
	}
	if e = s.Members.Allowed(ctx, s.Query, sc, m.AgentID, out.ObservedAt); e != nil {
		if !errors.Is(e, foundation.ErrConflict) {
			return out, e
		}
		progress.Enabled = false
		progress.BlockedReason = "AGENT_NOT_ACTIVE"
	}
	if s.CanReward != nil {
		if err := s.CanReward(ctx, s.Query, sc); err != nil {
			if err.Error() != "SITE_AGENT_DISABLED" {
				return out, err
			}
			progress.Enabled = false
			progress.BlockedReason = "SITE_AGENT_DISABLED"
		}
	}
	// 仅公开本人直属会员的推荐达标进度，不能从浏览器传任意上级或会员ID。
	out.DirectMembers = []ReferralProgress{}
	for _, child := range regs {
		if child.ParentUser != m.UserID {
			continue
		}
		selected := []affiliate.Fact{}
		for _, fact := range facts {
			if fact.UserID == child.UserID && !fact.At.Before(gate.Since) {
				selected = append(selected, fact)
			}
		}
		q, err := affiliate.Qualify(v.Config.ReferralCondition, selected, v.Config, out.ObservedAt.Add(time.Microsecond))
		if err != nil {
			return out, err
		}
		out.DirectMembers = append(out.DirectMembers, ReferralProgress{UserID: child.UserID, RegisteredAt: child.RegisteredAt, Qualification: q})
	}
	out.Progress = &progress
	selected := []affiliate.Fact{}
	for _, f := range facts {
		if !f.At.Before(gate.Since) {
			selected = append(selected, f)
		}
	}
	domainRegs := []affiliate.Registration{}
	for _, r := range regs {
		domainRegs = append(domainRegs, affiliate.Registration{UserID: r.UserID, ParentID: r.ParentUser, At: r.RegisteredAt})
	}
	var grade affiliate.DailyCommission
	if s.Relations == nil {
		grade, e = affiliate.CalculateRiskDay(v.Config, m.UserID, out.ObservedAt, domainRegs, selected, nil, facts)
	} else {
		domainRegs, e = s.Relations.Close(ctx, s.Query, sc, out.ObservedAt)
		if e != nil {
			return out, e
		}
		start, end := v.Config.Day(out.ObservedAt)
		if len(v.Config.RiskRules) > 0 {
			start = time.Time{}
		}
		ownership, err := s.Relations.Attributions(ctx, s.Query, sc, facts, start, end)
		if err != nil {
			return out, err
		}
		grade, e = affiliate.CalculateRiskDay(v.Config, m.UserID, out.ObservedAt, domainRegs, selected, ownership, facts)
	}
	if e != nil {
		return out, e
	}
	// 全站去重可能命中其他团队；会员端只展示原因，不泄漏跨团队赢家身份。
	for i := range grade.RiskDecisions {
		grade.RiskDecisions[i].Winner = ""
	}
	out.Grade = &grade
	return out, nil
}
