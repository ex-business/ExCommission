package adapters

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"time"

	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	"github.com/ex-business/ExCommission/internal/platform/ports"
)

type InboxEvent struct {
	Scope                             foundation.Scope
	Source, EventID, BusinessID, Type string
	CanonicalBusiness                 []byte
	At                                time.Time
}

// AcceptInbox 的CanonicalBusiness由schema规范化且排除event_id；新event_id不能绕过业务唯一键。
func AcceptInbox(ctx context.Context, tx ports.Executor, event InboxEvent) (string, bool, error) {
	if !event.Scope.Valid() || event.Source == "" || event.EventID == "" || event.BusinessID == "" || event.At.IsZero() {
		return "", false, foundation.ErrValidation
	}
	sum := sha256.Sum256(event.CanonicalBusiness)
	_, err := tx.ExecContext(ctx, `INSERT INTO integration_inbox VALUES(?,?,?,?,?,?,?,?,?,?) ON DUPLICATE KEY UPDATE source_id=source_id`, event.Scope.Tenant, event.Scope.App, event.Source, event.EventID, event.BusinessID, event.Type, event.CanonicalBusiness, sum[:], "ACCEPTED", event.At.UTC())
	if err != nil {
		return "", false, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT event_id,business_id,event_type,canonical_body FROM integration_inbox WHERE tenant_id=? AND app_id=? AND source_id=? AND (event_id=? OR (business_id=? AND event_type=?)) FOR UPDATE`, event.Scope.Tenant, event.Scope.App, event.Source, event.EventID, event.BusinessID, event.Type)
	if err != nil {
		return "", false, err
	}
	defer rows.Close()
	count := 0
	original := ""
	for rows.Next() {
		var eventID, businessID, eventType string
		var body []byte
		if err = rows.Scan(&eventID, &businessID, &eventType, &body); err != nil {
			return "", false, err
		}
		count++
		original = eventID
		if businessID != event.BusinessID || eventType != event.Type || !bytes.Equal(body, event.CanonicalBusiness) {
			return "", false, foundation.ErrConflict
		}
	}
	if err = rows.Err(); err != nil {
		return "", false, err
	}
	if count != 1 {
		return "", false, errors.New("inbox uniqueness invariant")
	}
	// 首次与原event_id重放都返回同一标识，调用方不能以该标志决定是否重复建权益。
	return original, original != event.EventID, nil
}
