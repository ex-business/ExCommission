package adapters

import (
	"context"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	model "github.com/ex-business/ExCommission/internal/qualification/domain"
	registrations "github.com/ex-business/ExCommission/internal/registration/adapters"
)

type Members struct{}

// Registered 通过注册模块的只读边界取首次邀请归属，不直接查询或改写其他业务的表。
func (Members) Registered(ctx context.Context, tx db.Executor, s foundation.Scope, source, external string) (model.Member, error) {
	r, _, err := (registrations.Repository{}).Receipt(ctx, tx, s, source, external)
	return model.Member{UserID: r.UserID, AgentID: r.AgentID, ParentID: r.ParentID, RegisteredAt: r.RegisteredAt}, err
}
