package application

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/ex-business/ExCommission/internal/calculation/ports"
	identity "github.com/ex-business/ExCommission/internal/identity/domain"
	money "github.com/ex-business/ExCommission/internal/payments/domain"
	response "github.com/ex-business/ExCommission/internal/platform/application"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	hash "github.com/ex-business/ExCommission/internal/workflow/adapters"
	approval "github.com/ex-business/ExCommission/internal/workflow/domain"
	workflow "github.com/ex-business/ExCommission/internal/workflow/ports"
	"math/big"
	"time"
)

type RecoveryInput struct {
	DebtID   string      `json:"debt_id"`
	Method   string      `json:"method"`
	Amount   money.Money `json:"amount"`
	Evidence string      `json:"evidence_ref"`
	Reason   string      `json:"reason"`
	Revision uint64      `json:"expected_revision"`
}
type recoveryPayload struct {
	Input        RecoveryInput `json:"input"`
	EvidenceHash string        `json:"evidence_hash"`
}
type RecoveryService struct {
	Commands db.Commands
	Query    db.Executor
	Ledger   ports.RecoveryLedger
	Workflow workflow.Repository
	Evidence ports.CashEvidence
	Now      func() time.Time
	ID       func() string
}

func (s RecoveryService) Request(ctx context.Context, p identity.Principal, input RecoveryInput, key, request string) (foundation.CommandResult, error) {
	scope := foundation.Scope{Tenant: p.Tenant, App: p.App}
	if saved, found, err := s.Commands.Lookup(ctx, scope, "recovery-request", key, input); err != nil || found {
		return saved, err
	}
	n, ok := new(big.Int).SetString(input.Amount.Minor, 10)
	if !ok || n.Sign() <= 0 || n.String() != input.Amount.Minor || len(input.Amount.Minor) > 38 || !foundation.ValidID(input.Evidence) || !foundation.ValidID(input.Reason) || (input.Method != "CASH_CONFIRMED" && input.Method != "FUTURE_OFFSET") {
		return foundation.CommandResult{}, foundation.ErrValidation
	}
	e, err := s.Ledger.Get(ctx, s.Query, scope, input.DebtID, false)
	if err != nil {
		return foundation.CommandResult{}, err
	}
	if e.Currency != input.Amount.Currency {
		return foundation.CommandResult{}, errors.New("UNIT_MISMATCH")
	}
	evidenceHash := ""
	if input.Method == "CASH_CONFIRMED" {
		if s.Evidence == nil {
			return foundation.CommandResult{}, errors.New("UNSUPPORTED_CAPABILITY")
		}
		evidenceHash, err = s.Evidence.Verify(ctx, scope, input.Evidence, e.AgentID, e.Currency, input.Amount.Minor)
		if err != nil {
			return foundation.CommandResult{}, err
		}
	}
	return s.Commands.Execute(ctx, scope, "recovery-request", key, input, func(tx db.Executor) (foundation.CommandResult, error) {
		e, err := s.Ledger.Get(ctx, tx, scope, input.DebtID, true)
		if err != nil {
			return foundation.CommandResult{}, err
		}
		if e.Revision != input.Revision {
			return foundation.CommandResult{}, errors.New("REVISION_CONFLICT")
		}
		debt, _ := new(big.Int).SetString(e.Debt, 10)
		if debt.Cmp(n) < 0 {
			return foundation.CommandResult{}, errors.New("INSUFFICIENT_BALANCE")
		}
		body, _ := json.Marshal(recoveryPayload{input, evidenceHash})
		a := approval.Request{ID: s.ID(), Type: "RECOVERY", Applicant: p.Subject, ObjectRef: input.DebtID, ObjectHash: hash.Hash(body), Payload: body}
		if err = s.Workflow.Create(ctx, tx, scope, a, input.Reason, s.Now()); err != nil {
			return foundation.CommandResult{}, err
		}
		return response.Result(202, a.ID, request, map[string]any{"approval_id": a.ID, "object_hash": a.ObjectHash, "revision": 0})
	})
}
func (s RecoveryService) Execute(ctx context.Context, tx db.Executor, scope foundation.Scope, a approval.Request, now time.Time) error {
	var payload recoveryPayload
	if err := json.Unmarshal(a.Payload, &payload); err != nil {
		return err
	}
	v := payload.Input
	if v.Method == "CASH_CONFIRMED" && payload.EvidenceHash == "" {
		return errors.New("UNSUPPORTED_CAPABILITY")
	}
	return s.Ledger.Recover(ctx, tx, scope, v.DebtID, v.Revision, v.Method, v.Amount.Minor, a.ID, v.Evidence, now)
}
