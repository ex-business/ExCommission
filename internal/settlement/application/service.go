package application

import (
	"context"
	"encoding/json"
	"errors"
	identity "github.com/ex-business/ExCommission/internal/identity/domain"
	response "github.com/ex-business/ExCommission/internal/platform/application"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	model "github.com/ex-business/ExCommission/internal/settlement/domain"
	"github.com/ex-business/ExCommission/internal/settlement/ports"
	hash "github.com/ex-business/ExCommission/internal/workflow/adapters"
	approval "github.com/ex-business/ExCommission/internal/workflow/domain"
	workflow "github.com/ex-business/ExCommission/internal/workflow/ports"
	"math/big"
	"sort"
	"time"
)

type RunInput struct {
	Period   string   `json:"period"`
	Calendar string   `json:"calendar_version"`
	AgentIDs []string `json:"agent_ids,omitempty"`
}
type Submission struct {
	Revision uint64 `json:"expected_revision"`
	Hash     string `json:"content_hash"`
}
type approvalPayload struct {
	ID       string `json:"statement_id"`
	Revision uint64 `json:"statement_revision"`
	Hash     string `json:"content_hash"`
}
type Service struct {
	Commands db.Commands
	Query    db.Executor
	Repo     ports.Repository
	Ledger   ports.Ledger
	Workflow workflow.Repository
	Now      func() time.Time
	ID       func() string
}

