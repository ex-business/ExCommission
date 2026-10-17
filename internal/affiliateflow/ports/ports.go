// Package ports 隔离事实存储、注册关系、发布配置、账簿和钱包，不让来源适配器直接发奖。
package ports

import (
	"context"
	affiliate "github.com/ex-business/ExCommission/internal/affiliate/domain"
	model "github.com/ex-business/ExCommission/internal/affiliateflow/domain"
	principal "github.com/ex-business/ExCommission/internal/identity/domain"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"time"
)

type Repository interface {
	RawFacts(context.Context, db.Executor, foundation.Scope, time.Time) ([]affiliate.Fact, error)
	Lock(context.Context, db.Executor, foundation.Scope) error
	SaveFact(context.Context, db.Executor, foundation.Scope, model.Event, string, time.Time, bool, bool) (bool, error)
	Facts(context.Context, db.Executor, foundation.Scope, time.Time) ([]affiliate.Fact, error)
	Gate(context.Context, db.Executor, foundation.Scope, string, time.Time) (model.Gate, error)
	SaveGate(context.Context, db.Executor, foundation.Scope, string, model.Gate) error
	Rewards(context.Context, db.Executor, foundation.Scope, string) ([]model.Reward, error)
	Reward(context.Context, db.Executor, foundation.Scope, string) (model.Reward, error)
	SaveReward(context.Context, db.Executor, foundation.Scope, model.Reward) error
	SaveProgress(context.Context, db.Executor, foundation.Scope, string, model.Progress, time.Time) error
	Progress(context.Context, db.Executor, foundation.Scope, string) (model.Progress, error)
}
type Members interface {
	Resolve(context.Context, db.Executor, foundation.Scope, string, string) (model.Member, error)
	List(context.Context, db.Executor, foundation.Scope, time.Time) ([]model.Member, error)
	Own(context.Context, db.Executor, foundation.Scope, string) (model.Member, error)
	Allowed(context.Context, db.Executor, foundation.Scope, string, time.Time) error
}
type Configurations interface {
	At(context.Context, db.Executor, foundation.Scope, time.Time) (affiliate.Version, error)
}
type Funds interface {
	Unlock(context.Context, db.Executor, foundation.Scope, model.Reward, time.Time) (string, error)
	Claim(context.Context, db.Executor, foundation.Scope, model.Reward, string, time.Time) error
	Cancel(context.Context, db.Executor, foundation.Scope, model.Reward, time.Time) error
}
type StepUp func(context.Context, principal.Principal, string, string, any) error
type Authorize func(context.Context, db.Executor, principal.Principal, string) error

// Source 每次接入（包括重放）都重新验签，不能借幂等结果跳过撤销或作用域校验。
type Source interface {
	Verify(context.Context, foundation.Scope, []byte, string) (model.Event, error)
}

type DailyRepository interface {
	Closed(context.Context, db.Executor, foundation.Scope, string, string) (bool, error)
	Manifest(context.Context, db.Executor, foundation.Scope, string, time.Time, time.Time) (uint64, string, error)
	SaveWatermark(context.Context, db.Executor, foundation.Scope, model.Watermark, time.Time) error
	Watermarks(context.Context, db.Executor, foundation.Scope, string) ([]model.Watermark, error)
	PendingDays(context.Context, db.Executor, foundation.Scope) ([]string, error)
	Daily(context.Context, db.Executor, foundation.Scope, string) (model.Daily, error)
	SaveDaily(context.Context, db.Executor, foundation.Scope, model.Daily, time.Time) error
}
type WatermarkSource interface {
	VerifyWatermark(context.Context, foundation.Scope, []byte, string) (model.Watermark, error)
}

type Records interface {
	Records(context.Context, db.Executor, foundation.Scope, model.RecordFilter, bool) (model.RecordPage, error)
}

type Relations interface {
	Close(context.Context, db.Executor, foundation.Scope, time.Time) ([]affiliate.Registration, error)
	Attributions(context.Context, db.Executor, foundation.Scope, []affiliate.Fact, time.Time, time.Time) ([]affiliate.Attribution, error)
}
