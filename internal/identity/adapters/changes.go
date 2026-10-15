package adapters

import (
	"context"
	"database/sql"
	"errors"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
)

func ChangeGrants(ctx context.Context, tx db.Executor, s foundation.Scope, subject string, revision uint64, grant, revoke []string) error {
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM identity_bootstraps WHERE tenant_id=? AND app_id=? AND subject_id=?`, s.Tenant, s.App, subject).Scan(&n); err != nil {
		return err
	}
	if n != 1 {
		return errors.New("NOT_FOUND")
	}
	if _, err := tx.ExecContext(ctx, `INSERT IGNORE INTO identity_subject_revisions VALUES(?,?,?,0)`, s.Tenant, s.App, subject); err != nil {
		return err
	}
	var actual uint64
	if err := tx.QueryRowContext(ctx, `SELECT revision FROM identity_subject_revisions WHERE tenant_id=? AND app_id=? AND subject_id=? FOR UPDATE`, s.Tenant, s.App, subject).Scan(&actual); err != nil {
		return err
	}
	if actual != revision {
		return errors.New("STALE_APPROVAL")
	}
	for _, permission := range revoke {
		if _, err := tx.ExecContext(ctx, `DELETE FROM identity_grants WHERE tenant_id=? AND app_id=? AND subject_id=? AND permission=?`, s.Tenant, s.App, subject, permission); err != nil {
			return err
		}
	}
	for _, permission := range grant {
		if _, err := tx.ExecContext(ctx, `INSERT IGNORE INTO identity_grants VALUES(?,?,?,?)`, s.Tenant, s.App, subject, permission); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, `UPDATE identity_subject_revisions SET revision=revision+1 WHERE tenant_id=? AND app_id=? AND subject_id=?`, s.Tenant, s.App, subject)
	return err
}
func SubjectGrants(ctx context.Context, tx db.Executor, s foundation.Scope, subject string) ([]string, uint64, error) {
	var revision uint64
	err := tx.QueryRowContext(ctx, `SELECT revision FROM identity_subject_revisions WHERE tenant_id=? AND app_id=? AND subject_id=?`, s.Tenant, s.App, subject).Scan(&revision)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, 0, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT permission FROM identity_grants WHERE tenant_id=? AND app_id=? AND subject_id=? ORDER BY permission`, s.Tenant, s.App, subject)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var permission string
		if err = rows.Scan(&permission); err != nil {
			return nil, 0, err
		}
		out = append(out, permission)
	}
	return out, revision, rows.Err()
}
