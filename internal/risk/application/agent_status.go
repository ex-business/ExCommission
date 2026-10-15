package application

import (
	"context"
	"encoding/json"
	"errors"
	identity "github.com/ex-business/ExCommission/internal/identity/domain"
	response "github.com/ex-business/ExCommission/internal/platform/application"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	hash "github.com/ex-business/ExCommission/internal/workflow/adapters"
	approval "github.com/ex-business/ExCommission/internal/workflow/domain"
	"time"
)

type StatusInput struct {
	AgentID  string `json:"agent_id"`
	Status   string `json:"status"`
	Revision uint64 `json:"expected_revision"`
	Reason   string `json:"reason"`
	Evidence string `json:"evidence_ref"`
}

func (s Service) StatusRequest(ctx context.Context, p identity.Principal, input StatusInput, key, request string) (foundation.CommandResult, error) {
	if !foundation.ValidID(input.AgentID) || !foundation.ValidID(input.Evidence) || !foundation.ValidID(input.Reason) || (input.Status != "ACTIVE" && input.Status != "FROZEN" && input.Status != "DISABLED" && input.Status != "CLOSED") {
		return foundation.CommandResult{}, foundation.ErrValidation
	}
	scope := foundation.Scope{Tenant: p.Tenant, App: p.App}
	return s.Commands.Execute(ctx, scope, "agent-status-request", key, input, func(tx db.Executor) (foundation.CommandResult, error) {
		revision, err := s.Accounts.Revision(ctx, tx, scope, input.AgentID)
		if err != nil {
			return foundation.CommandResult{}, err
		}
		if revision != input.Revision {
			return foundation.CommandResult{}, errors.New("REVISION_CONFLICT")
		}
		body, _ := json.Marshal(input)
		a := approval.Request{ID: s.ID(), Type: "AGENT_STATUS", Applicant: p.Subject, ObjectRef: input.AgentID, ObjectHash: hash.Hash(body), Payload: body}
		if err = s.Workflow.Create(ctx, tx, scope, a, input.Reason, s.Now()); err != nil {
			return foundation.CommandResult{}, err
		}
		return response.Result(202, a.ID, request, map[string]any{"approval_id": a.ID, "object_hash": a.ObjectHash, "revision": 0})
	})
}
func (s Service) ExecuteStatus(ctx context.Context, tx db.Executor, scope foundation.Scope, a approval.Request, now time.Time) error {
	var input StatusInput
	if err := json.Unmarshal(a.Payload, &input); err != nil {
		return err
	}
	if err := s.Accounts.SetStatus(ctx, tx, scope, input.AgentID, input.Status, input.Revision, input.Evidence, now); err != nil {
		return err
	}
	funds, err := s.Ledger.ForAgent(ctx, tx, scope, input.AgentID)
	if err != nil {
		return err
	}
	if input.Status == "CLOSED" && len(funds) > 0 {
		return errors.New("STATE_CONFLICT")
	}
	if input.Status == "FROZEN" {
		for _, e := range funds {
			if err = s.Ledger.Hold(ctx, tx, scope, e.ID, "agent-freeze-"+a.ID, "AGENT_FROZEN", now); err != nil {
				return err
			}
			if err = s.Ledger.Requeue(ctx, tx, scope, e.ID, "agent-freeze-"+a.ID, now); err != nil {
				return err
			}
		}
	}
	return nil
}
