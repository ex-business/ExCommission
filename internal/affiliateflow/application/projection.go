package application

import (
	"context"
	"database/sql"
	"errors"
	model "github.com/ex-business/ExCommission/internal/affiliateflow/domain"
	response "github.com/ex-business/ExCommission/internal/platform/application"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"strconv"
	"time"
)

// RefreshAll 补齐配置生效后或暂时没有新订单的会员进度；仅供后台可信进程调用，不提供任意用户刷新HTTP入口。
func (s Service) RefreshAll(ctx context.Context, sc foundation.Scope) error {
	now := s.Now().UTC().Truncate(time.Microsecond)
	v, e := s.Configs.At(ctx, s.Query, sc, now)
	if errors.Is(e, sql.ErrNoRows) {
		return nil
	}
	if e != nil {
		return e
	}
	members, e := s.Members.List(ctx, s.Query, sc, now)
	if e != nil {
		return e
	}
	for _, m := range members {
		input := struct {
			User     string
			Revision uint64
			Bucket   time.Time
		}{m.UserID, v.Revision, now.Truncate(time.Minute)}
		key := model.ID("projection", m.UserID, strconv.FormatUint(v.Revision, 10), input.Bucket.Format(time.RFC3339Nano))
		c := foundation.WithRequest(ctx, key, "system-affiliate-rewards")
		result, e := s.Commands.Execute(c, sc, "affiliate-rewards-projection", key, input, func(tx db.Executor) (foundation.CommandResult, error) {
			if e := s.Repo.Lock(c, tx, sc); e != nil {
				return foundation.CommandResult{}, e
			}
			current, e := s.Configs.At(c, tx, sc, now)
			if e != nil {
				return foundation.CommandResult{}, e
			}
			regs, e := s.Members.List(c, tx, sc, now)
			if e != nil {
				return foundation.CommandResult{}, e
			}
			facts, e := s.Repo.Facts(c, tx, sc, now.Add(time.Microsecond))
			if e != nil {
				return foundation.CommandResult{}, e
			}
			p, e := s.refresh(c, tx, sc, current, regs, facts, m, now)
			if e != nil {
				return foundation.CommandResult{}, e
			}
			return response.Result(200, m.UserID, key, p)
		})
		if e != nil {
			return e
		}
		if result.Status >= 400 {
			return errors.New("代理奖励投影未完成，保留任务重试")
		}
	}
	return nil
}
