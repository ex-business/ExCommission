package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/ex-business/ExCommission/internal/platform/ports"
)

type Entry struct {
	Tenant, App, ID, Subject, RequestID, Type, Action, Resource string
	Summary                                                     map[string]string
	At                                                          time.Time
}

func Append(ctx context.Context, tx ports.Executor, e Entry) error {
	if e.Tenant == "" || e.App == "" || e.Subject == "" || e.RequestID == "" || (e.Type != "OPERATION" && e.Type != "SECURITY") || e.At.IsZero() {
		return errors.New("invalid audit context")
	}
	// 白名单只保留引用与结果，禁止载荷、凭据及任意输入自动进入审计。
	safe := map[string]string{}
	for _, key := range []string{"before_revision", "after_revision", "result_code", "object_hash"} {
		if value, ok := e.Summary[key]; ok {
			safe[key] = value
		}
	}
	body, err := json.Marshal(safe)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO audit_entries(tenant_id,app_id,entry_id,subject_id,request_id,log_type,action,resource_ref,summary,occurred_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, e.Tenant, e.App, e.ID, e.Subject, e.RequestID, e.Type, e.Action, e.Resource, body, e.At.UTC())
	return err
}
