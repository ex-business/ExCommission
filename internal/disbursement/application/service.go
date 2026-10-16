package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	model "github.com/ex-business/ExCommission/internal/disbursement/domain"
	"github.com/ex-business/ExCommission/internal/disbursement/ports"
	identity "github.com/ex-business/ExCommission/internal/identity/domain"
	response "github.com/ex-business/ExCommission/internal/platform/application"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"time"
)

type Service struct {
	Commands      db.Commands
	UOW           db.UnitOfWork
	Query         db.Executor
	Repo          ports.Repository
	Now           func() time.Time
	ID            func() string
	Validate      func(context.Context, db.Executor, foundation.Scope, model.Instruction) error
	CreatePayment func(context.Context, identity.Principal, model.Instruction, string) (foundation.CommandResult, error)
}

func (s Service) Prepare(ctx context.Context, p identity.Principal, in model.Prepare, key, rid string) (foundation.CommandResult, error) {
	if !foundation.ValidID(in.StatementID) || !foundation.ValidID(in.Channel) || !model.ValidAmount(in.Amount) || !model.ValidRemark(in.Remark) {
		return foundation.CommandResult{}, foundation.ErrValidation
	}
	scope := foundation.Scope{Tenant: p.Tenant, App: p.App}
	ctx = foundation.WithRequest(ctx, rid, p.Subject)
	return s.Commands.Execute(ctx, scope, "payout-prepare", key, in, func(tx db.Executor) (foundation.CommandResult, error) {
		i := model.Instruction{ID: s.ID(), StatementID: in.StatementID, Amount: in.Amount, Channel: in.Channel, StatementRevision: in.StatementRevision, Remark: in.Remark, Status: "DRAFT", Created: s.Now()}
		if err := s.Validate(ctx, tx, scope, i); err != nil {
			return foundation.CommandResult{}, err
		}
		if err := s.Repo.Create(ctx, tx, scope, i); err != nil {
			return foundation.CommandResult{}, err
		}
		return response.Result(201, i.ID, rid, i)
	})
}
func (s Service) Edit(ctx context.Context, p identity.Principal, id string, in model.Edit, key, rid string) (foundation.CommandResult, error) {
	if !model.ValidAmount(in.Amount) || !model.ValidRemark(in.Remark) {
		return foundation.CommandResult{}, foundation.ErrValidation
	}
	scope := foundation.Scope{Tenant: p.Tenant, App: p.App}
	payload := struct {
		ID   string     `json:"instruction_id"`
		Edit model.Edit `json:"edit"`
	}{id, in}
	ctx = foundation.WithRequest(ctx, rid, p.Subject)
	return s.Commands.Execute(ctx, scope, "payout-edit", key, payload, func(tx db.Executor) (foundation.CommandResult, error) {
		i, err := s.Repo.Get(ctx, tx, scope, id, true)
		if err != nil {
			return foundation.CommandResult{}, err
		}
		if i.Revision != in.Revision {
			return foundation.CommandResult{}, errors.New("REVISION_CONFLICT")
		}
		if i.Status != "DRAFT" || in.Amount.Currency != i.Amount.Currency {
			return foundation.CommandResult{}, errors.New("STATE_CONFLICT")
		}
		i.Amount = in.Amount
		i.Remark = in.Remark
		if err = s.Validate(ctx, tx, scope, i); err != nil {
			return foundation.CommandResult{}, err
		}
		i.Revision++
		if err = s.Repo.Update(ctx, tx, scope, i, in.Revision); err != nil {
			return foundation.CommandResult{}, err
		}
		return response.Result(200, id, rid, i)
	})
}
func (s Service) Act(ctx context.Context, p identity.Principal, action string, in model.Action, key, rid string) (foundation.CommandResult, error) {
	if (action != "RELEASE" && action != "CANCEL") || !foundation.ValidID(in.Reason) || !model.ValidRemark(in.Remark) {
		return foundation.CommandResult{}, foundation.ErrValidation
	}
	scope := foundation.Scope{Tenant: p.Tenant, App: p.App}
	payload := struct {
		Action string       `json:"action"`
		Item   model.Action `json:"item"`
	}{action, in}
	ctx = foundation.WithRequest(ctx, rid, p.Subject)
	return s.Commands.Execute(ctx, scope, "payout-action", key, payload, func(tx db.Executor) (foundation.CommandResult, error) {
		i, err := s.Repo.Get(ctx, tx, scope, in.ID, true)
		if err != nil {
			return foundation.CommandResult{}, err
		}
		if i.Revision != in.Revision {
			return foundation.CommandResult{}, errors.New("REVISION_CONFLICT")
		}
		if action == "RELEASE" {
			if i.Status != "DRAFT" {
				return foundation.CommandResult{}, errors.New("STATE_CONFLICT")
			}
			if err = s.Validate(ctx, tx, scope, i); err != nil {
				return foundation.CommandResult{}, err
			}
			i.Status = "QUEUED"
		} else {
			if (i.Status != "DRAFT" && i.Status != "BLOCKED") || i.PaymentID != nil {
				return foundation.CommandResult{}, errors.New("STATE_CONFLICT")
			}
			i.Status = "CANCELLED"
		}
		if in.Remark != "" {
			i.Remark = in.Remark
		}
		i.Revision++
		if err = s.Repo.Update(ctx, tx, scope, i, in.Revision); err != nil {
			return foundation.CommandResult{}, err
		}
		return response.Result(200, i.ID, rid, i)
	})
}
func StableKey(batch, id string) string {
	body, _ := json.Marshal([]string{batch, id})
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}
func (s Service) Recover(ctx context.Context, scope foundation.Scope) error {
	items, err := s.Repo.Queued(ctx, s.Query, scope)
	if err != nil {
		return err
	}
	var first error
	for _, i := range items {
		if e := s.process(ctx, scope, i); e != nil && first == nil {
			first = e
		}
	}
	return first
}
func (s Service) process(ctx context.Context, scope foundation.Scope, i model.Instruction) error {
	p := identity.Principal{Tenant: scope.Tenant, App: scope.App, Subject: "system-disbursement"}
	result, err := s.CreatePayment(ctx, p, i, "payout-"+i.ID)
	if err != nil {
		return err
	}
	if result.Status >= 500 {
		return errors.New("payment dependency unavailable")
	}
	status := "LINKED"
	var pid, code *string
	if result.Status >= 400 {
		status = "BLOCKED"
		var failure struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if json.Unmarshal(result.Body, &failure) != nil || failure.Error.Code == "" {
			return errors.New("invalid payment failure")
		}
		code = &failure.Error.Code
	} else {
		if result.ResourceID == "" {
			return errors.New("missing payment resource")
		}
		id := result.ResourceID
		pid = &id
	}
	return s.UOW.Within(ctx, func(tx db.Executor) error {
		current, err := s.Repo.Get(ctx, tx, scope, i.ID, true)
		if err != nil {
			return err
		}
		if current.Status != "QUEUED" {
			return nil
		}
		old := current.Revision
		current.Status = status
		current.PaymentID = pid
		current.ErrorCode = code
		current.Revision++
		return s.Repo.Update(ctx, tx, scope, current, old)
	})
}
