package adapters

import (
	"context"
	"encoding/json"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	model "github.com/ex-business/ExCommission/internal/qualification/domain"
	"time"
)

// EffectiveStatisticsAt 按不可变规则筛选首次达标时间；只统计当前已知事实，不授予资格或重置累计。
func (r Repository) EffectiveStatisticsAt(ctx context.Context, tx db.Executor, s foundation.Scope, policy, source string, end time.Time) (model.StatisticsResult, error) {
	return r.statisticsAt(ctx, tx, s, policy, source, "", end)
}
func (r Repository) DirectStatisticsAt(ctx context.Context, tx db.Executor, s foundation.Scope, policy, source, parent string, end time.Time) (model.StatisticsResult, error) {
	if !foundation.ValidID(parent) {
		return model.StatisticsResult{}, foundation.ErrValidation
	}
	return r.statisticsAt(ctx, tx, s, policy, source, parent, end)
}

// statisticsAt 使用注册时首次邀请归属筛选直属推荐，不把二级下线或迁移后的非本人邀请计入奖励。
func (r Repository) statisticsAt(ctx context.Context, tx db.Executor, s foundation.Scope, policy, source, parent string, end time.Time) (model.StatisticsResult, error) {
	rules, err := r.Policy(ctx, tx, s, policy)
	if err != nil {
		return model.StatisticsResult{}, err
	}
	if rules.Source != source {
		return model.StatisticsResult{}, model.ErrValidation
	}
	window, err := model.WindowAt(rules.Statistics, end)
	if err != nil {
		return model.StatisticsResult{}, err
	}
	result := model.StatisticsResult{Window: window, AgentIDs: []string{}}
	rows, err := tx.QueryContext(ctx, `SELECT state_json FROM qualification_states WHERE tenant_id=? AND app_id=? AND policy_id=? AND source_id=? AND state_json IS NOT NULL ORDER BY external_user_id`, s.Tenant, s.App, policy, source)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var raw []byte
		var state model.Status
		if err = rows.Scan(&raw); err != nil {
			return result, err
		}
		if err = json.Unmarshal(raw, &state); err != nil {
			return result, err
		}
		if (parent == "" || state.ParentID != nil && *state.ParentID == parent) && state.Effective && state.QualifiedAt != nil && window.Contains(*state.QualifiedAt) && !seen[state.AgentID] {
			result.AgentIDs = append(result.AgentIDs, state.AgentID)
			seen[state.AgentID] = true
		}
	}
	result.Count = len(result.AgentIDs)
	return result, rows.Err()
}
