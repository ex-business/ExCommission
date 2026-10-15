package adapters

import (
	"context"
	"github.com/ex-business/ExCommission/internal/platform/ports"
)

// BootstrapOnce never restores a grant that was revoked after initial setup.
func BootstrapOnce(ctx context.Context, tx ports.Executor, tenant, app, marker, subject string, permissions []string) error {
	res, err := tx.ExecContext(ctx, `INSERT IGNORE INTO identity_bootstraps VALUES(?,?,?,UTC_TIMESTAMP(6))`, tenant, app, marker)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil || n == 0 {
		return err
	}
	for _, permission := range permissions {
		if _, err = tx.ExecContext(ctx, `INSERT INTO identity_grants VALUES(?,?,?,?)`, tenant, app, subject, permission); err != nil {
			return err
		}
	}
	return nil
}
