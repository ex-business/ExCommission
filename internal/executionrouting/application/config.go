// Package application 负责执行路由申请与独立审批生效，复用原独立审批与事务审计。
package application

import (
	"context"
	"encoding/json"
	"errors"
	repo "github.com/ex-business/ExCommission/internal/executionrouting/adapters"
	model "github.com/ex-business/ExCommission/internal/executionrouting/domain"
	identity "github.com/ex-business/ExCommission/internal/identity/domain"
	response "github.com/ex-business/ExCommission/internal/platform/application"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	approval "github.com/ex-business/ExCommission/internal/workflow/domain"
	workflow "github.com/ex-business/ExCommission/internal/workflow/ports"
	"log/slog"
	"strings"
	"time"
)

type Change struct {
	Revision uint64       `json:"expected_revision"`
	Config   model.Config `json:"config"`
	Reason   string       `json:"reason"`
}
type Service struct {
	Commands  db.Commands
	Query     db.Executor
	Repo      repo.Repository
	Workflow  workflow.Repository
	Now       func() time.Time
	ID        func() string
	Hash      func([]byte) string
	Authorize func(context.Context, db.Executor, identity.Principal, string) error
}

func (s Service) validate(ctx context.Context, tx db.Executor, sc foundation.Scope, in Change, lock bool) error {
	if len(strings.TrimSpace(in.Reason)) == 0 || len(in.Reason) > 1000 {
		return foundation.ErrValidation
	}
	if e := in.Config.Validate(); e != nil {
		return e
	}
	old, e := s.Repo.Head(ctx, tx, sc, lock)
	if e != nil {
		return e
	}
	if old.Revision != in.Revision {
		return errors.New("REVISION_CONFLICT")
	}
	if e = model.ValidateChange(old.Config, in.Config, s.Now()); e != nil {
		return e
	}

	return nil
}
func (s Service) Request(ctx context.Context, p identity.Principal, in Change, key, rid string) (foundation.CommandResult, error) {
	sc := foundation.Scope{Tenant: p.Tenant, App: p.App}
	ctx = foundation.WithRequest(ctx, rid, p.Subject)
	// 以不可变JSON文本保存配置快照，避免公共摘要把配置字段误认为已发生的业务。
	snapshot, _ := json.Marshal(in.Config)
	payload := storedChange{Revision: in.Revision, ConfigJSON: string(snapshot), Reason: in.Reason}
	out, e := s.Commands.Execute(ctx, sc, "execution-routing-request", key, payload, func(tx db.Executor) (foundation.CommandResult, error) {
		if e := s.Authorize(ctx, tx, p, "plans:write"); e != nil {
			return foundation.CommandResult{}, e
		}
		if e := s.validate(ctx, tx, sc, in, true); e != nil {
			return foundation.CommandResult{}, e
		}
		raw, _ := json.Marshal(payload)
		a := approval.Request{ID: s.ID(), Type: "EXECUTION_ROUTING", Applicant: p.Subject, ObjectRef: "execution-routing", ObjectHash: s.Hash(raw), Payload: raw}
		if e := s.Workflow.Create(ctx, tx, sc, a, in.Reason, s.Now()); e != nil {
			return foundation.CommandResult{}, e
		}
		return response.Result(202, a.ID, rid, map[string]any{"approval_id": a.ID, "object_hash": a.ObjectHash, "revision": 0})
	})
	if e == nil && !out.Replayed {
		slog.InfoContext(ctx, "执行路由配置已提交独立审批", "request_id", rid, "tenant_id", sc.Tenant, "app_id", sc.App, "expected_revision", in.Revision, "result", "PENDING")
	}
	return out, e
}
func (s Service) Execute(ctx context.Context, tx db.Executor, sc foundation.Scope, a approval.Request, at time.Time) error {
	if a.Type != "EXECUTION_ROUTING" || a.ObjectRef != "execution-routing" {
		return foundation.ErrValidation
	}
	in, e := DecodeChange(a.Payload)
	if e != nil {
		return e
	}
	if e := s.validate(ctx, tx, sc, in, true); e != nil {
		return e
	}
	return s.Repo.Apply(ctx, tx, sc, in.Revision, in.Config, a.ID, at)
}

// storedChange仅用于内部不可变审批快照，外部API仍采用结构化config对象。
type storedChange struct {
	Revision   uint64 `json:"expected_revision"`
	ConfigJSON string `json:"config_json"`
	Reason     string `json:"reason"`
}

func DecodeChange(raw []byte) (Change, error) {
	var stored storedChange
	var out Change
	if e := json.Unmarshal(raw, &stored); e != nil {
		return out, e
	}
	if e := json.Unmarshal([]byte(stored.ConfigJSON), &out.Config); e != nil {
		return out, e
	}
	out.Revision = stored.Revision
	out.Reason = stored.Reason
	return out, nil
}
