package application

import (
	"context"
	"encoding/json"
	"errors"
	identity "github.com/ex-business/ExCommission/internal/identity/domain"
	response "github.com/ex-business/ExCommission/internal/platform/application"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	model "github.com/ex-business/ExCommission/internal/risk/domain"
	"github.com/ex-business/ExCommission/internal/risk/ports"
	hash "github.com/ex-business/ExCommission/internal/workflow/adapters"
	approval "github.com/ex-business/ExCommission/internal/workflow/domain"
	workflow "github.com/ex-business/ExCommission/internal/workflow/ports"
	"time"
)

type HoldInput struct {
	Entitlement string `json:"entitlement_id"`
	Reason      string `json:"reason_code"`
	Evidence    string `json:"evidence_ref"`
}
type ReleaseInput struct {
	Revision uint64 `json:"expected_revision"`
	Reason   string `json:"reason"`
	Evidence string `json:"evidence_ref"`
}
type Service struct {
	Commands db.Commands
	Query    db.Executor
	Repo     ports.Repository
	Ledger   ports.Ledger
	Accounts ports.Accounts
	Workflow workflow.Repository
	Now      func() time.Time
	ID       func() string
}

func (s Service) Hold(ctx context.Context, p identity.Principal, input HoldInput, key, request string) (foundation.CommandResult, error) {
	if !foundation.ValidID(input.Reason) || !foundation.ValidID(input.Evidence) {
		return foundation.CommandResult{}, foundation.ErrValidation
	}
	scope := foundation.Scope{Tenant: p.Tenant, App: p.App}
	return s.Commands.Execute(ctx, scope, "risk-hold", key, input, func(tx db.Executor) (foundation.CommandResult, error) {
		v := model.Hold{ID: s.ID(), EntitlementID: input.Entitlement, Reason: input.Reason, Evidence: input.Evidence}
		if err := s.Repo.Create(ctx, tx, scope, v, s.Now()); err != nil {
			return foundation.CommandResult{}, err
		}
		if err := s.Ledger.Hold(ctx, tx, scope, input.Entitlement, v.ID, input.Reason, s.Now()); err != nil {
			return foundation.CommandResult{}, err
		}
		return response.Result(201, v.ID, request, map[string]any{"risk_id": v.ID, "revision": 0, "status": "OPEN"})
	})
}
func (s Service) ensure(ctx context.Context, tx db.Executor, scope foundation.Scope, id string) (model.Hold, error) {
	v, err := s.Repo.Get(ctx, tx, scope, id, true)
	if err == nil {
		return v, nil
	}
	if err.Error() != "NOT_FOUND" {
		return v, err
	}
	rows, err := s.Ledger.Restrictions(ctx, tx, scope, id)
	if err != nil {
		return v, err
	}
	if len(rows) == 0 {
		return v, errors.New("NOT_FOUND")
	}
	v = model.Hold{ID: id, EntitlementID: rows[0].EntitlementID, Reason: rows[0].Reason, Evidence: "ledger-generated", Status: "OPEN"}
	if err = s.Repo.Create(ctx, tx, scope, v, s.Now()); err != nil {
		return v, err
	}
	return v, nil
}
func (s Service) ReleaseRequest(ctx context.Context, p identity.Principal, id string, input ReleaseInput, key, request string) (foundation.CommandResult, error) {
	if !foundation.ValidID(input.Reason) || !foundation.ValidID(input.Evidence) {
		return foundation.CommandResult{}, foundation.ErrValidation
	}
	scope := foundation.Scope{Tenant: p.Tenant, App: p.App}
	payload := struct {
		RiskID string `json:"risk_id"`
		ReleaseInput
	}{id, input}
	return s.Commands.Execute(ctx, scope, "risk-release", key, payload, func(tx db.Executor) (foundation.CommandResult, error) {
		v, err := s.ensure(ctx, tx, scope, id)
		if err != nil {
			return foundation.CommandResult{}, err
		}
		if v.Revision != input.Revision || v.Status != "OPEN" {
			return foundation.CommandResult{}, errors.New("STALE_APPROVAL")
		}
		if v.Reason == "REFUND_AWAITING_PAYMENT" {
			return foundation.CommandResult{}, errors.New("STATE_CONFLICT")
		}
		if err = s.Repo.State(ctx, tx, scope, v, "PENDING_RELEASE"); err != nil {
			return foundation.CommandResult{}, err
		}
		body, _ := json.Marshal(map[string]any{"risk_id": id, "revision": v.Revision + 1, "evidence_ref": input.Evidence})
		a := approval.Request{ID: s.ID(), Type: "RISK_RELEASE", Applicant: p.Subject, ObjectRef: id, ObjectHash: hash.Hash(body), Payload: body}
		if err = s.Workflow.Create(ctx, tx, scope, a, input.Reason, s.Now()); err != nil {
			return foundation.CommandResult{}, err
		}
		return response.Result(202, a.ID, request, map[string]any{"approval_id": a.ID, "object_hash": a.ObjectHash, "revision": 0})
	})
}
func (s Service) ExecuteRelease(ctx context.Context, tx db.Executor, scope foundation.Scope, a approval.Request, now time.Time) error {
	var input struct {
		ID       string `json:"risk_id"`
		Revision uint64 `json:"revision"`
	}
	if err := json.Unmarshal(a.Payload, &input); err != nil {
		return err
	}
	v, err := s.Repo.Get(ctx, tx, scope, input.ID, true)
	if err != nil {
		return err
	}
	if v.Revision != input.Revision || v.Status != "PENDING_RELEASE" {
		return errors.New("STALE_APPROVAL")
	}
	restrictions, err := s.Ledger.Restrictions(ctx, tx, scope, input.ID)
	if err != nil {
		return err
	}
	for _, r := range restrictions {
		if r.Closed != nil {
			continue
		}
		if err = s.Ledger.CloseHold(ctx, tx, scope, r.EntitlementID, input.ID, now); err != nil {
			return err
		}
		if err = s.Ledger.Requeue(ctx, tx, scope, r.EntitlementID, "risk-release-"+a.ID, now); err != nil {
			return err
		}
	}
	return s.Repo.State(ctx, tx, scope, v, "RELEASED")
}
func (s Service) RejectRelease(ctx context.Context, tx db.Executor, scope foundation.Scope, a approval.Request, now time.Time) error {
	v, err := s.Repo.Get(ctx, tx, scope, a.ObjectRef, true)
	if err != nil {
		return err
	}
	if v.Status != "PENDING_RELEASE" {
		return errors.New("STATE_CONFLICT")
	}
	return s.Repo.State(ctx, tx, scope, v, "OPEN")
}
func (s Service) Appeal(ctx context.Context, p identity.Principal, id, reason, evidence, key, request string) (foundation.CommandResult, error) {
	if !foundation.ValidID(reason) || !foundation.ValidID(evidence) {
		return foundation.CommandResult{}, foundation.ErrValidation
	}
	scope := foundation.Scope{Tenant: p.Tenant, App: p.App}
	input := map[string]string{"risk_id": id, "reason": reason, "evidence_ref": evidence}
	return s.Commands.Execute(ctx, scope, "risk-appeal", key, input, func(tx db.Executor) (foundation.CommandResult, error) {
		if _, err := s.ensure(ctx, tx, scope, id); err != nil {
			return foundation.CommandResult{}, err
		}
		appealID := s.ID()
		if err := s.Repo.Appeal(ctx, tx, scope, appealID, id, p.Subject, reason, evidence, s.Now()); err != nil {
			return foundation.CommandResult{}, err
		}
		return response.Result(201, appealID, request, map[string]string{"appeal_id": appealID, "risk_id": id})
	})
}
