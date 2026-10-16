package application

import (
	"context"
	"errors"
	model "github.com/ex-business/ExCommission/internal/disbursement/domain"
	identity "github.com/ex-business/ExCommission/internal/identity/domain"
	response "github.com/ex-business/ExCommission/internal/platform/application"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
)

type Claim struct {
	Revision uint64 `json:"expected_revision"`
}

// Claim verifies ownership before command replay and again under the instruction lock.
func (s Service) Claim(ctx context.Context, p identity.Principal, id string, in Claim, key, rid string) (foundation.CommandResult, error) {
	scope := foundation.Scope{Tenant: p.Tenant, App: p.App}
	if s.AuthorizeClaim == nil {
		return foundation.CommandResult{}, identity.ErrPermission
	}
	if err := s.AuthorizeClaim(ctx, s.Query, p); err != nil {
		return foundation.CommandResult{}, err
	}
	check := func(tx db.Executor, lock bool) (model.Instruction, error) {
		i, err := s.Repo.Get(ctx, tx, scope, id, lock)
		if err != nil {
			return i, err
		}
		if p.MemberUserID == "" || i.DeliveryMode != "MEMBER_CLAIM" || i.BeneficiaryUserID != p.MemberUserID {
			return i, errors.New("NOT_FOUND")
		}
		owner, err := s.Beneficiary(ctx, tx, scope, i.StatementID)
		if err != nil {
			return i, err
		}
		if owner != p.MemberUserID {
			return i, errors.New("NOT_FOUND")
		}
		return i, nil
	}
	if _, err := check(s.Query, false); err != nil {
		return foundation.CommandResult{}, err
	}
	payload := struct {
		ID, Subject string
		Input       Claim
	}{id, p.Subject, in}
	ctx = foundation.WithRequest(ctx, rid, p.Subject)
	return s.Commands.Execute(ctx, scope, "member-commission-claim", key, payload, func(tx db.Executor) (foundation.CommandResult, error) {
		if err := s.AuthorizeClaim(ctx, tx, p); err != nil {
			return foundation.CommandResult{}, err
		}
		i, err := check(tx, true)
		if err != nil {
			return foundation.CommandResult{}, err
		}
		// Once accepted the original instruction is the single retry resource, regardless of key.
		if i.Status == "QUEUED" || i.Status == "LINKED" {
			return response.Result(202, i.ID, rid, map[string]string{"instruction_id": i.ID, "claim_status": "PROCESSING"})
		}
		if i.Revision != in.Revision {
			return foundation.CommandResult{}, errors.New("REVISION_CONFLICT")
		}
		if i.Status != "CLAIMABLE" {
			return foundation.CommandResult{}, errors.New("STATE_CONFLICT")
		}
		if err = s.Validate(ctx, tx, scope, i); err != nil {
			return foundation.CommandResult{}, err
		}
		previous := i.Revision
		i.Status = "QUEUED"
		i.Revision++
		if err = s.Repo.Update(ctx, tx, scope, i, previous); err != nil {
			return foundation.CommandResult{}, err
		}
		return response.Result(202, i.ID, rid, map[string]string{"instruction_id": i.ID, "claim_status": "PROCESSING"})
	})
}
