package adapters

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	"github.com/ex-business/ExCommission/internal/platform/ports"
	"github.com/ex-business/ExCommission/internal/workflow/domain"
	"time"
)

type Repository struct{}

func Hash(body []byte) string {
	canonical, err := foundation.BusinessCanonicalJSON(body)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}
func (Repository) Create(ctx context.Context, tx ports.Executor, s foundation.Scope, r domain.Request, reason string, at time.Time) error {
	if !s.Valid() || r.ID == "" || r.Applicant == "" || r.ObjectRef == "" || r.ObjectHash == "" || r.ObjectHash != Hash(r.Payload) || at.IsZero() {
		return foundation.ErrValidation
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO workflow_requests(tenant_id,app_id,approval_id,request_type,applicant_id,object_ref,object_hash,payload,revision,status,execution_status,decision_by,reason,created_at,decided_at) VALUES(?,?,?,?,?,?,?,?,0,'PENDING','PENDING',NULL,?,?,NULL)`, s.Tenant, s.App, r.ID, r.Type, r.Applicant, r.ObjectRef, r.ObjectHash, r.Payload, reason, at.UTC())
	return err
}
func (Repository) Get(ctx context.Context, tx ports.Executor, s foundation.Scope, id string, lock bool) (domain.Request, error) {
	statement := `SELECT approval_id,request_type,applicant_id,object_ref,object_hash,payload,revision,status,execution_status FROM workflow_requests WHERE tenant_id=? AND app_id=? AND approval_id=?`
	if lock {
		statement += " FOR UPDATE"
	}
	var r domain.Request
	err := tx.QueryRowContext(ctx, statement, s.Tenant, s.App, id).Scan(&r.ID, &r.Type, &r.Applicant, &r.ObjectRef, &r.ObjectHash, &r.Payload, &r.Revision, &r.Status, &r.Execution)
	if errors.Is(err, sql.ErrNoRows) {
		return r, errors.New("NOT_FOUND")
	}
	return r, err
}
func (Repository) Finish(ctx context.Context, tx ports.Executor, s foundation.Scope, id, subject, status, execution, reason string, at time.Time) error {
	_, err := tx.ExecContext(ctx, `UPDATE workflow_requests SET status=?,execution_status=?,decision_by=?,decision_reason=?,decided_at=?,revision=revision+1 WHERE tenant_id=? AND app_id=? AND approval_id=?`, status, execution, subject, reason, at.UTC(), s.Tenant, s.App, id)
	return err
}
