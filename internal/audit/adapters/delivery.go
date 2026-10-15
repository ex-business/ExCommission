package adapters

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"time"
)

// Deliver stores only an event reference and digest; raw outbox payload is never audit text.
func Deliver(ctx context.Context, tx db.Executor, s foundation.Scope, id, topic string, payload []byte, at time.Time) error {
	if !s.Valid() || !foundation.ValidID(id) || !foundation.ValidID(topic) || at.IsZero() {
		return foundation.ErrValidation
	}
	sum := sha256.Sum256(payload)
	digest := hex.EncodeToString(sum[:])
	entryID := "outbox_" + id
	if len(entryID) > 128 {
		return foundation.ErrValidation
	}
	summary, _ := json.Marshal(map[string]string{"result_code": "DELIVERED", "object_hash": digest})
	if _, err := tx.ExecContext(ctx, `INSERT IGNORE INTO audit_entries(tenant_id,app_id,entry_id,subject_id,request_id,log_type,action,resource_ref,summary,occurred_at) VALUES(?,?,?,'system-outbox',?,'OPERATION',?,?,?,?)`, s.Tenant, s.App, entryID, id, topic, id, summary, at); err != nil {
		return err
	}
	var saved, action, resource string
	err := tx.QueryRowContext(ctx, `SELECT JSON_UNQUOTE(JSON_EXTRACT(summary,'$.object_hash')),action,resource_ref FROM audit_entries WHERE tenant_id=? AND app_id=? AND entry_id=?`, s.Tenant, s.App, entryID).Scan(&saved, &action, &resource)
	if err != nil {
		return err
	}
	if saved != digest || action != topic || resource != id {
		return errors.New("IDEMPOTENCY_CONFLICT")
	}
	return nil
}
