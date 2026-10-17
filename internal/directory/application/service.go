package application

import (
	"context"
	model "github.com/ex-business/ExCommission/internal/directory/domain"
	"github.com/ex-business/ExCommission/internal/directory/ports"
	identity "github.com/ex-business/ExCommission/internal/identity/domain"
	response "github.com/ex-business/ExCommission/internal/platform/application"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"time"
)

type Service struct {
	Commands db.Commands
	Query    db.Executor
	Repo     ports.Repository
	Now      func() time.Time
}

func (s Service) Save(ctx context.Context, p identity.Principal, in model.Statistics, key, rid string) (foundation.CommandResult, error) {
	if err := in.Validate(); err != nil {
		return foundation.CommandResult{}, err
	}
	if in.Observed.After(s.Now()) {
		return foundation.CommandResult{}, foundation.ErrValidation
	}
	ctx = foundation.WithRequest(ctx, rid, p.Subject)
	scope := foundation.Scope{Tenant: p.Tenant, App: p.App}
	return s.Commands.Execute(ctx, scope, "member-statistics", key, in, func(tx db.Executor) (foundation.CommandResult, error) {
		if err := s.Repo.Save(ctx, tx, scope, in); err != nil {
			return foundation.CommandResult{}, err
		}
		return response.Result(200, in.UserID, rid, map[string]string{"user_id": in.UserID, "status": "RECORDED"})
	})
}
