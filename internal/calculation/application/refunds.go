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
	"math/big"
	"time"
)

type RefundService struct {
	Adjustments ports.Adjustments
	Commands    db.Commands
	UOW         db.UnitOfWork
	Query       db.Executor
	Repo        ports.RefundRepository
	Tasks       ports.Tasks
	Ledger      ports.RefundLedger
	Now         func() time.Time
	ID          func() string
}

func (s RefundService) Receive(ctx context.Context, p identity.Principal, input model.Refund, key, request string) (foundation.CommandResult, error) {
	if !foundation.ValidID(input.Source) || !foundation.ValidID(input.ID) || !foundation.ValidID(input.BusinessID) || input.Time.IsZero() || input.Time.Nanosecond()%1000 != 0 || !foundation.ValidID(input.Reason) {
		return foundation.CommandResult{}, foundation.ErrValidation
	}
	total, err := foundation.ParseDecimal(input.Total)
	if err != nil {
		return foundation.CommandResult{}, err
	}
	input.Total = total.String()
	input.Time = input.Time.UTC()
	if input.Quantity != "" {
		q, err := foundation.ParseDecimal(input.Quantity)
		if err != nil {
			return foundation.CommandResult{}, err
		}
		input.Quantity = q.String()
	}
	scope := foundation.Scope{Tenant: p.Tenant, App: p.App}
	return s.Commands.Execute(ctx, scope, "refund-receive", key, input, func(tx db.Executor) (foundation.CommandResult, error) {
		if err := s.Repo.Lock(ctx, tx, scope, input.Source, input.BusinessID, input.EventType); err != nil {
			return foundation.CommandResult{}, err
		}
		// If the original exists, reject over-refunds before accepting the source's confirmation.
		task, err := s.Tasks.ByBusiness(ctx, tx, scope, input.Source, input.BusinessID, input.EventType)
		if err == nil {
			var snapshot model.Input
			if len(task.Snapshot) > 0 {
				if err = json.Unmarshal(task.Snapshot, &snapshot); err != nil {
					return foundation.CommandResult{}, err
				}
				if s.Adjustments != nil {
					snapshot, _, err = s.Adjustments.Latest(ctx, tx, scope, task)
					if err != nil {
						return foundation.CommandResult{}, err
					}
				}
				if err = validateRefund(input, snapshot); err != nil {
					return foundation.CommandResult{}, err
				}
			}
		} else if err.Error() != "NOT_FOUND" {
			return foundation.CommandResult{}, err
		}
		body, _ := json.Marshal(input)
		canonical, err := foundation.BusinessCanonicalJSON(body)
		if err != nil {
			return foundation.CommandResult{}, err
		}
		r, err := s.Repo.Accept(ctx, tx, scope, model.RefundRecord{ID: s.ID(), Input: input}, canonical, s.Now())
		if err != nil {
			return foundation.CommandResult{}, err
		}
		return response.Result(202, r.ID, request, map[string]string{"refund_task_id": r.ID, "status": r.Status})
	})
}
func validateRefund(r model.Refund, input model.Input) error {
	if r.Currency != input.Facts.Currency {
		return errors.New("UNIT_MISMATCH")
	}
	original, err := foundation.ParseDecimal(input.Facts.Basis)
	if err != nil {
		return err
	}
	refund, err := foundation.ParseDecimal(r.Total)
	if err != nil {
		return err
	}
	if refund.Atoms().Cmp(original.Atoms()) > 0 {
		return errors.New("REFUND_EXCEEDS_ORIGINAL")
	}
	if r.Quantity != "" {
		old, err := foundation.ParseDecimal(input.Facts.Quantity)
		if err != nil {
			return errors.New("UNIT_MISMATCH")
		}
		q, err := foundation.ParseDecimal(r.Quantity)
		if err != nil {
			return err
		}
		if q.Atoms().Cmp(old.Atoms()) > 0 {
			return errors.New("REFUND_EXCEEDS_ORIGINAL")
		}
		if r.Unit != input.Facts.QuantityUnit {
			return errors.New("UNIT_MISMATCH")
		}
	}
	return nil
}
func (s RefundService) totals(ctx context.Context, tx db.Executor, scope foundation.Scope, task model.Task, input model.Input) (string, string, string, []model.RefundRecord, error) {
	records, err := s.Repo.Records(ctx, tx, scope, task.Source, task.BusinessID, task.EventType)
	if err != nil {
		return "", "", "", nil, err
	}
	max := new(big.Int)
	maxQuantity := new(big.Int)
	total, quantity, unit := "0", "", ""
	active := []model.RefundRecord{}
	for _, r := range records {
		if r.Status == "REJECTED" {
			continue
		}
		if err = validateRefund(r.Input, input); err != nil {
			code := err.Error()
			if e := s.Repo.State(ctx, tx, scope, r.ID, "REJECTED", &code, s.Now()); e != nil {
				return "", "", "", nil, e
			}
			continue
		}
		d, _ := foundation.ParseDecimal(r.Input.Total)
		cmp := d.Atoms().Cmp(max)
		if cmp < 0 {
			if r.Status != "SUCCEEDED" && r.Status != "STALE_NOOP" {
				if err = s.Repo.State(ctx, tx, scope, r.ID, "STALE_NOOP", nil, s.Now()); err != nil {
					return "", "", "", nil, err
				}
			}
			continue
		}
		if r.Input.Quantity != "" {
			old := maxQuantity
			q, _ := foundation.ParseDecimal(r.Input.Quantity)
			if q.Atoms().Cmp(old) < 0 {
				code := "NON_MONOTONIC_REFUND"
				if err = s.Repo.State(ctx, tx, scope, r.ID, "REJECTED", &code, s.Now()); err != nil {
					return "", "", "", nil, err
				}
				continue
			}
		}
		if r.Input.Quantity != "" {
			q, _ := foundation.ParseDecimal(r.Input.Quantity)
			maxQuantity = q.Atoms()
		}
		if cmp > 0 {
			max = d.Atoms()
			total = r.Input.Total
			quantity = r.Input.Quantity
			unit = r.Input.Unit
		} else if r.Input.Quantity != "" {
			quantity = r.Input.Quantity
			unit = r.Input.Unit
		}
		if r.Status != "SUCCEEDED" && r.Status != "STALE_NOOP" {
			active = append(active, r)
		}
	}
	return total, quantity, unit, active, nil
}
func (s RefundService) finish(ctx context.Context, tx db.Executor, scope foundation.Scope, records []model.RefundRecord, status string, reason *string) error {
	for _, r := range records {
		if err := s.Repo.State(ctx, tx, scope, r.ID, status, reason, s.Now()); err != nil {
			return err
		}
	}
	return nil
}
func (s RefundService) Prepare(ctx context.Context, tx db.Executor, scope foundation.Scope, task model.Task, input model.Input, result model.Result) (model.Result, error) {
	if err := s.Repo.Lock(ctx, tx, scope, task.Source, task.BusinessID, task.EventType); err != nil {
		return result, err
	}
	total, q, unit, records, err := s.totals(ctx, tx, scope, task, input)
	if err != nil {
		return result, err
	}
	targets, err := model.RefundTargets(input, total, q, unit)
	if err != nil {
		return result, err
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
	return result, s.finish(ctx, tx, scope, records, "SUCCEEDED", nil)
}
func (s RefundService) Process(ctx context.Context, scope foundation.Scope, record model.RefundRecord) error {
	return s.UOW.Within(ctx, func(tx db.Executor) error {
		if err := s.Repo.Lock(ctx, tx, scope, record.Input.Source, record.Input.BusinessID, record.Input.EventType); err != nil {
			return err
		}
		task, err := s.Tasks.ByBusiness(ctx, tx, scope, record.Input.Source, record.Input.BusinessID, record.Input.EventType)
		if err != nil {
			if err.Error() == "NOT_FOUND" {
				return nil
			}
			return err
		}
		if len(task.Snapshot) == 0 {
			return nil
		}
		var input model.Input
		if err = json.Unmarshal(task.Snapshot, &input); err != nil {
			return err
		}
		if s.Adjustments != nil {
			input, _, err = s.Adjustments.Latest(ctx, tx, scope, task)
			if err != nil {
				return err
			}
		}
		total, q, unit, records, err := s.totals(ctx, tx, scope, task, input)
		if err != nil {
			return err
		}
		targets, err := model.RefundTargets(input, total, q, unit)
		if err != nil {
			code := err.Error()
			return s.finish(ctx, tx, scope, records, "SUSPENDED", &code)
		}
		if task.Status != "SUCCEEDED" && task.Status != "NO_MATCH" {
			if task.Status == "SUSPENDED" && task.Reason != nil && *task.Reason == "REFUND_QUANTITY_MISSING" {
				return s.Tasks.Resume(ctx, tx, scope, task.ID, task.Revision, "refund-facts-"+record.ID, s.Now())
			}
			return nil
		}
		entitlements, err := s.Ledger.TaskEntitlements(ctx, tx, scope, task.ID)
		if err != nil {
			return err
		}
		held := false
		for _, e := range entitlements {
			if e.Reserved != "0" {
				held = true
			}
		}
		if held {
			for _, e := range entitlements {
				if err = s.Ledger.Hold(ctx, tx, scope, e.ID, "refund-"+task.ID, "REFUND_AWAITING_PAYMENT", s.Now()); err != nil {
					return err
				}
			}
			code := "PAYMENT_RESULT_UNKNOWN"
			return s.finish(ctx, tx, scope, records, "WAITING_PAYMENT", &code)
		}
		for _, e := range entitlements {
			target := targets[e.RuleID+"\x00"+e.AgentID]
			if target == "" {
				target = "0"
			}
			if err = s.Ledger.Reduce(ctx, tx, scope, e.ID, target, "refund-"+task.ID+"-"+total, s.Now()); err != nil {
				return err
			}
			if err = s.Ledger.CloseHold(ctx, tx, scope, e.ID, "refund-"+task.ID, s.Now()); err != nil {
				return err
			}
		}
		return s.finish(ctx, tx, scope, records, "SUCCEEDED", nil)
	})
}
func (s RefundService) Recover(ctx context.Context, scope foundation.Scope) error {
	records, err := s.Repo.Pending(ctx, s.Query, scope)
	if err != nil {
		return err
	}
	for _, r := range records {
		if err = s.Process(ctx, scope, r); err != nil {
			return err
		}
	}
	return nil
}
