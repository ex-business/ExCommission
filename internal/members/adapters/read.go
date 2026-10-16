package adapters

import (
	"context"
	"encoding/json"
	model "github.com/ex-business/ExCommission/internal/members/domain"
	milestone "github.com/ex-business/ExCommission/internal/milestones/domain"
	plan "github.com/ex-business/ExCommission/internal/plans/domain"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"time"
)

func (Repository) Rewards(ctx context.Context, tx db.Executor, s foundation.Scope, agent, after string, limit int, at time.Time) ([]model.Reward, error) {
	rows, err := tx.QueryContext(ctx, `SELECT config FROM milestone_programs WHERE tenant_id=? AND app_id=? AND program_id>? ORDER BY program_id LIMIT ?`, s.Tenant, s.App, after, limit+1)
	if err != nil {
		return nil, err
	}
	programs := []milestone.Program{}
	for rows.Next() {
		var body []byte
		var p milestone.Program
		if err = rows.Scan(&body); err != nil {
			rows.Close()
			return nil, err
		}
		if err = json.Unmarshal(body, &p); err != nil {
			rows.Close()
			return nil, err
		}
		programs = append(programs, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := []model.Reward{}
	for _, p := range programs {
		v := model.Reward{ID: p.ID, Name: p.Name, Starts: p.Starts, Ends: p.Ends, Active: !at.Before(p.Starts) && at.Before(p.Ends), Conditions: []model.RewardCondition{}}
		for _, condition := range p.Conditions {
			v.Conditions = append(v.Conditions, model.RewardCondition{Metric: model.FriendlyMetric(condition.Metric), Unit: condition.Unit, Threshold: condition.Threshold})
		}
		if err = tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(JSON_EXTRACT(CONVERT(progress USING utf8mb4),'$.qualified_at') IS NOT NULL AND JSON_TYPE(JSON_EXTRACT(CONVERT(progress USING utf8mb4),'$.qualified_at'))<>'NULL'),0),COALESCE(SUM(task_id IS NOT NULL),0) FROM milestone_members WHERE tenant_id=? AND app_id=? AND program_id=? AND JSON_UNQUOTE(JSON_EXTRACT(CONVERT(progress USING utf8mb4),'$.beneficiary_agent_id'))=?`, s.Tenant, s.App, p.ID, agent).Scan(&v.Qualified, &v.Triggered); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}
func (Repository) Rules(ctx context.Context, tx db.Executor, s foundation.Scope, planID string, at time.Time) ([]model.Rules, error) {
	rows, err := tx.QueryContext(ctx, `SELECT config FROM plan_versions WHERE tenant_id=? AND app_id=? AND (?='' OR plan_id=?) AND event_type<>'MILESTONE_REACHED' AND effective_from<=? AND (effective_to IS NULL OR effective_to>?) ORDER BY plan_id LIMIT 100`, s.Tenant, s.App, planID, planID, at, at)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Rules{}
	for rows.Next() {
		var body []byte
		var c plan.Config
		if err = rows.Scan(&body); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(body, &c); err != nil {
			return nil, err
		}
		out = append(out, model.PublicRules(c))
	}
	return out, rows.Err()
}
