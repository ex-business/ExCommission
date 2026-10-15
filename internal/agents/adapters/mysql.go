package adapters

import (
	"context"
	"database/sql"
	"errors"
	agent "github.com/ex-business/ExCommission/internal/agents/domain"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	"github.com/ex-business/ExCommission/internal/platform/ports"
	"time"
)

type Repository struct{}

func (Repository) Create(ctx context.Context, tx ports.Executor, s foundation.Scope, id, user, name string, at time.Time) error {
	if !s.Valid() || !foundation.ValidID(id) || !foundation.ValidID(user) || !foundation.ValidID(name) || at.IsZero() {
		return foundation.ErrValidation
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_accounts VALUES(?,?,?,?,?,'ACTIVE',0,?)`, s.Tenant, s.App, id, user, name, at.UTC()); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO agent_relations VALUES(?,?,?,NULL,?,NULL)`, s.Tenant, s.App, id, at.UTC())
	return err
}
func (Repository) Revision(ctx context.Context, tx ports.Executor, s foundation.Scope, id string) (uint64, error) {
	var revision uint64
	err := tx.QueryRowContext(ctx, `SELECT revision FROM agent_accounts WHERE tenant_id=? AND app_id=? AND agent_id=?`, s.Tenant, s.App, id).Scan(&revision)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, agent.ErrNotFound
	}
	return revision, err
}
func (r Repository) Move(ctx context.Context, tx ports.Executor, s foundation.Scope, change agent.RelationChange, at time.Time) error {
	if !s.Valid() || at.IsZero() || change.AgentID == "" || (change.NewParentID != nil && *change.NewParentID == "") {
		return foundation.ErrValidation
	}
	// 同一关系空间串行化，并在锁内再次读取与检查，阻止并发互绑成环。
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_relation_spaces VALUES(?,?) ON DUPLICATE KEY UPDATE app_id=app_id`, s.Tenant, s.App); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `SELECT 1 FROM agent_relation_spaces WHERE tenant_id=? AND app_id=? FOR UPDATE`, s.Tenant, s.App); err != nil {
		return err
	}
	revision, err := r.Revision(ctx, tx, s, change.AgentID)
	if err != nil {
		return err
	}
	if revision != change.ExpectedRevision {
		return agent.ErrStale
	}
	seen := map[string]bool{change.AgentID: true}
	parent := change.NewParentID
	for parent != nil {
		if seen[*parent] {
			return agent.ErrCycle
		}
		seen[*parent] = true
		if _, err = r.Revision(ctx, tx, s, *parent); err != nil {
			return err
		}
		var next *string
		err = tx.QueryRowContext(ctx, `SELECT parent_id FROM agent_relations WHERE tenant_id=? AND app_id=? AND agent_id=? AND effective_to IS NULL FOR UPDATE`, s.Tenant, s.App, *parent).Scan(&next)
		if errors.Is(err, sql.ErrNoRows) {
			return agent.ErrHistory
		}
		if err != nil {
			return err
		}
		parent = next
	}
	var previous time.Time
	err = tx.QueryRowContext(ctx, `SELECT effective_from FROM agent_relations WHERE tenant_id=? AND app_id=? AND agent_id=? AND effective_to IS NULL FOR UPDATE`, s.Tenant, s.App, change.AgentID).Scan(&previous)
	if errors.Is(err, sql.ErrNoRows) {
		return agent.ErrHistory
	}
	if err != nil {
		return err
	}
	at = at.UTC().Truncate(time.Microsecond)
	if !at.After(previous) {
		return foundation.ErrValidation
	}
	if _, err = tx.ExecContext(ctx, `UPDATE agent_relations SET effective_to=? WHERE tenant_id=? AND app_id=? AND agent_id=? AND effective_to IS NULL`, at, s.Tenant, s.App, change.AgentID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO agent_relations VALUES(?,?,?,?,?,NULL)`, s.Tenant, s.App, change.AgentID, change.NewParentID, at); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE agent_accounts SET revision=revision+1 WHERE tenant_id=? AND app_id=? AND agent_id=? AND revision=?`, s.Tenant, s.App, change.AgentID, revision)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return agent.ErrStale
	}
	return nil
}

// Ancestors 保留物理层数；分页从历史同一时点续查，绝不宣称一次无限计算。
func (r Repository) Ancestors(ctx context.Context, tx ports.Executor, s foundation.Scope, id string, at time.Time, offset, limit int) ([]agent.Ancestor, bool, error) {
	if !s.Valid() || at.IsZero() || limit < 1 || limit > 100 || offset < 0 {
		return nil, false, foundation.ErrValidation
	}
	if _, err := r.Revision(ctx, tx, s, id); err != nil {
		return nil, false, err
	}
	items := []agent.Ancestor{}
	seen := map[string]bool{id: true}
	current := id
	for depth := 1; depth <= offset+limit+1; depth++ {
		var parent *string
		var from time.Time
		var to *time.Time
		err := tx.QueryRowContext(ctx, `SELECT parent_id,effective_from,effective_to FROM agent_relations WHERE tenant_id=? AND app_id=? AND agent_id=? AND effective_from<=? AND (effective_to IS NULL OR effective_to>?)`, s.Tenant, s.App, current, at.UTC(), at.UTC()).Scan(&parent, &from, &to)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false, agent.ErrHistory
		}
		if err != nil {
			return nil, false, err
		}
		if parent == nil {
			return items, false, nil
		}
		if seen[*parent] {
			return nil, false, agent.ErrCycle
		}
		seen[*parent] = true
		if _, err = r.Revision(ctx, tx, s, *parent); err != nil {
			return nil, false, agent.ErrHistory
		}
		if depth > offset+limit {
			return items, true, nil
		}
		if depth > offset {
			items = append(items, agent.Ancestor{AgentID: *parent, Depth: depth, EffectiveFrom: from, EffectiveTo: to})
		}
		current = *parent
	}
	return items, false, nil
}

type Contract = agent.Contract

func (Repository) AddContract(ctx context.Context, tx ports.Executor, s foundation.Scope, c Contract) error {
	if !s.Valid() || !foundation.ValidID(c.ID) || !foundation.ValidID(c.AgentID) || !foundation.ValidID(c.PlanID) || !foundation.ValidID(c.Evidence) || c.From.IsZero() || (c.To != nil && !c.To.After(c.From)) {
		return foundation.ErrValidation
	}
	var id string
	if err := tx.QueryRowContext(ctx, `SELECT agent_id FROM agent_accounts WHERE tenant_id=? AND app_id=? AND agent_id=? FOR UPDATE`, s.Tenant, s.App, c.AgentID).Scan(&id); err != nil {
		return err
	}
	var count int
	query := `SELECT COUNT(*) FROM agent_contracts WHERE tenant_id=? AND app_id=? AND agent_id=? AND plan_id=? AND (effective_to IS NULL OR effective_to>?)`
	args := []any{s.Tenant, s.App, c.AgentID, c.PlanID, c.From.UTC()}
	if c.To != nil {
		query += " AND effective_from<?"
		args = append(args, c.To.UTC())
	}
	if err := tx.QueryRowContext(ctx, query, args...).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return foundation.ErrConflict
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO agent_contracts VALUES(?,?,?,?,?,?,?,?)`, s.Tenant, s.App, c.ID, c.AgentID, c.PlanID, c.From.UTC(), c.To, c.Evidence)
	return err
}
func (Repository) ContractAt(ctx context.Context, tx ports.Executor, s foundation.Scope, agentID, planID string, at time.Time) (Contract, error) {
	var c Contract
	err := tx.QueryRowContext(ctx, `SELECT contract_id,agent_id,plan_id,effective_from,effective_to,evidence_ref FROM agent_contracts WHERE tenant_id=? AND app_id=? AND agent_id=? AND plan_id=? AND effective_from<=? AND (effective_to IS NULL OR effective_to>?)`, s.Tenant, s.App, agentID, planID, at.UTC(), at.UTC()).Scan(&c.ID, &c.AgentID, &c.PlanID, &c.From, &c.To, &c.Evidence)
	return c, err
}
