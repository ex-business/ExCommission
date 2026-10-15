package adapters

import (
	"context"
	"database/sql"
	"errors"
	"github.com/ex-business/ExCommission/internal/agents/domain"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	"github.com/ex-business/ExCommission/internal/platform/ports"
	"time"
)

func (r Repository) GradeRevision(ctx context.Context, tx ports.Executor, s foundation.Scope, agentID, planID string) (uint64, error) {
	if _, err := r.Revision(ctx, tx, s, agentID); err != nil {
		return 0, err
	}
	var revision uint64
	err := tx.QueryRowContext(ctx, `SELECT revision FROM agent_plan_states WHERE tenant_id=? AND app_id=? AND agent_id=? AND plan_id=?`, s.Tenant, s.App, agentID, planID).Scan(&revision)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return revision, err
}
func (r Repository) GradeAt(ctx context.Context, tx ports.Executor, s foundation.Scope, agentID, planID string, at time.Time) (domain.GradeState, bool, error) {
	if _, err := r.Revision(ctx, tx, s, agentID); err != nil {
		return domain.GradeState{}, false, err
	}
	var created time.Time
	if err := tx.QueryRowContext(ctx, `SELECT created_at FROM agent_accounts WHERE tenant_id=? AND app_id=? AND agent_id=?`, s.Tenant, s.App, agentID).Scan(&created); err != nil {
		return domain.GradeState{}, false, err
	}
	if at.Before(created) {
		return domain.GradeState{}, false, domain.ErrHistory
	}
	var state domain.GradeState
	err := tx.QueryRowContext(ctx, `SELECT grade_id,eligibility,effective_from,effective_to,source_kind,evidence_ref FROM agent_grade_history WHERE tenant_id=? AND app_id=? AND agent_id=? AND plan_id=? AND effective_from<=? AND (effective_to IS NULL OR effective_to>?)`, s.Tenant, s.App, agentID, planID, at.UTC(), at.UTC()).Scan(&state.Grade, &state.Eligibility, &state.From, &state.To, &state.Source, &state.Evidence)
	if errors.Is(err, sql.ErrNoRows) {
		var first *time.Time
		if err = tx.QueryRowContext(ctx, `SELECT MIN(effective_from) FROM agent_grade_history WHERE tenant_id=? AND app_id=? AND agent_id=? AND plan_id=?`, s.Tenant, s.App, agentID, planID).Scan(&first); err != nil {
			return state, false, err
		}
		if first != nil && !at.Before(*first) {
			return state, false, domain.ErrHistory
		}
		return domain.GradeState{Eligibility: "INELIGIBLE", From: created, To: first, Source: "NOT_JOINED", Evidence: "default-ineligible"}, false, nil
	}
	if err != nil {
		return state, false, err
	}
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_grade_history WHERE tenant_id=? AND app_id=? AND agent_id=? AND plan_id=? AND effective_from<=?`, s.Tenant, s.App, agentID, planID, state.From).Scan(&state.Revision)
	return state, true, err
}
func (r Repository) ApplyGrade(ctx context.Context, tx ports.Executor, s foundation.Scope, change domain.GradeChange, source, evidence string, at time.Time) error {
	if change.GradeID == "" || change.PlanID == "" || (change.Eligibility != "ELIGIBLE" && change.Eligibility != "INELIGIBLE") || at.IsZero() {
		return foundation.ErrValidation
	}
	if _, err := r.Revision(ctx, tx, s, change.AgentID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_plan_states VALUES(?,?,?,?,0) ON DUPLICATE KEY UPDATE agent_id=agent_id`, s.Tenant, s.App, change.AgentID, change.PlanID); err != nil {
		return err
	}
	var revision uint64
	if err := tx.QueryRowContext(ctx, `SELECT revision FROM agent_plan_states WHERE tenant_id=? AND app_id=? AND agent_id=? AND plan_id=? FOR UPDATE`, s.Tenant, s.App, change.AgentID, change.PlanID).Scan(&revision); err != nil {
		return err
	}
	if revision != change.Revision {
		return domain.ErrStale
	}
	var previous time.Time
	err := tx.QueryRowContext(ctx, `SELECT effective_from FROM agent_grade_history WHERE tenant_id=? AND app_id=? AND agent_id=? AND plan_id=? AND effective_to IS NULL FOR UPDATE`, s.Tenant, s.App, change.AgentID, change.PlanID).Scan(&previous)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	at = at.UTC().Truncate(time.Microsecond)
	if !previous.IsZero() && !at.After(previous) {
		return foundation.ErrConflict
	}
	if _, err = tx.ExecContext(ctx, `UPDATE agent_grade_history SET effective_to=? WHERE tenant_id=? AND app_id=? AND agent_id=? AND plan_id=? AND effective_to IS NULL`, at, s.Tenant, s.App, change.AgentID, change.PlanID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO agent_grade_history VALUES(?,?,?,?,?,?,?,?,?,NULL)`, s.Tenant, s.App, change.AgentID, change.PlanID, change.GradeID, change.Eligibility, source, evidence, at); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE agent_plan_states SET revision=revision+1 WHERE tenant_id=? AND app_id=? AND agent_id=? AND plan_id=?`, s.Tenant, s.App, change.AgentID, change.PlanID)
	return err
}

func (Repository) SubjectsAt(ctx context.Context, tx ports.Executor, s foundation.Scope, at time.Time) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT a.agent_id FROM agent_accounts a JOIN agent_status_history h ON h.tenant_id=a.tenant_id AND h.app_id=a.app_id AND h.agent_id=a.agent_id WHERE a.tenant_id=? AND a.app_id=? AND a.created_at<=? AND h.effective_from<=? AND (h.effective_to IS NULL OR h.effective_to>?) AND h.status IN ('ACTIVE','FROZEN') ORDER BY a.agent_id`, s.Tenant, s.App, at.UTC(), at.UTC(), at.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (Repository) EligibleGrades(ctx context.Context, tx ports.Executor, s foundation.Scope, planID string, at time.Time) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT h.grade_id FROM agent_grade_history h JOIN agent_accounts a ON a.tenant_id=h.tenant_id AND a.app_id=h.app_id AND a.agent_id=h.agent_id WHERE h.tenant_id=? AND h.app_id=? AND h.plan_id=? AND h.eligibility='ELIGIBLE' AND h.effective_from<=? AND (h.effective_to IS NULL OR h.effective_to>?) AND a.status IN ('ACTIVE','FROZEN')`, s.Tenant, s.App, planID, at.UTC(), at.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	grades := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		grades = append(grades, id)
	}
	return grades, rows.Err()
}
