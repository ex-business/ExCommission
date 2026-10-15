package adapters

import (
	"context"
	"github.com/ex-business/ExCommission/internal/ledger/domain"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"time"
)

// PendingBefore exposes ledger-owned records to settlement without granting write access.
func (r Repository) PendingBefore(ctx context.Context, tx db.Executor, s foundation.Scope, before time.Time) ([]domain.Entitlement, error) {
	rows, err := tx.QueryContext(ctx, `SELECT entitlement_id FROM ledger_entitlements WHERE tenant_id=? AND app_id=? AND pending>0 AND business_time<? ORDER BY business_time,entitlement_id`, s.Tenant, s.App, before)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	result := []domain.Entitlement{}
	for _, id := range ids {
		e, err := r.Get(ctx, tx, s, id, true)
		if err != nil {
			return nil, err
		}
		if e.Pending != "0" && !e.Frozen {
			result = append(result, e)
		}
	}
	return result, nil
}
