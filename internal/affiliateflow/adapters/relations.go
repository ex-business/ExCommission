package adapters

import (
	"context"
	"errors"
	affiliate "github.com/ex-business/ExCommission/internal/affiliate/domain"
	agents "github.com/ex-business/ExCommission/internal/agents/adapters"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"time"
)

// Relations 仅适配旧模块的已审批关系历史；旧节点仍占物理层级，但不会因被读取而获得新模块权益。
type Relations struct{}

func (Relations) Close(ctx context.Context, tx db.Executor, s foundation.Scope, at time.Time) ([]affiliate.Registration, error) {
	rows, e := tx.QueryContext(ctx, `SELECT a.user_id,a.created_at,COALESCE(p.user_id,'') FROM agent_accounts a JOIN agent_relations r ON r.tenant_id=a.tenant_id AND r.app_id=a.app_id AND r.agent_id=a.agent_id AND r.effective_from<=? AND (r.effective_to IS NULL OR r.effective_to>?) LEFT JOIN agent_accounts p ON p.tenant_id=r.tenant_id AND p.app_id=r.app_id AND p.agent_id=r.parent_id WHERE a.tenant_id=? AND a.app_id=? AND a.created_at<=? ORDER BY a.user_id`, at.UTC(), at.UTC(), s.Tenant, s.App, at.UTC())
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []affiliate.Registration{}
	for rows.Next() {
		var r affiliate.Registration
		if e = rows.Scan(&r.UserID, &r.At, &r.ParentID); e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func (Relations) Attributions(ctx context.Context, tx db.Executor, s foundation.Scope, facts []affiliate.Fact, start, end time.Time) ([]affiliate.Attribution, error) {
	rows, e := tx.QueryContext(ctx, `SELECT agent_id,user_id FROM agent_accounts WHERE tenant_id=? AND app_id=?`, s.Tenant, s.App)
	if e != nil {
		return nil, e
	}
	byAgent := map[string]string{}
	byUser := map[string]string{}
	for rows.Next() {
		var agent, user string
		if e = rows.Scan(&agent, &user); e != nil {
			rows.Close()
			return nil, e
		}
		byAgent[agent] = user
		byUser[user] = agent
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	out := []affiliate.Attribution{}
	for _, f := range facts {
		if f.Kind != "BET_SETTLED" || f.At.Before(start) || !f.At.Before(end) {
			continue
		}
		agent, ok := byUser[f.UserID]
		if !ok {
			return nil, errors.New("RELATION_HISTORY_MISSING")
		}
		chain, _, e := (agents.Repository{}).Ancestors(ctx, tx, s, agent, f.At, 0, 3)
		if e != nil {
			return nil, e
		}
		row := affiliate.Attribution{FactID: f.ID}
		for _, node := range chain {
			user, ok := byAgent[node.AgentID]
			if !ok {
				return nil, errors.New("RELATION_HISTORY_MISSING")
			}
			row.Ancestors[node.Depth-1] = user
		}
		out = append(out, row)
	}
	return out, nil
}
