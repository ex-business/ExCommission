package application

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	agents "github.com/ex-business/ExCommission/internal/agents/ports"
	identity "github.com/ex-business/ExCommission/internal/identity/domain"
	"github.com/ex-business/ExCommission/internal/milestones/domain"
	"github.com/ex-business/ExCommission/internal/milestones/ports"
	plan "github.com/ex-business/ExCommission/internal/plans/domain"
	plans "github.com/ex-business/ExCommission/internal/plans/ports"
	response "github.com/ex-business/ExCommission/internal/platform/application"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	users "github.com/ex-business/ExCommission/internal/users/ports"
	approval "github.com/ex-business/ExCommission/internal/workflow/domain"
	workflow "github.com/ex-business/ExCommission/internal/workflow/ports"
	"math/big"
	"time"
)

type Request struct {
	Program domain.Program `json:"program"`
	Reason  string         `json:"reason"`
}
type Service struct {
	Commands    db.Commands
	Query       db.Executor
	Repo        ports.Repository
	Workflow    workflow.Repository
	Plans       plans.Repository
	Users       users.BusinessUsers
	Accounts    agents.HistoricalAccounts
	Now         func() time.Time
	ID          func() string
	AllowSource func(context.Context, db.Executor, foundation.Scope, string) error
	Dispatch    func(context.Context, db.Executor, foundation.Scope, domain.Program, domain.Progress, domain.Observation, plan.Version, time.Time) (string, error)
}

