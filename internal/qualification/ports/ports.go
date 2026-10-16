// Package ports 隔离达标规则、事实来源、注册事实和持久化，便于接入不同站点及充值/投注系统。
package ports

import (
	"context"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	model "github.com/ex-business/ExCommission/internal/qualification/domain"
	"time"
)

type Source interface {
	Verify(context.Context, foundation.Scope, []byte, string) (model.Fact, error)
}
type Members interface {
	Registered(context.Context, db.Executor, foundation.Scope, string, string) (model.Member, error)
}
type Reader interface {
	Status(context.Context, db.Executor, foundation.Scope, string, string, string) (model.Status, error)
}
type Repository interface {
	Reader
	Policy(context.Context, db.Executor, foundation.Scope, string) (model.Policy, error)
	SavePolicy(context.Context, db.Executor, foundation.Scope, model.Policy, time.Time) error
	LockMember(context.Context, db.Executor, foundation.Scope, string, string, string) error
	ClaimFact(context.Context, db.Executor, foundation.Scope, model.Fact, string) (bool, error)
	Facts(context.Context, db.Executor, foundation.Scope, string, string, string) ([]model.Fact, error)
	SaveStatus(context.Context, db.Executor, foundation.Scope, model.Status, time.Time) error
}

// StatisticsReader 由有效会员模块提供人数筛选；其他模块不得自行重新定义达标时间口径。
type StatisticsReader interface {
	EffectiveStatisticsAt(context.Context, db.Executor, foundation.Scope, string, string, time.Time) (model.StatisticsResult, error)
}
