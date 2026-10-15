package adapters

import (
	"context"
	"database/sql"
	"errors"
	"github.com/ex-business/ExCommission/internal/identity/domain"
	"github.com/ex-business/ExCommission/internal/platform/ports"
)

// Authorize 每次从持久化授权读取；令牌不携带可自行声明的授权。
func Authorize(ctx context.Context, tx ports.Executor, p domain.Principal, tenant, app, permission string) error {
	if p.Subject == "" || p.Tenant != tenant || p.App != app {
		return domain.ErrPermission
	}
	var allowed int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM identity_grants WHERE tenant_id=? AND app_id=? AND subject_id=? AND permission=?`, tenant, app, p.Subject, permission).Scan(&allowed)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ErrPermission
	}
	return err
}

type DatabaseAuthorizer struct{ Query ports.Executor }

func (a DatabaseAuthorizer) Authorize(ctx context.Context, p domain.Principal, tenant, app, permission string) error {
	return Authorize(ctx, a.Query, p, tenant, app, permission)
}
