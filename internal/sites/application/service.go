package application

import (
	"context"
	"encoding/json"
	"errors"
	identity "github.com/ex-business/ExCommission/internal/identity/domain"
	plans "github.com/ex-business/ExCommission/internal/plans/ports"
	response "github.com/ex-business/ExCommission/internal/platform/application"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	model "github.com/ex-business/ExCommission/internal/sites/domain"
	"github.com/ex-business/ExCommission/internal/sites/ports"
	hash "github.com/ex-business/ExCommission/internal/workflow/adapters"
	approval "github.com/ex-business/ExCommission/internal/workflow/domain"
	workflow "github.com/ex-business/ExCommission/internal/workflow/ports"
	"time"
)

type Service struct {
	Commands db.Commands
	Query    db.Executor
	Repo     ports.Repository
	Plans    plans.Repository
	Workflow workflow.Repository
	Now      func() time.Time
	ID       func() string
}

func (s Service) Request(ctx context.Context, p identity.Principal, in model.Change, key, rid string) (foundation.CommandResult, error) {
	if err := in.Validate(); err != nil {
		return foundation.CommandResult{}, err
	}
	scope := foundation.Scope{Tenant: p.Tenant, App: p.App}
	ctx = foundation.WithRequest(ctx, rid, p.Subject)
	return s.Commands.Execute(ctx, scope, "site-agent-change", key, in, func(tx db.Executor) (foundation.CommandResult, error) {
		c, err := s.Repo.Current(ctx, tx, scope, true)
		if err != nil {
			return foundation.CommandResult{}, err
		}
		if c.Revision != in.Revision {
			return foundation.CommandResult{}, errors.New("REVISION_CONFLICT")
		}
		if in.Enabled {
			if _, err = s.Plans.AtPlan(ctx, tx, scope, in.PlanID, s.Now()); err != nil {
				return foundation.CommandResult{}, err
			}
		}
		body, _ := json.Marshal(in)
		a := approval.Request{ID: s.ID(), Type: "SITE_AGENT_CONFIG", Applicant: p.Subject, ObjectRef: "site-agent-config", ObjectHash: hash.Hash(body), Payload: body}
		if err = s.Workflow.Create(ctx, tx, scope, a, in.Reason, s.Now()); err != nil {
			return foundation.CommandResult{}, err
		}
		return response.Result(202, a.ID, rid, map[string]any{"approval_id": a.ID, "object_hash": a.ObjectHash, "revision": 0})
	})
}
func (s Service) Execute(ctx context.Context, tx db.Executor, scope foundation.Scope, a approval.Request, at time.Time) error {
	var in model.Change
	if err := json.Unmarshal(a.Payload, &in); err != nil {
		return err
	}
	if err := in.Validate(); err != nil {
		return err
	}
	if in.Enabled {
		if _, err := s.Plans.AtPlan(ctx, tx, scope, in.PlanID, at); err != nil {
			return err
		}
	}
	return s.Repo.Apply(ctx, tx, scope, in, a.ID, at)
}
func (s Service) Allow(ctx context.Context, tx db.Executor, scope foundation.Scope) error {
	c, err := s.Repo.Current(ctx, tx, scope, true)
	if err != nil {
		return err
	}
	if c.Configured && !c.Enabled {
		return errors.New("SITE_AGENT_DISABLED")
	}
	return nil
}
