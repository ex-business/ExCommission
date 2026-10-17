package adapters

import (
	"context"
	"errors"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
)

type Repository struct{}

func (Repository) Attempt(ctx context.Context, tx db.Executor, s foundation.Scope, subject string, window int64) (int, error) {
	// Only the current minute is needed. Retain at most one limiter row per configured subject.
	if _, err := tx.ExecContext(ctx, `DELETE FROM security_totp_attempts WHERE tenant_id=? AND app_id=? AND subject_id=? AND minute_window<?`, s.Tenant, s.App, subject, window); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO security_totp_attempts VALUES(?,?,?,?,1) ON DUPLICATE KEY UPDATE attempts=attempts+1`, s.Tenant, s.App, subject, window); err != nil {
		return 0, err
	}
	var n int
	err := tx.QueryRowContext(ctx, `SELECT attempts FROM security_totp_attempts WHERE tenant_id=? AND app_id=? AND subject_id=? AND minute_window=? FOR UPDATE`, s.Tenant, s.App, subject, window).Scan(&n)
	return n, err
}
func (Repository) Consume(ctx context.Context, tx db.Executor, s foundation.Scope, subject string, counter uint64, hash string) error {
	// Keep a monotonic watermark, so even a backward clock jump cannot make an old code reusable.
	if _, err := tx.ExecContext(ctx, `INSERT IGNORE INTO security_totp_uses VALUES(?,?,?,?,?)`, s.Tenant, s.App, subject, counter, hash); err != nil {
		return err
	}
	var old string
	var previous uint64
	if err := tx.QueryRowContext(ctx, `SELECT time_counter,action_hash FROM security_totp_uses WHERE tenant_id=? AND app_id=? AND subject_id=? FOR UPDATE`, s.Tenant, s.App, subject).Scan(&previous, &old); err != nil {
		return err
	}
	if counter < previous || (counter == previous && old != hash) {
		return errors.New("TOTP_REPLAY")
	}
	if counter == previous {
		return nil
	}
	_, err := tx.ExecContext(ctx, `UPDATE security_totp_uses SET time_counter=?,action_hash=? WHERE tenant_id=? AND app_id=? AND subject_id=?`, counter, hash, s.Tenant, s.App, subject)
	return err
}
