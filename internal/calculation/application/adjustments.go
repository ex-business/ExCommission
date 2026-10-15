package application

import (
	"context"
	"encoding/json"
	"errors"
	model "github.com/ex-business/ExCommission/internal/calculation/domain"
	"github.com/ex-business/ExCommission/internal/calculation/ports"
	identity "github.com/ex-business/ExCommission/internal/identity/domain"
	response "github.com/ex-business/ExCommission/internal/platform/application"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	hash "github.com/ex-business/ExCommission/internal/workflow/adapters"
	approval "github.com/ex-business/ExCommission/internal/workflow/domain"
	workflow "github.com/ex-business/ExCommission/internal/workflow/ports"
	"math/big"
	"time"
)

type AdjustmentInput struct {
	TaskID   string      `json:"original_task_id"`
	Facts    model.Facts `json:"corrected_facts"`
	Reason   string      `json:"reason"`
	Evidence string      `json:"evidence_ref"`
	Revision uint64      `json:"expected_revision"`
}
type adjustmentPayload struct {
	Input    AdjustmentInput `json:"input"`
	Snapshot model.Input     `json:"snapshot"`
	Result   model.Result    `json:"result"`
}
type AdjustmentService struct {
	Commands db.Commands
	Tasks    ports.Tasks
	Repo     ports.Adjustments
	Refunds  RefundService
	Ledger   ports.AdjustmentLedger
	Workflow workflow.Repository
	Now      func() time.Time
	ID       func() string
}

func (s AdjustmentService) derive(ctx context.Context, tx db.Executor, scope foundation.Scope, task model.Task, input AdjustmentInput) (model.Input, model.Result, error) {
	base, revision, err := s.Repo.Latest(ctx, tx, scope, task)
	if err != nil {
		return base, model.Result{}, err
	}
	if revision != input.Revision {
		return base, model.Result{}, errors.New("STALE_APPROVAL")
	}
	if input.Facts.Currency != base.Facts.Currency || input.Facts.Product != base.Facts.Product || input.Facts.Scene != base.Facts.Scene || !input.Facts.BusinessTime.Equal(base.Facts.BusinessTime) {
		return base, model.Result{}, foundation.ErrValidation
	}
	base.Facts = input.Facts
	base.SnapshotID = "adjustment-" + task.ID
	result := model.Calculate(base)
	if result.Status == "SUSPENDED" {
		return base, result, errors.New("UNKNOWN_FACT")
	}
	rows, err := s.Refunds.Repo.Records(ctx, tx, scope, task.Source, task.BusinessID, task.EventType)
	if err != nil {
		return base, result, err
	}
	for _, r := range rows {
		if r.Status == "REJECTED" || r.Status == "STALE_NOOP" {
			continue
		}
		if err = validateRefund(r.Input, base); err != nil {
			return base, result, err
		}
	}
	total, q, unit, _, err := s.Refunds.totals(ctx, tx, scope, task, base)
	if err != nil {
		return base, result, err
	}
	targets, err := model.RefundTargets(base, total, q, unit)
	if err != nil {
		return base, result, err
	}
	items := []model.Item{}
	for _, item := range result.Items {
		net := targets[item.RuleID+"\x00"+item.BeneficiaryID]
		if net == "" || net == "0" {
			continue
		}
		item.OriginalMinor = item.Minor
		item.Minor = net
		items = append(items, item)
	}
	result.Items = items
	return base, result, nil
}
func (s AdjustmentService) Request(ctx context.Context, p identity.Principal, input AdjustmentInput, key, request string) (foundation.CommandResult, error) {
	if !foundation.ValidID(input.Reason) || !foundation.ValidID(input.Evidence) {
		return foundation.CommandResult{}, foundation.ErrValidation
	}
	scope := foundation.Scope{Tenant: p.Tenant, App: p.App}
	return s.Commands.Execute(ctx, scope, "adjustment-request", key, input, func(tx db.Executor) (foundation.CommandResult, error) {
		task, err := s.Tasks.Get(ctx, tx, scope, input.TaskID)
		if err != nil {
			return foundation.CommandResult{}, err
		}
		if task.Status != "SUCCEEDED" && task.Status != "NO_MATCH" {
			return foundation.CommandResult{}, errors.New("STATE_CONFLICT")
		}
		if err = s.Refunds.Repo.Lock(ctx, tx, scope, task.Source, task.BusinessID, task.EventType); err != nil {
			return foundation.CommandResult{}, err
		}
		snapshot, result, err := s.derive(ctx, tx, scope, task, input)
		if err != nil {
			return foundation.CommandResult{}, err
		}
		body, _ := json.Marshal(adjustmentPayload{input, snapshot, result})
		a := approval.Request{ID: s.ID(), Type: "ADJUSTMENT", Applicant: p.Subject, ObjectRef: task.ID, ObjectHash: hash.Hash(body), Payload: body}
		if err = s.Workflow.Create(ctx, tx, scope, a, input.Reason, s.Now()); err != nil {
			return foundation.CommandResult{}, err
		}
		return response.Result(202, a.ID, request, map[string]any{"approval_id": a.ID, "object_hash": a.ObjectHash, "revision": 0})
	})
}
func (s AdjustmentService) Execute(ctx context.Context, tx db.Executor, scope foundation.Scope, a approval.Request, now time.Time) error {
	var p adjustmentPayload
	if err := json.Unmarshal(a.Payload, &p); err != nil {
		return err
	}
	task, err := s.Tasks.Get(ctx, tx, scope, p.Input.TaskID)
	if err != nil {
		return err
	}
	if err = s.Refunds.Repo.Lock(ctx, tx, scope, task.Source, task.BusinessID, task.EventType); err != nil {
		return err
	}
	snapshot, result, err := s.derive(ctx, tx, scope, task, p.Input)
	if err != nil {
		return err
	}
	body, _ := json.Marshal(result)
	expected, _ := json.Marshal(p.Result)
	if hash.Hash(body) != hash.Hash(expected) {
		return errors.New("STALE_APPROVAL")
	}
	existing, err := s.Ledger.TaskEntitlements(ctx, tx, scope, task.ID)
	if err != nil {
		return err
	}
	targets := map[string]model.Item{}
	for _, item := range result.Items {
		targets[item.RuleID+"\x00"+item.BeneficiaryID] = item
	}
	for _, e := range existing {
		key := e.RuleID + "\x00" + e.AgentID
		item, ok := targets[key]
		value := "0"
		if ok {
			value = item.Minor
		}
		n, _ := new(big.Int).SetString(value, 10)
		old, _ := new(big.Int).SetString(e.Target, 10)
		if n.Cmp(old) < 0 {
			err = s.Ledger.Reduce(ctx, tx, scope, e.ID, value, "adjust-"+a.ID, now)
		} else {
			err = s.Ledger.Increase(ctx, tx, scope, e.ID, value, "adjust-"+a.ID, now)
		}
		if err != nil {
			return err
		}
		delete(targets, key)
	}
	fresh := model.Result{TaskID: task.ID, Status: "SUCCEEDED", Items: []model.Item{}}
	for _, item := range result.Items {
		if _, ok := targets[item.RuleID+"\x00"+item.BeneficiaryID]; ok {
			fresh.Items = append(fresh.Items, item)
		}
	}
	if err = s.Ledger.Commit(ctx, tx, scope, task, snapshot, fresh, now); err != nil {
		return err
	}
	return s.Repo.Save(ctx, tx, scope, a.ID, task.ID, p.Input.Revision+1, snapshot, result, p.Input.Evidence, now)
}
