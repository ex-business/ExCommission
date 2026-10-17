package application

import (
	"context"
	"encoding/json"
	"errors"
	model "github.com/ex-business/ExCommission/internal/affiliate/domain"
	"github.com/ex-business/ExCommission/internal/affiliate/ports"
	identity "github.com/ex-business/ExCommission/internal/identity/domain"
	response "github.com/ex-business/ExCommission/internal/platform/application"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	approval "github.com/ex-business/ExCommission/internal/workflow/domain"
	workflow "github.com/ex-business/ExCommission/internal/workflow/ports"
	"time"
)

type ConfigurationService struct {
	Commands db.Commands
	Query    db.Executor
	Repo     ports.Configurations
	Workflow workflow.Repository
	Now      func() time.Time
	ID       func() string
	Hash     func([]byte) string
}

func (s ConfigurationService) validate(ctx context.Context, tx db.Executor, scope foundation.Scope, in model.Change, at time.Time, lock bool) error {
	old, e := s.Repo.Configuration(ctx, tx, scope, at, lock)
	if e != nil {
		return e
	}
	if old.Revision != in.Revision {
		return errors.New("REVISION_CONFLICT")
	}
	if old.Scheduled != nil {
		return errors.New("SCHEDULED_CONFIG_EXISTS")
	}
	if old.Current != nil {
		if e = in.Config.ValidateChange(old.Current.Config); e != nil {
			return e
		}
	}
	venues, e := s.Repo.Venues(ctx, tx, scope)
	if e != nil {
		return e
	}
	known := map[string]bool{}
	for _, v := range venues {
		known[v.ID] = true
	}
	for _, id := range in.Config.VenueIDs {
		if !known[id] {
			return errors.New("UNKNOWN_VENUE_TYPE")
		}
	}
	return nil
}
func (s ConfigurationService) Request(ctx context.Context, p identity.Principal, in model.Change, key, rid string) (foundation.CommandResult, error) {
	if e := in.Normalize(); e != nil {
		return foundation.CommandResult{}, e
	}
	scope := foundation.Scope{Tenant: p.Tenant, App: p.App}
	ctx = foundation.WithRequest(ctx, rid, p.Subject)
	return s.Commands.Execute(ctx, scope, "affiliate-config-request", key, in, func(tx db.Executor) (foundation.CommandResult, error) {
		if e := s.validate(ctx, tx, scope, in, s.Now(), true); e != nil {
			return foundation.CommandResult{}, e
		}
		b, _ := json.Marshal(in)
		a := approval.Request{ID: s.ID(), Type: "AFFILIATE_CONFIG", Applicant: p.Subject, ObjectRef: "affiliate-config", ObjectHash: s.Hash(b), Payload: b}
		if e := s.Workflow.Create(ctx, tx, scope, a, in.Reason, s.Now()); e != nil {
			return foundation.CommandResult{}, e
		}
		return response.Result(202, a.ID, rid, map[string]any{"approval_id": a.ID, "object_hash": a.ObjectHash, "revision": 0})
	})
}
func (s ConfigurationService) Execute(ctx context.Context, tx db.Executor, scope foundation.Scope, a approval.Request, at time.Time) error {
	if a.Type != "AFFILIATE_CONFIG" || a.ObjectRef != "affiliate-config" || s.Hash(a.Payload) != a.ObjectHash {
		return foundation.ErrValidation
	}
	var in model.Change
	if e := json.Unmarshal(a.Payload, &in); e != nil {
		return e
	}
	return s.Repo.Apply(ctx, tx, scope, in, a.ID, at)
}
