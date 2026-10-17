// Package ports 隔离注册来源、用户身份和代理关系，便于增加站点或消息适配器。
package ports

import (
	"context"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	model "github.com/ex-business/ExCommission/internal/registration/domain"
	user "github.com/ex-business/ExCommission/internal/users/domain"
	"time"
)

// Source 由服务端按租户/应用装配；浏览器不能自行声明一个可信来源。
type Source interface {
	Verify(context.Context, foundation.Scope, []byte, string) (model.Event, error)
}
type Users interface {
	Upsert(context.Context, db.Executor, foundation.Scope, string, user.Record, time.Time) (string, error)
}
type Agents interface {
	LockRegistration(context.Context, db.Executor, foundation.Scope) error
	RegisterInitial(context.Context, db.Executor, foundation.Scope, string, string, string, *string, time.Time) error
	Active(context.Context, db.Executor, foundation.Scope, string, time.Time) error
}
type Repository interface {
	StableInvitation(context.Context, db.Executor, foundation.Scope, string, string, time.Time) (model.Invitation, error)
	ClaimEvent(context.Context, db.Executor, foundation.Scope, string, string, string) error
	Invitation(context.Context, db.Executor, foundation.Scope, string, string) (model.Invitation, error)
	SaveInvitation(context.Context, db.Executor, foundation.Scope, model.Invitation, time.Time) error
	Receipt(context.Context, db.Executor, foundation.Scope, string, string) (model.Receipt, string, error)
	SaveReceipt(context.Context, db.Executor, foundation.Scope, model.Receipt, string) error
}
