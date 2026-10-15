package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"time"
)

// Send implements the local notification inbox; no external message is sent.
func (r Repository) Send(ctx context.Context, tx db.Executor, s foundation.Scope, id, topic string, payload []byte, at time.Time) error {
	enabled, _, err := r.Component(ctx, tx, s, "NOTIFICATION", "local-inbox")
	if err != nil {
		return err
	}
	if !enabled {
		return nil
	}
	sum := sha256.Sum256(payload)
	hash := hex.EncodeToString(sum[:])
	if _, err = tx.ExecContext(ctx, `INSERT IGNORE INTO platform_notifications VALUES(?,?,?,?,?,?,?)`, s.Tenant, s.App, id, topic, id, hash, at); err != nil {
		return err
	}
	var old string
	if err = tx.QueryRowContext(ctx, `SELECT payload_hash FROM platform_notifications WHERE tenant_id=? AND app_id=? AND event_id=?`, s.Tenant, s.App, id).Scan(&old); err != nil {
		return err
	}
	if old != hash {
		return foundation.ErrConflict
	}
	return nil
}
func (Repository) Notifications(ctx context.Context, tx db.Executor, s foundation.Scope) ([]map[string]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT event_id,topic,resource_ref,payload_hash,created_at FROM platform_notifications WHERE tenant_id=? AND app_id=? ORDER BY created_at DESC,event_id LIMIT 100`, s.Tenant, s.App)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]string{}
	for rows.Next() {
		var id, topic, ref, hash string
		var at time.Time
		if err = rows.Scan(&id, &topic, &ref, &hash, &at); err != nil {
			return nil, err
		}
		out = append(out, map[string]string{"event_id": id, "topic": topic, "resource_ref": ref, "payload_hash": hash, "created_at": at.Format(time.RFC3339Nano)})
	}
	return out, rows.Err()
}