func (s Service) Run(ctx context.Context, p identity.Principal, input RunInput, key, request string) (foundation.CommandResult, error) {
	period, err := time.Parse("2006-01-02", input.Period)
	if err != nil || input.Calendar != "UTC-v1" || period.Add(24*time.Hour).After(s.Now()) {
		return foundation.CommandResult{}, foundation.ErrValidation
	}
	if input.AgentIDs == nil {
		input.AgentIDs = []string{}
	}
	filter := map[string]bool{}
	for _, id := range input.AgentIDs {
		if !foundation.ValidID(id) || filter[id] {
			return foundation.CommandResult{}, foundation.ErrValidation
		}
		filter[id] = true
	}
	sort.Strings(input.AgentIDs)
	scope := foundation.Scope{Tenant: p.Tenant, App: p.App}
	return s.Commands.Execute(ctx, scope, "statement-run", key, input, func(tx db.Executor) (foundation.CommandResult, error) {
		if err := s.Repo.Lock(ctx, tx, scope); err != nil {
			return foundation.CommandResult{}, err
		}
		filterBody, _ := json.Marshal(map[string]any{"agent_ids": input.AgentIDs})
		filterHash := hash.Hash(filterBody)
		statements, exists, err := s.Repo.ExistingRun(ctx, tx, scope, input.Period, filterHash)
		if err != nil {
			return foundation.CommandResult{}, err
		}
		if exists {
			return response.Result(202, "period-"+input.Period, request, map[string]any{"period": input.Period, "statements": statements})
		}
		if err = s.Repo.CreateRun(ctx, tx, scope, s.ID(), input.Period, input.Calendar, filterHash, s.Now()); err != nil {
			return foundation.CommandResult{}, err
		}
		candidates, err := s.Ledger.PendingBefore(ctx, tx, scope, period.Add(24*time.Hour))
		if err != nil {
			return foundation.CommandResult{}, err
		}
		groups := map[string]*model.Statement{}
		keys := []string{}
		for _, e := range candidates {
			if len(filter) > 0 && !filter[e.AgentID] {
				continue
			}
			claimed, err := s.Repo.Claimed(ctx, tx, scope, e.ID)
			if err != nil {
				return foundation.CommandResult{}, err
			}
			if claimed {
				continue
			}
			group := e.AgentID + "\x00" + e.Currency
			v := groups[group]
			if v == nil {
				v = &model.Statement{ID: s.ID(), AgentID: e.AgentID, Currency: e.Currency, Period: input.Period, Status: "DRAFT", Total: "0", Items: []model.Item{}}
				groups[group] = v
				keys = append(keys, group)
			}
			v.Items = append(v.Items, model.Item{ID: e.ID, Amount: e.Pending, Revision: e.Revision})
			a, _ := new(big.Int).SetString(v.Total, 10)
			b, _ := new(big.Int).SetString(e.Pending, 10)
			a.Add(a, b)
			if len(a.String()) > 38 {
				return foundation.CommandResult{}, errors.New("AMOUNT_OUT_OF_RANGE")
			}
			v.Total = a.String()
		}
		statements = []model.Statement{}
		sort.Strings(keys)
		for _, key := range keys {
			v := groups[key]
			sort.Slice(v.Items, func(i, j int) bool { return v.Items[i].ID < v.Items[j].ID })
			body, _ := json.Marshal(struct {
				Agent, Currency, Period string
				Items                   []model.Item
			}{v.AgentID, v.Currency, v.Period, v.Items})
			v.Hash = hash.Hash(body)
			if err = s.Repo.Create(ctx, tx, scope, *v, s.Now()); err != nil {
				return foundation.CommandResult{}, err
			}
			statements = append(statements, *v)
		}
		return response.Result(202, "period-"+input.Period, request, map[string]any{"period": input.Period, "statements": statements})
	})
}
func (s Service) validate(ctx context.Context, tx db.Executor, scope foundation.Scope, v model.Statement) error {
	for _, item := range v.Items {
		e, err := s.Ledger.Get(ctx, tx, scope, item.ID, true)
		if err != nil {
			return err
		}
		if e.Frozen || e.Revision != item.Revision || e.Pending != item.Amount || e.AgentID != v.AgentID || e.Currency != v.Currency {
			return errors.New("STALE_APPROVAL")
		}
	}
	return nil
}
func (s Service) Submit(ctx context.Context, p identity.Principal, id string, input Submission, key, request string) (foundation.CommandResult, error) {
	scope := foundation.Scope{Tenant: p.Tenant, App: p.App}
	payload := struct {
		ID string `json:"statement_id"`
		Submission
	}{id, input}
	return s.Commands.Execute(ctx, scope, "statement-submit", key, payload, func(tx db.Executor) (foundation.CommandResult, error) {
		if err := s.Repo.Lock(ctx, tx, scope); err != nil {
			return foundation.CommandResult{}, err
		}
		v, err := s.Repo.Get(ctx, tx, scope, id, true)
		if err != nil {
			return foundation.CommandResult{}, err
		}
		if v.Revision != input.Revision || v.Hash != input.Hash {
			return foundation.CommandResult{}, errors.New("REVISION_CONFLICT")
		}
		if v.Status != "DRAFT" {
			return foundation.CommandResult{}, errors.New("STATE_CONFLICT")
		}
		if err = s.validate(ctx, tx, scope, v); err != nil {
			return foundation.CommandResult{}, err
		}
		if err = s.Repo.Claim(ctx, tx, scope, v); err != nil {
			return foundation.CommandResult{}, err
		}
		if err = s.Repo.State(ctx, tx, scope, v, "PENDING_APPROVAL"); err != nil {
			return foundation.CommandResult{}, err
		}
		body, _ := json.Marshal(approvalPayload{ID: id, Revision: v.Revision + 1, Hash: v.Hash})
		a := approval.Request{ID: s.ID(), Type: "STATEMENT", Applicant: p.Subject, ObjectRef: id, ObjectHash: hash.Hash(body), Payload: body}
		if err = s.Workflow.Create(ctx, tx, scope, a, "statement-submission", s.Now()); err != nil {
			return foundation.CommandResult{}, err
		}
		return response.Result(202, a.ID, request, map[string]any{"approval_id": a.ID, "statement_id": id, "object_hash": a.ObjectHash, "revision": 0})
	})
}
func (s Service) Execute(ctx context.Context, tx db.Executor, scope foundation.Scope, a approval.Request, now time.Time) error {
	var p approvalPayload
	if err := json.Unmarshal(a.Payload, &p); err != nil {
		return err
	}
	if err := s.Repo.Lock(ctx, tx, scope); err != nil {
		return err
	}
	v, err := s.Repo.Get(ctx, tx, scope, p.ID, true)
	if err != nil {
		return err
	}
	if v.Status != "PENDING_APPROVAL" || v.Revision != p.Revision || v.Hash != p.Hash {
		return errors.New("STALE_APPROVAL")
	}
	if err = s.validate(ctx, tx, scope, v); err != nil {
		return err
	}
	for _, item := range v.Items {
		if err = s.Ledger.Transfer(ctx, tx, scope, item.ID, "pending", "available", item.Amount, "settle-"+v.ID, now); err != nil {
			return err
		}
	}
	return s.Repo.State(ctx, tx, scope, v, "APPROVED")
}
func (s Service) Reject(ctx context.Context, tx db.Executor, scope foundation.Scope, a approval.Request, now time.Time) error {
	if err := s.Repo.Lock(ctx, tx, scope); err != nil {
		return err
	}
	v, err := s.Repo.Get(ctx, tx, scope, a.ObjectRef, true)
	if err != nil {
		return err
	}
	if v.Status != "PENDING_APPROVAL" {
		return errors.New("STATE_CONFLICT")
	}
	return s.Repo.State(ctx, tx, scope, v, "DRAFT")
}
