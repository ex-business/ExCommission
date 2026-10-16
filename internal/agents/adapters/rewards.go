package adapters

import (
	"context"
	"database/sql"
	"errors"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"time"
)

// RewardEligible 与停用和资格变更共用账户/方案状态锁；不能借领取接口绕过资格审批。
func (r Repository) RewardEligible(ctx context.Context, tx db.Executor, s foundation.Scope, agent, plan string, at time.Time) error {
	if err := r.Active(ctx, tx, s, agent, at); err != nil {
		return err
	}
	var revision uint64
	err := tx.QueryRowContext(ctx, `SELECT revision FROM agent_plan_states WHERE tenant_id=? AND app_id=? AND agent_id=? AND plan_id=? FOR UPDATE`, s.Tenant, s.App, agent, plan).Scan(&revision)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("EVIDENCE_NOT_CONFIRMED")
	}
	if err != nil {
		return err
	}
	var eligibility string
	err = tx.QueryRowContext(ctx, `SELECT eligibility FROM agent_grade_history WHERE tenant_id=? AND app_id=? AND agent_id=? AND plan_id=? AND effective_from<=? AND (effective_to IS NULL OR effective_to>?) FOR UPDATE`, s.Tenant, s.App, agent, plan, at, at).Scan(&eligibility)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("EVIDENCE_NOT_CONFIRMED")
	}
	if err != nil {
		return err
	}
	if eligibility != "ELIGIBLE" {
		return errors.New("EVIDENCE_NOT_CONFIRMED")
	}
	return nil
}
