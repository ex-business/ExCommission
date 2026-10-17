package application

import (
	"context"
	"encoding/hex"
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
	"sort"
	"time"
)

func (s Service) ReceiveWatermark(ctx context.Context, p principal.Principal, raw []byte, signature, key, rid string) (foundation.CommandResult, error) {
	var selector struct {
		Source string `json:"source_id"`
	}
	if json.Unmarshal(raw, &selector) != nil {
		return foundation.CommandResult{}, foundation.ErrValidation
	}
	candidate := s.Sources[selector.Source]
	custom := candidate == nil && s.ResolveSource != nil
	if candidate == nil && s.ResolveSource != nil {
		var err error
		candidate, err = s.ResolveSource(ctx, p, selector.Source)
		if err != nil {
			return foundation.CommandResult{}, err
		}
	}
	source, ok := candidate.(ports.WatermarkSource)
	if !ok {
		return foundation.CommandResult{}, principal.ErrPermission
	}
	m, e := source.VerifyWatermark(ctx, scope(p), raw, signature)
	if e != nil {
		return foundation.CommandResult{}, e
	}
	if len(m.Day) != 10 || len(m.Digest) != 64 {
		return foundation.CommandResult{}, foundation.ErrValidation
	}
	if _, e = hex.DecodeString(m.Digest); e != nil {
		return foundation.CommandResult{}, foundation.ErrValidation
	}
	if e = s.Authorize(ctx, s.Query, p, "affiliate:ingest"); e != nil {
		return foundation.CommandResult{}, e
	}
	sc := scope(p)
	ctx = foundation.WithRequest(ctx, rid, p.Subject)
	out, e := s.Commands.Execute(ctx, sc, "affiliate-source-watermark", key, m, func(tx db.Executor) (foundation.CommandResult, error) {
		if e := s.Authorize(ctx, tx, p, "affiliate:ingest"); e != nil {
			return foundation.CommandResult{}, e
		}
		if custom && s.WatermarkGuard != nil {
			if e := s.WatermarkGuard(ctx, tx, p, raw, signature, m); e != nil {
				return foundation.CommandResult{}, e
			}
		}
		if e := s.Repo.Lock(ctx, tx, sc); e != nil {
			return foundation.CommandResult{}, e
		}
		now := s.Now().UTC().Truncate(time.Microsecond)
		v, e := s.Configs.At(ctx, tx, sc, now)
		if e != nil {
			return foundation.CommandResult{}, e
		}
		day, e := time.ParseInLocation("2006-01-02", m.Day, v.Config.Location())
		if e != nil {
			return foundation.CommandResult{}, foundation.ErrValidation
		}
		start, end := v.Config.Day(day)
		if end.After(now) {
			return foundation.CommandResult{}, errors.New("STATE_CONFLICT:BUSINESS_DAY_NOT_FINISHED")
		}
		count, digest, e := s.DailyRepo.Manifest(ctx, tx, sc, m.Source, start, end)
		if e != nil {
			return foundation.CommandResult{}, e
		}
		if count != m.Count || digest != m.Digest {
			return foundation.CommandResult{}, errors.New("STATE_CONFLICT:SOURCE_WATERMARK_INCOMPLETE")
		}
		if e = s.DailyRepo.SaveWatermark(ctx, tx, sc, m, now); e != nil {
			return foundation.CommandResult{}, e
		}
		return response.Result(200, model.ID(m.Source, m.Day), rid, map[string]any{"watermark": m, "status": "SOURCE_CLOSED"})
	})
	if e == nil && out.Status == 200 && !out.Replayed {
		slog.InfoContext(ctx, "来源业务日已完成清单核对并关闭", "request_id", rid, "source_id", m.Source, "business_day", m.Day, "fact_count", m.Count, "result", "SUCCEEDED")
	}
	return out, e
}

var errSourcesIncomplete = errors.New("代理业务来源水位尚未完整")

