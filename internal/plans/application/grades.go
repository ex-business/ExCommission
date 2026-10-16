package application

import (
	"context"
	"encoding/json"
	"errors"
	agent "github.com/ex-business/ExCommission/internal/agents/domain"
	agents "github.com/ex-business/ExCommission/internal/agents/ports"
	identity "github.com/ex-business/ExCommission/internal/identity/domain"
	"github.com/ex-business/ExCommission/internal/plans/domain"
	"github.com/ex-business/ExCommission/internal/plans/ports"
	response "github.com/ex-business/ExCommission/internal/platform/application"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	approval "github.com/ex-business/ExCommission/internal/workflow/domain"
	"sort"
	"time"
)

type GradeService struct {
	Service
	Grades      agents.Grades
	Subjects    agents.GradeSubjects
	Evaluations ports.Evaluations
	Statistics  ports.StatisticsProvider
}

func (s GradeService) RequestChange(ctx context.Context, p identity.Principal, input agent.GradeChange, key, request string) (foundation.CommandResult, error) {
	if input.Reason == "" || (input.Eligibility != "ELIGIBLE" && input.Eligibility != "INELIGIBLE") {
		return foundation.CommandResult{}, foundation.ErrValidation
	}
	scope := foundation.Scope{Tenant: p.Tenant, App: p.App}
	ctx = foundation.WithRequest(ctx, request, p.Subject)
	return s.Commands.Execute(ctx, scope, "grade-change-request", key, input, func(tx db.Executor) (foundation.CommandResult, error) {
		version, err := s.Repo.AtPlan(ctx, tx, scope, input.PlanID, s.Now())
		if err != nil {
			return foundation.CommandResult{}, err
		}
		exists := false
		for _, grade := range version.Config.Grades {
			if grade.ID == input.GradeID {
				exists = true
			}
		}
		if !exists {
			return foundation.CommandResult{}, foundation.ErrValidation
		}
		revision, err := s.Grades.GradeRevision(ctx, tx, scope, input.AgentID, input.PlanID)
		if err != nil {
			return foundation.CommandResult{}, err
		}
		if revision != input.Revision {
			return foundation.CommandResult{}, errors.New("REVISION_CONFLICT")
		}
		payload, err := json.Marshal(input)
		if err != nil {
			return foundation.CommandResult{}, err
		}
		payload, err = foundation.CanonicalJSON(payload)
		if err != nil {
			return foundation.CommandResult{}, err
		}
		id := s.ID()
		if err = s.Workflow.Create(ctx, tx, scope, approval.Request{ID: id, Type: "GRADE", Applicant: p.Subject, ObjectRef: input.AgentID, ObjectHash: hash(payload), Payload: payload}, input.Reason, s.Now()); err != nil {
			return foundation.CommandResult{}, err
		}
		return response.Result(200, id, request, map[string]string{"approval_id": id})
	})
}

type EvaluationRequest struct {
	PlanID   string    `json:"plan_id"`
	Period   string    `json:"calendar_period"`
	Cutoff   time.Time `json:"cutoff"`
	Previous *string   `json:"expected_previous_evaluation"`
}

