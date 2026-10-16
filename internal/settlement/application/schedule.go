package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	identity "github.com/ex-business/ExCommission/internal/identity/domain"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	model "github.com/ex-business/ExCommission/internal/settlement/domain"
	"github.com/ex-business/ExCommission/internal/settlement/ports"
	"time"
)

type Scheduler struct {
	UOW       db.UnitOfWork
	Service   Service
	Repo      ports.ScheduleRepository
	History   ports.RequestHistory
	Calendars ports.CalendarSelector
	Now       func() time.Time
	ID        func() string
	Audit     func(context.Context, db.Executor, foundation.Scope, string, string, string) error
}

func (s Scheduler) RunOnce(ctx context.Context, scope foundation.Scope) (bool, error) {
	if !scope.Valid() || s.Audit == nil {
		return false, foundation.ErrValidation
	}
	at := s.Now().UTC()
	rid := s.ID()
	// This transaction writes its own audit only for a completed period; suppress
	// generic mutation auditing of no-op polling locks.
	ctx = foundation.WithOperation(foundation.WithScope(foundation.WithRequest(ctx, rid, "system-settlement"), scope), "settlement.daily", true)
	// Persist first registration separately so even a failed first due run cannot
	// silently move its starting period forward when the worker restarts tomorrow.
	if _, err := s.Repo.Schedule(ctx, s.Service.Query, scope); errors.Is(err, sql.ErrNoRows) {
		if err = s.UOW.Within(ctx, func(tx db.Executor) error {
			if err := s.Service.Repo.Lock(ctx, tx, scope); err != nil {
				return err
			}
			_, err := s.Repo.LockSchedule(ctx, tx, scope, model.InitialPeriod(at), at)
			return err
		}); err != nil {
			return false, err
		}
	} else if err != nil {
		return false, err
	}
	done := false
	err := s.UOW.Within(ctx, func(tx db.Executor) error {
		if err := s.Service.Repo.Lock(ctx, tx, scope); err != nil {
			return err
		}
		state, err := s.Repo.LockSchedule(ctx, tx, scope, model.InitialPeriod(at), at)
		if err != nil {
			return err
		}
		due, err := model.DueAt(state.NextPeriod)
		if err != nil {
			return err
		}
		if at.Before(due) {
			return nil
		}
		period, err := time.Parse("2006-01-02", state.NextPeriod)
		if err != nil {
			return err
		}
		calendar, err := s.Calendars.CalendarForPeriod(ctx, tx, scope, period)
		if err != nil {
			return err
		}
		if s.Service.Calendar != nil {
			if err = s.Service.Calendar.ValidateCalendar(ctx, tx, scope, calendar, period); err != nil {
				return err
			}
		}
		result, err := s.Service.runTx(ctx, tx, scope, RunInput{Period: state.NextPeriod, Calendar: calendar, AgentIDs: []string{}}, period, map[string]bool{}, rid)
		if err != nil {
			return err
		}
		var envelope struct {
			Data struct {
				RunID      string            `json:"run_id"`
				Statements []model.Statement `json:"statements"`
			} `json:"data"`
		}
		if json.Unmarshal(result.Body, &envelope) != nil || envelope.Data.RunID == "" {
			return errors.New("invalid scheduled settlement result")
		}
		output := model.ScheduledResult{Period: state.NextPeriod, RunID: envelope.Data.RunID, StatementIDs: []string{}, ApprovalIDs: []string{}, Completed: at}
		maker := identity.Principal{Tenant: scope.Tenant, App: scope.App, Subject: "system-settlement"}
		for _, statement := range envelope.Data.Statements {
			output.StatementIDs = append(output.StatementIDs, statement.ID)
			if statement.Status != "DRAFT" {
				continue
			}
			exists, err := s.History.HasRequest(ctx, tx, scope, "STATEMENT", statement.ID)
			if err != nil {
				return err
			}
			if exists {
				continue
			}
			response, err := s.Service.submitTx(ctx, tx, maker, statement.ID, Submission{Revision: statement.Revision, Hash: statement.Hash}, rid)
			if err != nil {
				return err
			}
			var submitted struct {
				Data struct {
					ID string `json:"approval_id"`
				} `json:"data"`
			}
			if json.Unmarshal(response.Body, &submitted) != nil || submitted.Data.ID == "" {
				return errors.New("invalid scheduled approval result")
			}
			output.ApprovalIDs = append(output.ApprovalIDs, submitted.Data.ID)
		}
		next, err := model.NextPeriod(state.NextPeriod)
		if err != nil {
			return err
		}
		if err = s.Repo.Advance(ctx, tx, scope, next, output, at); err != nil {
			return err
		}
		if err = s.Audit(ctx, tx, scope, "settlement.daily", state.NextPeriod, "SCHEDULED"); err != nil {
			return err
		}
		done = true
		return nil
	})
	return done && err == nil, err
}