func (s Service) freeze(ctx context.Context, sc foundation.Scope, day string) error {
	p := principal.Principal{Tenant: sc.Tenant, App: sc.App, Subject: "system-affiliate-dayend"}
	ctx = foundation.WithRequest(ctx, model.ID("freeze", day), p.Subject)
	out, e := s.Commands.Execute(ctx, sc, "affiliate-day-freeze", day, map[string]string{"business_day": day}, func(tx db.Executor) (foundation.CommandResult, error) {
		if e := s.Repo.Lock(ctx, tx, sc); e != nil {
			return foundation.CommandResult{}, e
		}
		existing, e := s.DailyRepo.Daily(ctx, tx, sc, day)
		if e == nil {
			return response.Result(200, day, "affiliate-day-freeze", existing)
		}
		if e.Error() != "NOT_FOUND" {
			return foundation.CommandResult{}, e
		}
		marks, e := s.DailyRepo.Watermarks(ctx, tx, sc, day)
		if e != nil {
			return foundation.CommandResult{}, e
		}
		complete := map[string]bool{}
		for _, m := range marks {
			complete[m.Source] = true
		}
		now := s.Now().UTC().Truncate(time.Microsecond)
		current, e := s.Configs.At(ctx, tx, sc, now)
		if e != nil {
			return foundation.CommandResult{}, e
		}
		date, e := time.ParseInLocation("2006-01-02", day, current.Config.Location())
		if e != nil {
			return foundation.CommandResult{}, foundation.ErrValidation
		}
		_, end := current.Config.Day(date)
		if end.After(now) {
			return foundation.CommandResult{}, errSourcesIncomplete
		}
		required := map[string]bool{}
		for source := range s.Sources {
			required[source] = true
		}
		if s.RequiredSources != nil {
			start, end := current.Config.Day(date)
			ids, err := s.RequiredSources(ctx, tx, sc, start, end)
			if err != nil {
				return foundation.CommandResult{}, err
			}
			for _, source := range ids {
				required[source] = true
			}
		}
		if len(required) == 0 {
			return foundation.CommandResult{}, errSourcesIncomplete
		}
		for source := range required {
			if !complete[source] {
				return foundation.CommandResult{}, errSourcesIncomplete
			}
		}
		// 业务日最终版本在关账时冻结；后续配置变更不能重算已冻结日期的等级、费率或归属。
		version, e := s.Configs.At(ctx, tx, sc, end.Add(-time.Microsecond))
		if e != nil {
			return foundation.CommandResult{}, e
		}
		regs, e := s.Members.List(ctx, tx, sc, end.Add(-time.Microsecond))
		if e != nil {
			return foundation.CommandResult{}, e
		}
		facts, e := s.Repo.Facts(ctx, tx, sc, end)
		if e != nil {
			return foundation.CommandResult{}, e
		}
		domainRegs := []affiliate.Registration{}
		for _, m := range regs {
			domainRegs = append(domainRegs, affiliate.Registration{UserID: m.UserID, ParentID: m.ParentUser, At: m.RegisteredAt})
		}
		if s.Relations != nil {
			domainRegs, e = s.Relations.Close(ctx, tx, sc, end.Add(-time.Microsecond))
			if e != nil {
				return foundation.CommandResult{}, e
			}
		}
		sort.Slice(regs, func(i, j int) bool { return regs[i].AgentID < regs[j].AgentID })
		daily := model.Daily{Day: day, Status: "CALCULATED", Config: version, Watermarks: marks, Results: []model.FrozenCommission{}, FrozenAt: now}
		for _, m := range regs {
			g, e := s.Repo.Gate(ctx, tx, sc, m.UserID, m.RegisteredAt)
			if e != nil {
				return foundation.CommandResult{}, e
			}
			selected := []affiliate.Fact{}
			for _, f := range facts {
				if !f.At.Before(g.Since) {
					selected = append(selected, f)
				}
			}
			var result affiliate.DailyCommission
			if s.Relations == nil {
				result, e = affiliate.CalculateRiskDay(version.Config, m.UserID, date, domainRegs, selected, nil, facts)
			} else {
				start, _ := version.Config.Day(date)
				// 跨日去重需要历史业务发生时的归属，不能只按今日团队重选赢家。
				if len(version.Config.RiskRules) > 0 {
					start = time.Time{}
				}
				ownership, err := s.attributions(ctx, tx, sc, facts, start, end, version.Config.DepthLimit())
				if err != nil {
					return foundation.CommandResult{}, err
				}
				result, e = affiliate.CalculateRiskDay(version.Config, m.UserID, date, domainRegs, selected, ownership, facts)
			}
			if e != nil {
				return foundation.CommandResult{}, e
			}
			eligible := version.Config.Enabled && g.Enabled && g.Since.Before(end)
			if e = s.Members.Allowed(ctx, tx, sc, m.AgentID, now); e != nil {
				if !errors.Is(e, foundation.ErrConflict) {
					return foundation.CommandResult{}, e
				}
				eligible = false
			}
			if s.CanReward != nil {
				if err := s.CanReward(ctx, tx, sc); err != nil {
					if err.Error() != "SITE_AGENT_DISABLED" {
						return foundation.CommandResult{}, err
					}
					eligible = false
				}
			}
			daily.Results = append(daily.Results, model.FrozenCommission{Since: g.Since, AgentID: m.AgentID, Eligible: eligible, Result: result})
		}
		if e = s.DailyRepo.SaveDaily(ctx, tx, sc, daily, now); e != nil {
			return foundation.CommandResult{}, e
		}
		return response.Result(200, day, "affiliate-day-freeze", daily)
	})
	if errors.Is(e, errSourcesIncomplete) {
		return nil
	}
	if e == nil && out.Status >= 400 {
		return errors.New("日终冻结失败，保留原日期重试")
	}
	return e
}
func (s Service) settleDay(ctx context.Context, sc foundation.Scope, day string) error {
	daily, e := s.DailyRepo.Daily(ctx, s.Query, sc, day)
	if e != nil {
		if e.Error() == "NOT_FOUND" {
			return nil
		}
		return e
	}
	if daily.Status == "SETTLED" {
		return nil
	}
	date, e := time.ParseInLocation("2006-01-02", day, daily.Config.Config.Location())
	if e != nil {
		return e
	}
	if date.UTC().After(daily.Config.Config.DueDate(s.Now())) {
		return nil
	}
	ctx = foundation.WithRequest(ctx, model.ID("settle", day), "system-affiliate-dayend")
	out, e := s.Commands.Execute(ctx, sc, "affiliate-day-settle", day, map[string]string{"business_day": day}, func(tx db.Executor) (foundation.CommandResult, error) {
		if e := s.Repo.Lock(ctx, tx, sc); e != nil {
			return foundation.CommandResult{}, e
		}
		d, e := s.DailyRepo.Daily(ctx, tx, sc, day)
		if e != nil {
			return foundation.CommandResult{}, e
		}
		if d.Status == "SETTLED" {
			return response.Result(200, day, "affiliate-day-settle", d)
		}
		now := s.Now().UTC().Truncate(time.Microsecond)
		siteEnabled := true
		if s.CanReward != nil {
			if err := s.CanReward(ctx, tx, sc); err != nil {
				if err.Error() != "SITE_AGENT_DISABLED" {
					return foundation.CommandResult{}, err
				}
				siteEnabled = false
			}
		}
		for _, frozen := range d.Results {
			if !siteEnabled || !frozen.Eligible || frozen.Result.Amount == "0" {
				continue
			}
			m, e := s.Members.Own(ctx, tx, sc, frozen.Result.UserID)
			if e != nil {
				return foundation.CommandResult{}, e
			}
			g, e := s.Repo.Gate(ctx, tx, sc, m.UserID, m.RegisteredAt)
			if e != nil {
				return foundation.CommandResult{}, e
			}
			_, end := d.Config.Config.Day(date)
			if !g.Enabled || !g.Since.Before(end) {
				continue
			}
			if e = s.Members.Allowed(ctx, tx, sc, m.AgentID, now); e != nil {
				if errors.Is(e, foundation.ErrConflict) {
					continue
				}
				return foundation.CommandResult{}, e
			}
			// 01点结算只产生待审核业务单，不调用钱包；审核通过才生成受控待领取权益。
			r := model.Reward{Since: frozen.Since, Qualifications: []model.Qualified{}, ID: model.ID(model.Commission, m.UserID, day), UserID: m.UserID, AgentID: m.AgentID, Kind: model.Commission, Day: day, Currency: d.Config.Config.Currency, Amount: frozen.Result.Amount, Original: frozen.Result.Amount, ConfigRevision: d.Config.Revision, CreatedAt: now, UpdatedAt: now, Status: "PENDING_REVIEW", UnlockedAt: now, Members: []string{}, Revision: 1}
			if e = s.Repo.SaveReward(ctx, tx, sc, r); e != nil {
				return foundation.CommandResult{}, e
			}
		}
		d.Status = "SETTLED"
		d.SettledAt = &now
		if e = s.DailyRepo.SaveDaily(ctx, tx, sc, d, now); e != nil {
			return foundation.CommandResult{}, e
		}
		return response.Result(200, day, "affiliate-day-settle", d)
	})
	if e == nil && out.Status == 200 && !out.Replayed {
		slog.InfoContext(ctx, "代理业务日已按最终等级完成结算待审核", "business_day", day, "tenant_id", sc.Tenant, "app_id", sc.App, "result", "SUCCEEDED")
	}
	if e == nil && out.Status >= 400 {
		return errors.New("日终结算失败，保留原日期重试")
	}
	return e
}

// RunOnce 可由独立worker重入；业务日期与命令幂等记录保证宕机恢复、夏令时重复01点都不重复结算。
func (s Service) RunOnce(ctx context.Context, sc foundation.Scope) error {
	days, e := s.DailyRepo.PendingDays(ctx, s.Query, sc)
	if e != nil {
		return e
	}
	for _, day := range days {
		if e = s.freeze(ctx, sc, day); e != nil {
			return e
		}
		if e = s.settleDay(ctx, sc, day); e != nil {
			return e
		}
	}
	return nil
}