func Hash(body []byte) string {
	canonical, e := foundation.BusinessCanonicalJSON(body)
	if e != nil {
		return ""
	}
	h := sha256.Sum256(canonical)
	return hex.EncodeToString(h[:])
}
func ValidateReward(v plan.Version) error {
	c := v.Config
	if c.Route.EventType != "MILESTONE_REACHED" || len(c.Rules) != 1 {
		return errors.New("UNSUPPORTED_CAPABILITY")
	}
	r := c.Rules[0]
	amount, ok := new(big.Int).SetString(r.Parameters.AmountMinor, 10)
	if r.Algorithm != "FIXED" || r.Recipient != "DIRECT" || r.Condition.Logic != "AND" || len(r.Condition.Children) != 0 || !ok || amount.Sign() <= 0 {
		return errors.New("UNSUPPORTED_CAPABILITY")
	}

	// The configured one-time fixed reward must fit every configured budget/cap.
	for _, limit := range []*string{c.EventBudget, r.BeneficiaryCap, r.EventCap} {
		if limit == nil {
			continue
		}
		cap, valid := new(big.Int).SetString(*limit, 10)
		if !valid || cap.Cmp(amount) < 0 {
			return errors.New("BUDGET_EXCEEDED")
		}
	}
	return nil
}
func (s Service) reward(ctx context.Context, tx db.Executor, scope foundation.Scope, p domain.Program, at time.Time) (plan.Version, error) {
	v, e := s.Plans.AtPlan(ctx, tx, scope, p.PlanID, at)
	if e != nil {
		return v, e
	}
	e = ValidateReward(v)
	return v, e
}
func (s Service) Request(ctx context.Context, p identity.Principal, in Request, key, rid string) (foundation.CommandResult, error) {
	if err := in.Program.Normalize(); err != nil {
		return foundation.CommandResult{}, err
	}
	if !foundation.ValidID(in.Reason) {
		return foundation.CommandResult{}, foundation.ErrValidation
	}
	scope := foundation.Scope{Tenant: p.Tenant, App: p.App}
	ctx = foundation.WithRequest(ctx, rid, p.Subject)
	return s.Commands.Execute(ctx, scope, "milestone-program-request", key, in, func(tx db.Executor) (foundation.CommandResult, error) {
		if !in.Program.Starts.After(s.Now()) {
			return foundation.CommandResult{}, foundation.ErrValidation
		}
		if err := s.AllowSource(ctx, tx, scope, in.Program.Source); err != nil {
			return foundation.CommandResult{}, err
		}
		if _, err := s.reward(ctx, tx, scope, in.Program, in.Program.Starts); err != nil {
			return foundation.CommandResult{}, err
		}
		if _, err := s.Repo.Program(ctx, tx, scope, in.Program.ID); err == nil {
			return foundation.CommandResult{}, errors.New("STATE_CONFLICT")
		} else if !errors.Is(err, sql.ErrNoRows) {
			return foundation.CommandResult{}, err
		}
		body, _ := json.Marshal(in)
		id := s.ID()
		if err := s.Workflow.Create(ctx, tx, scope, approval.Request{ID: id, Type: "MILESTONE_PROGRAM", Applicant: p.Subject, ObjectRef: in.Program.ID, ObjectHash: Hash(body), Payload: body, Revision: 0}, in.Reason, s.Now()); err != nil {
			return foundation.CommandResult{}, err
		}
		return response.Result(202, id, rid, map[string]any{"approval_id": id, "program_id": in.Program.ID, "object_hash": Hash(body), "revision": 0})
	})
}
func (s Service) Activate(ctx context.Context, tx db.Executor, scope foundation.Scope, r approval.Request, at time.Time) error {
	var in Request
	if json.Unmarshal(r.Payload, &in) != nil || Hash(r.Payload) != r.ObjectHash || in.Program.ID != r.ObjectRef || in.Program.Normalize() != nil {
		return foundation.ErrValidation
	}
	if !in.Program.Starts.After(at) {
		return errors.New("STALE_APPROVAL")
	}
	if err := s.AllowSource(ctx, tx, scope, in.Program.Source); err != nil {
		return err
	}
	if _, err := s.reward(ctx, tx, scope, in.Program, in.Program.Starts); err != nil {
		return err
	}
	body, _ := json.Marshal(in.Program)
	return s.Repo.Activate(ctx, tx, scope, ports.ProgramRecord{Program: in.Program, Hash: Hash(body), ApprovalID: r.ID}, at)
}
func (s Service) Observe(ctx context.Context, p identity.Principal, o domain.Observation, key, rid string) (foundation.CommandResult, error) {
	if err := o.Normalize(); err != nil {
		return foundation.CommandResult{}, err
	}
	if o.At.After(s.Now()) {
		return foundation.CommandResult{}, foundation.ErrValidation
	}
	scope := foundation.Scope{Tenant: p.Tenant, App: p.App}
	ctx = foundation.WithRequest(ctx, rid, p.Subject)
	body, _ := json.Marshal(o)
	hash := Hash(body)
	return s.Commands.Execute(ctx, scope, "milestone-observation", key, o, func(tx db.Executor) (foundation.CommandResult, error) {
		if err := s.AllowSource(ctx, tx, scope, o.Source); err != nil {
			return foundation.CommandResult{}, err
		}
		record, err := s.Repo.Program(ctx, tx, scope, o.ProgramID)
		if err != nil {
			return foundation.CommandResult{}, err
		}
		program := record.Program
		if program.Source != o.Source {
			return foundation.CommandResult{}, identity.ErrPermission
		}
		cond, ok := program.Condition(o.Metric)
		if !ok || cond.Unit != o.Unit || o.At.Before(program.Starts) || !o.At.Before(program.Ends) {
			return foundation.CommandResult{}, foundation.ErrValidation
		}
		user, err := s.Users.Resolve(ctx, tx, scope, o.Source, o.ExternalUser)
		if err != nil {
			return foundation.CommandResult{}, err
		}
		account, err := s.Accounts.AccountAt(ctx, tx, scope, o.Beneficiary, o.At)
		if err != nil {
			return foundation.CommandResult{}, err
		}
		if account.UserID == user {
			return foundation.CommandResult{}, identity.ErrPermission
		}
		member, err := s.Repo.LockMember(ctx, tx, scope, program.ID, domain.Progress{UserID: user, Beneficiary: o.Beneficiary, BoundAt: o.At, BindingEvidence: o.Evidence, Facts: map[string]domain.Fact{}}, s.Now())
		if err != nil {
			return foundation.CommandResult{}, err
		}
		if member.Beneficiary != o.Beneficiary {
			return foundation.CommandResult{}, errors.New("STATE_CONFLICT")
		}
		saved, old, err := s.Repo.Receipt(ctx, tx, scope, o.Source, o.EventID)
		if err == nil {
			if saved != hash {
				return foundation.CommandResult{}, errors.New("IDEMPOTENCY_CONFLICT")
			}
			return old, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return foundation.CommandResult{}, err
		}
		outcome := "UPDATED"
		value, _ := foundation.ParseDecimal(o.Value)
		if previous, known := member.Facts[o.Metric]; known {
			prior, _ := foundation.ParseDecimal(previous.Value)
			cmp := value.Atoms().Cmp(prior.Atoms())
			switch {
			case o.At.Equal(previous.At) && cmp != 0:
				return foundation.CommandResult{}, errors.New("STATE_CONFLICT")
			case cmp < 0:
				outcome = "STALE_NOOP"
			case cmp == 0:
				outcome = "NO_CHANGE"
			case !o.At.After(previous.At):
				return foundation.CommandResult{}, errors.New("STATE_CONFLICT")
			}
		}
		if outcome == "UPDATED" {
			member.Facts[o.Metric] = domain.Fact{Value: o.Value, At: o.At, Evidence: o.Evidence}
		}
		reached, at := program.Reached(member.Facts)
		if member.BoundAt.After(at) {
			at = member.BoundAt
		}
		if reached && member.TaskID == nil {
			version, err := s.reward(ctx, tx, scope, program, at)
			if err != nil {
				return foundation.CommandResult{}, err
			}
			id, err := s.Dispatch(ctx, tx, scope, program, member, o, version, at)
			if err != nil {
				return foundation.CommandResult{}, err
			}
			member.TaskID = &id
			member.QualifiedAt = &at
		}
		if err = s.Repo.SaveMember(ctx, tx, scope, program.ID, member, s.Now()); err != nil {
			return foundation.CommandResult{}, err
		}
		status := "WAITING"
		if member.TaskID != nil {
			status = "TRIGGERED"
		}
		result, err := response.Result(202, user, rid, map[string]any{"program_id": program.ID, "user_id": user, "observation_status": outcome, "status": status, "task_id": member.TaskID, "qualified_at": member.QualifiedAt})
		if err != nil {
			return result, err
		}
		if err = s.Repo.SaveReceipt(ctx, tx, scope, o, body, hash, result, s.Now()); err != nil {
			return foundation.CommandResult{}, err
		}
		return result, nil
	})
}
