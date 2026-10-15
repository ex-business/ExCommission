package adapters

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/ex-business/ExCommission/internal/platform/domain"
	"github.com/ex-business/ExCommission/internal/platform/ports"
	user "github.com/ex-business/ExCommission/internal/users/domain"
	userports "github.com/ex-business/ExCommission/internal/users/ports"
	"time"
)

func Upsert(ctx context.Context, tx ports.Executor, scope domain.Scope, source string, record user.Record, at time.Time) (string, error) {
	if !scope.Valid() || !domain.ValidID(source) || !domain.ValidID(record.ExternalID) || !domain.ValidID(record.DisplayName) {
		return "", domain.ErrValidation
	}
	identity, _ := json.Marshal([]string{source, record.ExternalID})
	sum := sha256.Sum256(identity)
	id := "usr_" + hex.EncodeToString(sum[:16])
	_, err := tx.ExecContext(ctx, `INSERT INTO users_external VALUES(?,?,?,?,?,?,?) ON DUPLICATE KEY UPDATE display_name=VALUES(display_name)`, scope.Tenant, scope.App, id, source, record.ExternalID, record.DisplayName, at.UTC())
	return id, err
}
func Exists(ctx context.Context, tx ports.Executor, scope domain.Scope, id string) error {
	var found int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM users_external WHERE tenant_id=? AND app_id=? AND user_id=?`, scope.Tenant, scope.App, id).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("USER_NOT_FOUND")
	}
	if err != nil {
		return err
	}
	return nil
}

type Repository struct{}

func (Repository) Upsert(ctx context.Context, tx ports.Executor, s domain.Scope, source string, record user.Record, at time.Time) (string, error) {
	return Upsert(ctx, tx, s, source, record, at)
}
func (Repository) Exists(ctx context.Context, tx ports.Executor, s domain.Scope, id string) error {
	return Exists(ctx, tx, s, id)
}
func (Repository) SaveJob(ctx context.Context, tx ports.Executor, s domain.Scope, job userports.Job, source, mapping string, at time.Time) error {
	errorsBody, err := json.Marshal(job.ErrorRefs)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO user_sync_jobs VALUES(?,?,?,?,?,?,?,?,?,?,?)`, s.Tenant, s.App, job.ID, source, mapping, job.Status, job.Watermark, job.Success, job.Failure, errorsBody, at.UTC())
	return err
}
func (Repository) GetJob(ctx context.Context, tx ports.Executor, s domain.Scope, id string) (userports.Job, error) {
	var job userports.Job
	var body []byte
	err := tx.QueryRowContext(ctx, `SELECT job_id,status,watermark,success_count,failure_count,error_refs FROM user_sync_jobs WHERE tenant_id=? AND app_id=? AND job_id=?`, s.Tenant, s.App, id).Scan(&job.ID, &job.Status, &job.Watermark, &job.Success, &job.Failure, &body)
	if err != nil {
		return job, err
	}
	err = json.Unmarshal(body, &job.ErrorRefs)
	return job, err
}
