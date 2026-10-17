package adapters

import (
	"context"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
)

func (Repository) HasRequest(ctx context.Context, tx db.Executor, s foundation.Scope, kind, ref string) (bool, error) {
	var count int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM workflow_requests WHERE tenant_id=? AND app_id=? AND request_type=? AND object_ref=?`, s.Tenant, s.App, kind, ref).Scan(&count)
	return count > 0, err
}
