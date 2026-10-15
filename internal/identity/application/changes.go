package application

import (
	"context"
	"encoding/json"
	model "github.com/ex-business/ExCommission/internal/identity/domain"
	response "github.com/ex-business/ExCommission/internal/platform/application"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	hash "github.com/ex-business/ExCommission/internal/workflow/adapters"
	approval "github.com/ex-business/ExCommission/internal/workflow/domain"
	workflow "github.com/ex-business/ExCommission/internal/workflow/ports"
	"sort"
	"time"
)

type GrantChange struct {
	Subject  string   `json:"subject_id"`
	Grant    []string `json:"grant"`
	Revoke   []string `json:"revoke"`
	Revision uint64   `json:"expected_revision"`
	Reason   string   `json:"reason"`
	Evidence string   `json:"evidence_ref"`
}
type GrantService struct {
	Commands db.Commands
	Workflow workflow.Repository
	Allowed  map[string]bool
	Now      func() time.Time
	ID       func() string
	Apply    func(context.Context, db.Executor, foundation.Scope, string, uint64, []string, []string) error
}

func (s GrantService) Request(ctx context.Context, p model.Principal, input GrantChange, key, request string) (foundation.CommandResult, error) {
	if !foundation.ValidID(input.Subject) || !foundation.ValidID(input.Reason) || !foundation.ValidID(input.Evidence) || len(input.Grant)+len(input.Revoke) == 0 {
		return foundation.CommandResult{}, foundation.ErrValidation
	}
	seen := map[string]bool{}
	for _, list := range [][]string{input.Grant, input.Revoke} {
		for _, permission := range list {
			if seen[permission] || !s.Allowed[permission] {
				return foundation.CommandResult{}, foundation.ErrValidation
			}
			seen[permission] = true
		}
	}
	sort.Strings(input.Grant)
	sort.Strings(input.Revoke)
	scope := foundation.Scope{Tenant: p.Tenant, App: p.App}
	return s.Commands.Execute(ctx, scope, "identity-change-request", key, input, func(tx db.Executor) (foundation.CommandResult, error) {
		body, _ := json.Marshal(input)
		a := approval.Request{ID: s.ID(), Type: "IDENTITY_GRANT", Applicant: p.Subject, ObjectRef: input.Subject, ObjectHash: hash.Hash(body), Payload: body}
		if err := s.Workflow.Create(ctx, tx, scope, a, input.Reason, s.Now()); err != nil {
			return foundation.CommandResult{}, err
		}
		return response.Result(202, a.ID, request, map[string]any{"approval_id": a.ID, "object_hash": a.ObjectHash, "revision": 0})
	})
}
func (s GrantService) Execute(ctx context.Context, tx db.Executor, scope foundation.Scope, a approval.Request, now time.Time) error {
	var input GrantChange
	if err := json.Unmarshal(a.Payload, &input); err != nil {
		return err
	}
	return s.Apply(ctx, tx, scope, input.Subject, input.Revision, input.Grant, input.Revoke)
}
