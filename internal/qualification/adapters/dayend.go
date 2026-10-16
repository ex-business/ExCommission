package adapters

import (
	"context"
	"encoding/json"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	model "github.com/ex-business/ExCommission/internal/qualification/domain"
	"time"
)

// EffectiveAt 只暴露达标代理ID，统计当前已知事实中在业务日结束前达标的会员，不授予计佣资格。
func (Repository) EffectiveAt(ctx context.Context, tx db.Executor, s foundation.Scope, policy, source string, end time.Time) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT state_json FROM qualification_states WHERE tenant_id=? AND app_id=? AND policy_id=? AND source_id=? AND state_json IS NOT NULL`, s.Tenant, s.App, policy, source)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var raw []byte
		var state model.Status
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &state); err != nil {
			return nil, err
		}
		if state.Effective && state.QualifiedAt != nil && state.QualifiedAt.Before(end) {
			ids = append(ids, state.AgentID)
		}
	}
	return ids, rows.Err()
}
