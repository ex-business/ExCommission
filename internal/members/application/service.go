package application

import (
	"context"
	"errors"
	identity "github.com/ex-business/ExCommission/internal/identity/domain"
	model "github.com/ex-business/ExCommission/internal/members/domain"
	"github.com/ex-business/ExCommission/internal/members/ports"
	response "github.com/ex-business/ExCommission/internal/platform/application"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"time"
)

type Service struct {
	Commands  db.Commands
	Query     db.Executor
	Repo      ports.Repository
	Bindings  ports.Bindings
	Authorize func(context.Context, db.Executor, identity.Principal) error
	CanRefer  func(context.Context, db.Executor, foundation.Scope, string) error
	Now       func() time.Time
	ID        func() string
	PublicURL string
	ShareURL  func(string, string) (string, error)
}

func (s Service) Read(ctx context.Context, p identity.Principal) (model.Link, error) {
	scope := foundation.Scope{Tenant: p.Tenant, App: p.App}
	if p.MemberUserID == "" {
		return model.Link{}, identity.ErrPermission
	}
	agent, err := s.Repo.OwnAgent(ctx, s.Query, scope, p.MemberUserID, false)
	if err != nil {
		return model.Link{}, err
	}
	link, err := s.Repo.Link(ctx, s.Query, scope, agent)
	if err != nil {
		return link, err
	}
	link.URL, err = s.shareURL(s.PublicURL, link.Code)
	return link, err
}
func (s Service) Create(ctx context.Context, p identity.Principal, key, rid string) (foundation.CommandResult, error) {
	if p.MemberUserID == "" || s.Authorize == nil {
		return foundation.CommandResult{}, identity.ErrPermission
	}
	if err := s.Authorize(ctx, s.Query, p); err != nil {
		return foundation.CommandResult{}, err
	}
	if _, err := s.shareURL(s.PublicURL, "probe"); err != nil {
		return foundation.CommandResult{}, err
	}
	scope := foundation.Scope{Tenant: p.Tenant, App: p.App}
	ctx = foundation.WithRequest(ctx, rid, p.Subject)
	return s.Commands.Execute(ctx, scope, "member-referral-link", key, map[string]string{"user_id": p.MemberUserID}, func(tx db.Executor) (foundation.CommandResult, error) {
		if err := s.Authorize(ctx, tx, p); err != nil {
			return foundation.CommandResult{}, err
		}
		agent, err := s.Repo.OwnAgent(ctx, tx, scope, p.MemberUserID, true)
		if err != nil {
			return foundation.CommandResult{}, err
		}
		if err = s.CanRefer(ctx, tx, scope, agent); err != nil {
			return foundation.CommandResult{}, err
		}
		link, err := s.Repo.Create(ctx, tx, scope, agent, s.ID(), s.Now())
		if err != nil {
			return foundation.CommandResult{}, err
		}
		link.URL, err = s.shareURL(s.PublicURL, link.Code)
		if err != nil {
			return foundation.CommandResult{}, err
		}
		return response.Result(201, link.Code, rid, link)
	})
}
func (s Service) Bind(ctx context.Context, p identity.Principal, code, key, rid string) (foundation.CommandResult, error) {
	if !foundation.ValidID(code) {
		return foundation.CommandResult{}, foundation.ErrValidation
	}
	if p.MemberUserID == "" || s.Authorize == nil {
		return foundation.CommandResult{}, identity.ErrPermission
	}
	if err := s.Authorize(ctx, s.Query, p); err != nil {
		return foundation.CommandResult{}, err
	}
	scope := foundation.Scope{Tenant: p.Tenant, App: p.App}
	ctx = foundation.WithRequest(ctx, rid, p.Subject)
	return s.Commands.Execute(ctx, scope, "member-referral-binding", key, map[string]string{"user_id": p.MemberUserID, "code": code}, func(tx db.Executor) (foundation.CommandResult, error) {
		if err := s.Authorize(ctx, tx, p); err != nil {
			return foundation.CommandResult{}, err
		}
		agent, err := s.Repo.Resolve(ctx, tx, scope, code)
		if err != nil {
			return foundation.CommandResult{}, err
		}
		if err = s.CanRefer(ctx, tx, scope, agent); err != nil {
			return foundation.CommandResult{}, err
		}
		own, err := s.Repo.OwnAgent(ctx, tx, scope, p.MemberUserID, true)
		if err == nil && own != "" {
			return foundation.CommandResult{}, errors.New("STATE_CONFLICT")
		}
		if err != nil && err.Error() != "NOT_FOUND" {
			return foundation.CommandResult{}, err
		}
		if err = s.Bindings.BindInitial(ctx, tx, scope, p.MemberUserID, agent, "referral-"+code, s.Now()); err != nil {
			return foundation.CommandResult{}, err
		}
		return response.Result(200, p.MemberUserID, rid, map[string]string{"status": "BOUND"})
	})
}

// shareURL 保留旧会员模块的链接约定；接入注册来源时由装配层注入统一注册链接。
func (s Service) shareURL(base, code string) (string, error) {
	if s.ShareURL != nil {
		return s.ShareURL(base, code)
	}
	return model.ShareURL(base, code)
}
