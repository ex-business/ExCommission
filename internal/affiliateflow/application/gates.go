package application

import (
	"context"
	"errors"
	principal "github.com/ex-business/ExCommission/internal/identity/domain"
	response "github.com/ex-business/ExCommission/internal/platform/application"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"log/slog"
	"time"
)

type GateChange struct {
	Enabled   *bool  `json:"enabled"`
	Revision  uint64 `json:"expected_revision"`
	Reason    string `json:"reason"`
	Confirmed bool   `json:"confirmed"`
}

func (s Service) Toggle(ctx context.Context, p principal.Principal, user string, in GateChange, key, rid string) (foundation.CommandResult, error) {
	if !foundation.ValidID(user) || in.Enabled == nil || !in.Confirmed || !foundation.ValidID(in.Reason) || s.StepUp == nil {
		return foundation.CommandResult{}, foundation.ErrValidation
	}
	payload := struct {
		User   string
		Change GateChange
	}{user, in}
	// TOTP 在幂等返回前也验证，不能拿旧命令结果绕过本次敏感操作的二次认证。
	if e := s.StepUp(ctx, p, "affiliate-agent-toggle", key, payload); e != nil {
		return foundation.CommandResult{}, e
	}
	sc := scope(p)
	ctx = foundation.WithRequest(ctx, rid, p.Subject)
	out, e := s.Commands.Execute(ctx, sc, "affiliate-agent-toggle", key, payload, func(tx db.Executor) (foundation.CommandResult, error) {
		if e := s.Authorize(ctx, tx, p, "affiliate:manage"); e != nil {
			return foundation.CommandResult{}, e
		}
		if e := s.Repo.Lock(ctx, tx, sc); e != nil {
			return foundation.CommandResult{}, e
		}
		now := s.Now().UTC().Truncate(time.Microsecond)
		m, e := s.Members.Own(ctx, tx, sc, user)
		if e != nil {
			return foundation.CommandResult{}, e
		}
		g, e := s.Repo.Gate(ctx, tx, sc, user, m.RegisteredAt)
		if e != nil {
			return foundation.CommandResult{}, e
		}
		if g.Revision != in.Revision {
			return foundation.CommandResult{}, errors.New("REVISION_CONFLICT")
		}
		if g.Enabled == *in.Enabled {
			return foundation.CommandResult{}, foundation.ErrConflict
		}
		g.Enabled = *in.Enabled
		g.Revision++
		g.Reason = in.Reason
		g.Actor = p.Subject
		g.Updated = now
		// 恢复只开启新的累计区间；既有奖励仍存留，之前已占用的阶梯会员不会重复奖励。
		if g.Enabled {
			g.Since = now
		}
		if e = s.Repo.SaveGate(ctx, tx, sc, user, g); e != nil {
			return foundation.CommandResult{}, e
		}
		return response.Result(200, user, rid, g)
	})
	if e == nil && out.Status == 200 && !out.Replayed {
		slog.InfoContext(ctx, "单代理奖励开关已更新", "request_id", rid, "user_id", user, "enabled", *in.Enabled, "actor_id", p.Subject, "result", "SUCCEEDED")
	}
	return out, e
}