func (s GradeService) Evaluate(ctx context.Context, p identity.Principal, input EvaluationRequest, key, request string) (foundation.CommandResult, error) {
	scope := foundation.Scope{Tenant: p.Tenant, App: p.App}
	ctx = foundation.WithRequest(ctx, request, p.Subject)
	if previous, found, err := s.Commands.Lookup(ctx, scope, "grade-evaluate", key, input); err != nil {
		return foundation.CommandResult{}, err
	} else if found {
		return previous, nil
	}
	now := s.Now().UTC()
	effective := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	period := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -1, 0).Format("2006-01")
	if input.Period != period || input.Cutoff.After(effective) || input.Cutoff.IsZero() {
		return foundation.CommandResult{}, foundation.ErrValidation
	}
	if s.Statistics == nil {
		return foundation.CommandResult{}, errors.New("UNSUPPORTED_CAPABILITY")
	}
	frozen, err := s.Statistics.Freeze(ctx, scope, input.PlanID, input.Period, input.Cutoff)
	if err != nil {
		return foundation.CommandResult{}, err
	}
	return s.Commands.Execute(ctx, scope, "grade-evaluate", key, input, func(tx db.Executor) (foundation.CommandResult, error) {
		draft, err := s.Repo.Read(ctx, tx, scope, input.PlanID, true)
		if err != nil {
			return foundation.CommandResult{}, err
		}
		version, err := s.Repo.AtPlan(ctx, tx, scope, input.PlanID, effective)
		if err != nil {
			return foundation.CommandResult{}, err
		}
		if version.Config.AgentPolicy.GradeWindow != "PREVIOUS_CALENDAR_MONTH" {
			return foundation.CommandResult{}, errors.New("UNSUPPORTED_CAPABILITY")
		}
		_ = draft
		previous, err := s.Evaluations.Latest(ctx, tx, scope, input.PlanID)
		if err != nil {
			return foundation.CommandResult{}, err
		}
		expected := ""
		if input.Previous != nil {
			expected = *input.Previous
		}
		if expected != previous {
			return foundation.CommandResult{}, errors.New("REVISION_CONFLICT")
		}
		if s.Subjects == nil {
			return foundation.CommandResult{}, errors.New("UNSUPPORTED_CAPABILITY")
		}
		subjects, err := s.Subjects.SubjectsAt(ctx, tx, scope, effective)
		if err != nil {
			return foundation.CommandResult{}, err
		}
		expectedIDs := map[string]bool{}
		for _, id := range subjects {
			included := version.Config.Scope.Type == "ALL_ACTIVE_AGENTS"
			for _, allowed := range version.Config.Scope.AgentIDs {
				if allowed == id {
					included = true
				}
			}
			if included {
				expectedIDs[id] = true
			}
		}
		if len(expectedIDs) != len(frozen.Agents) {
			return foundation.CommandResult{}, errors.New("UNKNOWN_FACT")
		}
		for id := range expectedIDs {
			if _, exists := frozen.Agents[id]; !exists {
				return foundation.CommandResult{}, errors.New("UNKNOWN_FACT")
			}
		}
		id := s.ID()
		ids := make([]string, 0, len(frozen.Agents))
		for agentID := range frozen.Agents {
			ids = append(ids, agentID)
		}
		sort.Strings(ids)
		results := map[string]string{}
		for _, agentID := range ids {
			grade, err := domain.SelectGrade(version.Config.Grades, frozen.Agents[agentID], version.Config.Currency)
			if err != nil {
				return foundation.CommandResult{}, err
			}
			revision, err := s.Grades.GradeRevision(ctx, tx, scope, agentID, input.PlanID)
			if err != nil {
				return foundation.CommandResult{}, err
			}
			state, _, err := s.Grades.GradeAt(ctx, tx, scope, agentID, input.PlanID, effective)
			if err != nil {
				return foundation.CommandResult{}, err
			}
			if err = s.Grades.ApplyGrade(ctx, tx, scope, agent.GradeChange{AgentID: agentID, PlanID: input.PlanID, GradeID: grade, Eligibility: state.Eligibility, Revision: revision}, "AUTO", id, effective); err != nil {
				return foundation.CommandResult{}, err
			}
			results[agentID] = grade
		}
		body, err := json.Marshal(results)
		if err != nil {
			return foundation.CommandResult{}, err
		}
		if err = s.Evaluations.Save(ctx, tx, scope, id, frozen, body, effective); err != nil {
			return foundation.CommandResult{}, err
		}
		return response.Result(200, id, request, map[string]any{"evaluation_id": id, "source_watermark": frozen.Watermark, "effective_from": effective, "grades": results})
	})
}
