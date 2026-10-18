package application

import (
	"context"
	"encoding/json"
	calculation "github.com/ex-business/ExCommission/internal/calculation/domain"
	tasks "github.com/ex-business/ExCommission/internal/calculation/ports"
	identity "github.com/ex-business/ExCommission/internal/identity/domain"
	eventmodel "github.com/ex-business/ExCommission/internal/integration/domain"
	"github.com/ex-business/ExCommission/internal/integration/ports"
	plan "github.com/ex-business/ExCommission/internal/plans/domain"
	plans "github.com/ex-business/ExCommission/internal/plans/ports"
	response "github.com/ex-business/ExCommission/internal/platform/application"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"time"
)

type Service struct {
	// 原事务内先核对跨引擎归属，失败时不保存事件、任务或权益。
	ExecutionGuard func(context.Context, db.Executor, foundation.Scope, eventmodel.Event) error
	AllowNew       func(context.Context, db.Executor, foundation.Scope, string) error
	Commands       db.Commands
	Query          db.Executor
	Repo           ports.Repository
	Tasks          tasks.Tasks
	Source         ports.Source
	Plans          plans.Repository
	Now            func() time.Time
	ID             func() string
}

func (s Service) Receive(ctx context.Context, p identity.Principal, raw []byte, signature, key, request string) (foundation.CommandResult, error) {
	event, err := s.Source.Normalize(raw, signature)
	if err != nil {
		return foundation.CommandResult{}, err
	}
	ctx = foundation.WithRequest(ctx, request, p.Subject)
	scope := foundation.Scope{Tenant: p.Tenant, App: p.App}
	return s.Commands.Execute(ctx, scope, "event-receive", key, event, func(tx db.Executor) (foundation.CommandResult, error) {
		if s.ExecutionGuard != nil {
			if err := s.ExecutionGuard(ctx, tx, scope, event); err != nil {
				return foundation.CommandResult{}, err
			}
		}
		body, err := json.Marshal(event)
		if err != nil {
			return foundation.CommandResult{}, err
		}
		canonical, err := foundation.CanonicalJSON(body)
		if err != nil {
			return foundation.CommandResult{}, err
		}
		original, err := s.Repo.Accept(ctx, tx, scope, event, canonical, s.Now())
		if err != nil {
			return foundation.CommandResult{}, err
		}
		if err = s.Repo.SaveRaw(ctx, tx, scope, event, raw); err != nil {
			return foundation.CommandResult{}, err
		}
		task, err := s.Tasks.BySource(ctx, tx, scope, event.Source, original)
		if err != nil && err.Error() != "NOT_FOUND" {
			return foundation.CommandResult{}, err
		}
		if err != nil {
			if s.AllowNew != nil {
				if err = s.AllowNew(ctx, tx, scope, event.Source); err != nil {
					return foundation.CommandResult{}, err
				}
			}
			if s.Plans == nil {
				return foundation.CommandResult{}, foundation.ErrValidation
			}
			version, planErr := s.Plans.Select(ctx, tx, scope, plan.Route{Product: event.Product, EventType: event.Type, Scene: event.Scene}, event.Time)
			if planErr != nil && planErr.Error() != "NO_EFFECTIVE_PLAN_VERSION" {
				return foundation.CommandResult{}, planErr
			}
			if planErr == nil {
				for _, rule := range version.Config.Rules {
					if rule.Algorithm == "UNIT_PRICE" {
						if event.Payload.Quantity == "" || event.Payload.QuantityUnit == "" {
							return foundation.CommandResult{}, foundation.ErrValidation
						}
						if event.Payload.QuantityUnit != rule.Parameters.QuantityUnit {
							return foundation.CommandResult{}, foundation.ErrValidation
						}
					}
				}
			}
			task = calculation.Task{ID: s.ID(), EventID: s.ID(), Source: event.Source, SourceEvent: original, BusinessID: event.BusinessID, EventType: event.Type, BusinessTime: event.Time, EventBody: canonical, Status: "QUEUED"}
			if err = s.Tasks.Create(ctx, tx, scope, task, s.Now()); err != nil {
				return foundation.CommandResult{}, err
			}

		}
		return response.Result(202, task.EventID, request, map[string]any{"event_id": task.EventID, "source_event_id": original, "task_id": task.ID, "acceptance_status": "ACCEPTED"})
	})
}
