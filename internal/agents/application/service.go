package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/ex-business/ExCommission/internal/agents/domain"
	"github.com/ex-business/ExCommission/internal/agents/ports"
	identity "github.com/ex-business/ExCommission/internal/identity/domain"
	result "github.com/ex-business/ExCommission/internal/platform/application"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	users "github.com/ex-business/ExCommission/internal/users/ports"
	workflow "github.com/ex-business/ExCommission/internal/workflow/domain"
	approvals "github.com/ex-business/ExCommission/internal/workflow/ports"
	"time"
)

type CreateRequest struct {
	UserID      string `json:"user_id"`
	DisplayName string `json:"display_name"`
}
type Service struct {
	Commands db.Commands
	Repo     ports.Repository
	Users    users.UserLookup
	Workflow approvals.Repository
	Query    db.Executor
	Now      func() time.Time
	ID       func() string
}

func (s Service) Create(ctx context.Context, p identity.Principal, input CreateRequest, key, request string) (foundation.CommandResult, error) {
	ctx = foundation.WithRequest(ctx, request, p.Subject)

	if !foundation.ValidID(input.UserID) || !foundation.ValidID(input.DisplayName) {
		return foundation.CommandResult{}, foundation.ErrValidation
	}
	scope := foundation.Scope{Tenant: p.Tenant, App: p.App}
	return s.Commands.Execute(ctx, scope, "agent-create", key, input, func(tx db.Executor) (foundation.CommandResult, error) {
		if err := s.Users.Exists(ctx, tx, scope, input.UserID); err != nil {
			return foundation.CommandResult{}, err
		}
		id := s.ID()
		if err := s.Repo.Create(ctx, tx, scope, id, input.UserID, input.DisplayName, s.Now()); err != nil {
			return foundation.CommandResult{}, err
		}
		return result.Result(200, id, request, map[string]any{"agent_id": id, "revision": 0, "status": "ACTIVE"})
	})
}
func (s Service) RequestMove(ctx context.Context, p identity.Principal, input domain.RelationChange, key, request string) (foundation.CommandResult, error) {
	ctx = foundation.WithRequest(ctx, request, p.Subject)

	if !foundation.ValidID(input.AgentID) || !foundation.ValidID(input.Reason) || (input.NewParentID != nil && !foundation.ValidID(*input.NewParentID)) {
		return foundation.CommandResult{}, foundation.ErrValidation
	}
	scope := foundation.Scope{Tenant: p.Tenant, App: p.App}
	return s.Commands.Execute(ctx, scope, "relationship-request", key, input, func(tx db.Executor) (foundation.CommandResult, error) {
		revision, err := s.Repo.Revision(ctx, tx, scope, input.AgentID)
		if err != nil {
			return foundation.CommandResult{}, err
		}
		if revision != input.ExpectedRevision {
			return foundation.CommandResult{}, domain.ErrStale
		}
		if input.NewParentID != nil {
			if _, err = s.Repo.Revision(ctx, tx, scope, *input.NewParentID); err != nil {
				return foundation.CommandResult{}, err
			}
		}
		body, err := json.Marshal(input)
		if err != nil {
			return foundation.CommandResult{}, err
		}
		body, err = foundation.CanonicalJSON(body)
		if err != nil {
			return foundation.CommandResult{}, err
		}
		hashBody, err := foundation.BusinessCanonicalJSON(body)
		if err != nil {
			return foundation.CommandResult{}, err
		}
		sum := sha256.Sum256(hashBody)
		id := s.ID()
		approval := workflow.Request{ID: id, Type: "RELATIONSHIP", Applicant: p.Subject, ObjectRef: input.AgentID, ObjectHash: hex.EncodeToString(sum[:]), Payload: body}
		if err = s.Workflow.Create(ctx, tx, scope, approval, input.Reason, s.Now()); err != nil {
			return foundation.CommandResult{}, err
		}
		return result.Result(200, id, request, map[string]string{"approval_id": id})
	})
}
func (s Service) Ancestors(ctx context.Context, p identity.Principal, id string, at time.Time, offset, limit int) ([]domain.Ancestor, bool, error) {
	return s.Repo.Ancestors(ctx, s.Query, foundation.Scope{Tenant: p.Tenant, App: p.App}, id, at, offset, limit)
}
