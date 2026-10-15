package adapters

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/ex-business/ExCommission/internal/integration/domain"
	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	db "github.com/ex-business/ExCommission/internal/platform/ports"
	"time"
)

type Repository struct{}

func (Repository) Accept(ctx context.Context, tx db.Executor, s foundation.Scope, event domain.Event, canonical []byte, at time.Time) (string, error) {
	// 业务内容排除事件ID，另有source+event_id别名永久防重。
	body, err := json.Marshal(event)
	if err != nil {
		return "", err
	}
	var object map[string]json.RawMessage
	if err = json.Unmarshal(body, &object); err != nil {
		return "", err
	}
	delete(object, "event_id")
	business, err := json.Marshal(object)
	if err != nil {
		return "", err
	}
	business, err = foundation.BusinessCanonicalJSON(business)
	if err != nil {
		return "", err
	}
	original, _, err := AcceptInbox(ctx, tx, InboxEvent{Scope: s, Source: event.Source, EventID: event.ID, BusinessID: event.BusinessID, Type: event.Type, CanonicalBusiness: business, At: at})
	return original, err
}
func (Repository) SaveRaw(ctx context.Context, tx db.Executor, s foundation.Scope, event domain.Event, raw []byte) error {
	sum := sha256.Sum256(raw)
	_, err := tx.ExecContext(ctx, `INSERT IGNORE INTO integration_raw_payloads VALUES(?,?,?,?,?,?)`, s.Tenant, s.App, event.Source, event.ID, raw, hex.EncodeToString(sum[:]))
	return err
}
