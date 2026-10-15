package adapters

import (
	"context"
	"database/sql"
	"errors"
	agent "github.com/ex-business/ExCommission/internal/agents/domain"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"time"
)

func (Repository) PaymentAllowed(ctx context.Context, tx db.Executor, s foundation.Scope, id string) error {
	var status string
	err := tx.QueryRowContext(ctx, `SELECT status FROM agent_accounts WHERE tenant_id=? AND app_id=? AND agent_id=? FOR UPDATE`, s.Tenant, s.App, id).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("NOT_FOUND")
	}
	if err != nil {
		return err
	}
	if status == "FROZEN" {
		return errors.New("FUNDS_FROZEN")
	}
	return nil
}
func (r Repository) SetStatus(ctx context.Context, tx db.Executor, s foundation.Scope, id, status string, revision uint64, evidence string, at time.Time) error {
	if status != "ACTIVE" && status != "FROZEN" && status != "DISABLED" && status != "CLOSED" {
		return foundation.ErrValidation
	}
	var current string
	var actual uint64
	err := tx.QueryRowContext(ctx, `SELECT status,revision FROM agent_accounts WHERE tenant_id=? AND app_id=? AND agent_id=? FOR UPDATE`, s.Tenant, s.App, id).Scan(&current, &actual)
	if errors.Is(err, sql.ErrNoRows) {
		return agent.ErrNotFound
	}
	if err != nil {
		return err
	}
	if actual != revision {
		return agent.ErrStale
	}
	if current == status || current == "CLOSED" {
		return errors.New("STATE_CONFLICT")
	}
	var from time.Time
	if err = tx.QueryRowContext(ctx, `SELECT effective_from FROM agent_status_history WHERE tenant_id=? AND app_id=? AND agent_id=? AND effective_to IS NULL FOR UPDATE`, s.Tenant, s.App, id).Scan(&from); err != nil {
		return err
	}
	if !at.After(from) {
		return foundation.ErrValidation
	}
	if _, err = tx.ExecContext(ctx, `UPDATE agent_status_history SET effective_to=? WHERE tenant_id=? AND app_id=? AND agent_id=? AND effective_to IS NULL`, at, s.Tenant, s.App, id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO agent_status_history VALUES(?,?,?,?,?,NULL,?)`, s.Tenant, s.App, id, status, at, evidence); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE agent_accounts SET status=?,revision=revision+1 WHERE tenant_id=? AND app_id=? AND agent_id=?`, status, s.Tenant, s.App, id)
	return err
}
