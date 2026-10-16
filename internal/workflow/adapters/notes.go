package adapters

import (
	"context"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"time"
)

func (Repository) Note(ctx context.Context, tx db.Executor, s foundation.Scope, id string, revision uint64, actor, note string, at time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO workflow_decision_notes VALUES(?,?,?,?,?,?,?)`, s.Tenant, s.App, id, revision, actor, note, at)
	return err
}
