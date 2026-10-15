package application

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	identity "github.com/ex-business/ExCommission/internal/identity/domain"
	response "github.com/ex-business/ExCommission/internal/platform/application"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
)

type RecoveryInput struct {
	Revision uint64 `json:"expected_revision"`
	Hash     string `json:"object_hash"`
	Reason   string `json:"reason"`
	Evidence string `json:"evidence_ref"`
}

func (s Service) RetryExecution(ctx context.Context, p identity.Principal, id string, input RecoveryInput, key, request string) (foundation.CommandResult, error) {
	if !foundation.ValidID(input.Reason) || !foundation.ValidID(input.Evidence) {
		return foundation.CommandResult{}, foundation.ErrValidation
	}
	scope := foundation.Scope{Tenant: p.Tenant, App: p.App}
	payload := struct {
		ID string `json:"approval_id"`
		RecoveryInput
	}{id, input}
	return s.Commands.Execute(ctx, scope, "approval-execution-retry", key, payload, func(tx db.Executor) (foundation.CommandResult, error) {
		a, err := s.Repo.Get(ctx, tx, scope, id, true)
		if err != nil {
			return foundation.CommandResult{}, err
		}
		if p.Subject == a.Applicant {
			return foundation.CommandResult{}, errors.New("SELF_APPROVAL_FORBIDDEN")
		}
		if a.Revision != input.Revision || a.ObjectHash != input.Hash {
			return foundation.CommandResult{}, errors.New("STALE_APPROVAL")
		}
		if a.Status != "APPROVED" || a.Execution != "FAILED" {
			return foundation.CommandResult{}, errors.New("STATE_CONFLICT")
		}
		if s.Authorize != nil {
			if err = s.Authorize(ctx, tx, p, ApprovalPermission(a.Type)); err != nil {
				return foundation.CommandResult{}, err
			}
		}
		execute, ok := s.Executors[a.Type]
		if !ok {
			return foundation.CommandResult{}, errors.New("UNSUPPORTED_CAPABILITY")
		}
		if _, err = tx.ExecContext(ctx, "SAVEPOINT execution_retry"); err != nil {
			return foundation.CommandResult{}, err
		}
		err = execute(ctx, tx, scope, a, s.Now())
		status, execution := "APPROVED", "SUCCEEDED"
		if err != nil {
			if _, rollbackErr := tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT execution_retry"); rollbackErr != nil {
				return foundation.CommandResult{}, rollbackErr
			}
			execution = "FAILED"
			if err.Error() == "STALE_APPROVAL" {
				status = "STALE"
			}
		}
		random := make([]byte, 16)
		if _, err = rand.Read(random); err != nil {
			return foundation.CommandResult{}, err
		}
		if err = s.Repo.Execution(ctx, tx, scope, id, a.Revision, status, execution, p.Subject, input.Reason, input.Evidence, hex.EncodeToString(random), s.Now()); err != nil {
			return foundation.CommandResult{}, err
		}
		return response.Result(200, id, request, map[string]any{"approval_id": id, "approval_status": status, "execution_status": execution, "revision": a.Revision + 1})
	})
}
func (s Service) Cancel(ctx context.Context, p identity.Principal, id string, input Decision, key, request string) (foundation.CommandResult, error) {
	if !foundation.ValidID(input.Reason) {
		return foundation.CommandResult{}, foundation.ErrValidation
	}
	scope := foundation.Scope{Tenant: p.Tenant, App: p.App}
	payload := struct {
		ID string `json:"approval_id"`
		Decision
	}{id, input}
	return s.Commands.Execute(ctx, scope, "approval-cancel", key, payload, func(tx db.Executor) (foundation.CommandResult, error) {
		a, err := s.Repo.Get(ctx, tx, scope, id, true)
		if err != nil {
			return foundation.CommandResult{}, err
		}
		if a.Applicant != p.Subject {
			return foundation.CommandResult{}, errors.New("PERMISSION_DENIED")
		}
		if a.Revision != input.Revision || a.ObjectHash != input.ObjectHash {
			return foundation.CommandResult{}, errors.New("STALE_APPROVAL")
		}
		if a.Status != "PENDING" {
			return foundation.CommandResult{}, errors.New("STATE_CONFLICT")
		}
		if reject, ok := s.Rejections[a.Type]; ok {
			if err = reject(ctx, tx, scope, a, s.Now()); err != nil {
				return foundation.CommandResult{}, err
			}
		}
		if err = s.Repo.Finish(ctx, tx, scope, id, p.Subject, "CANCELLED", "PENDING", input.Reason, s.Now()); err != nil {
			return foundation.CommandResult{}, err
		}
		return response.Result(200, id, request, map[string]any{"approval_id": id, "approval_status": "CANCELLED", "execution_status": "PENDING"})
	})
}
