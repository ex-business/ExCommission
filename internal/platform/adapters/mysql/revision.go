package mysql

import (
	"context"
	"errors"
	"github.com/ex-business/ExCommission/internal/platform/ports"
)

var ErrRevision = errors.New("REVISION_CONFLICT")

// AdvanceRevision 是事务协调元数据；领域对象仍由各所有者表保存。
func AdvanceRevision(ctx context.Context, tx ports.Executor, scope Scope, kind, id string, expected uint64) error {
	if !scope.Valid() || !validID(kind) || !validID(id) || expected == ^uint64(0) {
		return ErrValidation
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO platform_revisions VALUES(?,?,?,?,0) ON DUPLICATE KEY UPDATE resource_id=resource_id`, scope.Tenant, scope.App, kind, id); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE platform_revisions SET revision=revision+1 WHERE tenant_id=? AND app_id=? AND resource_type=? AND resource_id=? AND revision=?`, scope.Tenant, scope.App, kind, id, expected)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrRevision
	}
	return nil
}
