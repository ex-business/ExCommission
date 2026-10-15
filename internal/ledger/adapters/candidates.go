package adapters

import (
	"context"
	"github.com/ex-business/ExCommission/internal/ledger/domain"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"time"
)

// PendingBefore exposes ledger-owned records to settlement without granting write access.
func (r Repository) PendingBefore(ctx context.Context, tx db.Executor, s foundation.Scope, before, postedAt time.Time) ([]domain.Entitlement, error) {
	rows, err := tx.QueryContext(ctx, `SELECT e.entitlement_id FROM ledger_entitlements e JOIN ledger_payout_policies p ON p.tenant_id=e.tenant_id AND p.app_id=e.app_id AND p.entitlement_id=e.entitlement_id WHERE e.tenant_id=? AND e.app_id=? AND (e.pending>0 OR (e.available>0 AND e.available<p.minimum)) AND e.business_time<? ORDER BY e.business_time,e.entitlement_id`, s.Tenant, s.App, before)
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
		if e.Reserved != "0" {
			continue
		}
		if !e.Frozen && e.Available != "0" {
			if err = r.Requeue(ctx, tx, s, e.ID, "minimum-rollover-"+before.Format("20060102"), postedAt); err != nil {
				return nil, err
			}
			e, err = r.Get(ctx, tx, s, e.ID, true)
			if err != nil {
				return nil, err
			}
		}
		if e.Pending != "0" && !e.Frozen {
			result = append(result, e)
		}
	}
	return result, nil
}
