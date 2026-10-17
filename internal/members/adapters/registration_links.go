package adapters

import (
	"context"
	"database/sql"
	"errors"
	model "github.com/ex-business/ExCommission/internal/members/domain"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	registration "github.com/ex-business/ExCommission/internal/registration/adapters"
	"time"
)

// RegistrationLinks 仅在明确配置注册来源时装配，旧 member_referral_links 业务不被改写。
type RegistrationLinks struct {
	Repository
	Source string
}

func (r RegistrationLinks) Link(ctx context.Context, tx db.Executor, s foundation.Scope, agent string) (model.Link, error) {
	i, e := (registration.Repository{}).AgentInvitation(ctx, tx, s, r.Source, agent)
	if errors.Is(e, sql.ErrNoRows) {
		return model.Link{}, errors.New("NOT_FOUND")
	}
	return model.Link{Code: i.Code}, e
}
func (r RegistrationLinks) Create(ctx context.Context, tx db.Executor, s foundation.Scope, agent, unused string, at time.Time) (model.Link, error) {
	i, e := (registration.Repository{}).StableInvitation(ctx, tx, s, r.Source, agent, at)
	return model.Link{Code: i.Code}, e
}
func (r RegistrationLinks) Resolve(ctx context.Context, tx db.Executor, s foundation.Scope, code string) (string, error) {
	i, e := (registration.Repository{}).Invitation(ctx, tx, s, r.Source, code)
	if errors.Is(e, sql.ErrNoRows) {
		return "", errors.New("NOT_FOUND")
	}
	return i.AgentID, e
}
